// Package scheduler is the facade: it owns schedule lifecycle, revision
// boundaries, batch reschedule, authorization and audit.
package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

// Config tunes the scheduler.
type Config struct {
	Horizon        time.Duration
	MaxOccurrences int
}

// Scheduler is the application service. All mutations are serialized by
// mu so that concurrent requests (including batch reschedules) never lose
// updates.
type Scheduler struct {
	mu     *sync.Mutex
	store  store.Store
	tzp    tz.Provider
	engine *schedule.Engine
	clock  clock.Clock
	cfg    Config
}

// New returns a Scheduler.
func New(st store.Store, tzp tz.Provider, engine *schedule.Engine, clk clock.Clock, cfg Config) *Scheduler {
	if cfg.Horizon <= 0 {
		cfg.Horizon = 400 * 24 * time.Hour
	}
	if cfg.MaxOccurrences <= 0 {
		cfg.MaxOccurrences = 2000
	}
	return &Scheduler{
		mu:     &sync.Mutex{},
		store:  st,
		tzp:    tzp,
		engine: engine,
		clock:  clk,
		cfg:    cfg,
	}
}

// CreateInput is the input for creating a schedule.
type CreateInput struct {
	ActorID  string
	Timezone string
	Rule     schedule.RuleEnvelope
	Now      *time.Time // optional override
}

// Create creates a schedule and materializes its initial triggers.
func (s *Scheduler) Create(ctx context.Context, in CreateInput) (*schedule.Schedule, []schedule.Occurrence, error) {
	if in.ActorID == "" {
		return nil, nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	loc, err := s.tzp.Resolve(in.Timezone)
	if err != nil {
		return nil, nil, err
	}
	if _, err := schedule.RecurrenceFromRule(in.Rule); err != nil {
		return nil, nil, err
	}
	now := s.now(in.Now)

	occs, err := s.engine.Materialize(in.Rule, loc, now, 1)
	if err != nil {
		return nil, nil, err
	}
	if len(occs) > s.cfg.MaxOccurrences {
		occs = occs[:s.cfg.MaxOccurrences]
	}

	sch := &schedule.Schedule{
		ID:       newID("sch_"),
		OwnerID:  in.ActorID,
		Timezone: in.Timezone,
		Rule:     in.Rule,
		Status:   schedule.StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
		Revision: 1,
		NextSeq:  int64(len(occs)) + 1,
	}
	rev := &schedule.Revision{
		ID:          1,
		ScheduleID:  sch.ID,
		Revision:    1,
		Rule:        in.Rule,
		EffectiveAt: now,
		RulesVersion: s.engine.RulesVersion(),
		CreatedAt:   now,
		ActorID:     in.ActorID,
		Reason:      "created",
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.store.PutSchedule(ctx, sch); err != nil {
		return nil, nil, err
	}
	if err := s.store.PutRevision(ctx, rev); err != nil {
		return nil, nil, err
	}
	for _, o := range occs {
		t := occurrenceToTrigger(o, sch.ID, now)
		if err := s.store.PutTrigger(ctx, t); err != nil {
			return nil, nil, err
		}
	}
	s.audit(ctx, in.ActorID, "schedule.create", sch.ID, "ok", "", fmt.Sprintf("tz=%s rule=%s", in.Timezone, describeRule(in.Rule)))
	return sch, occs, nil
}

// UpdateInput is the input for modifying a schedule.
type UpdateInput struct {
	ActorID     string
	ScheduleID  string
	Timezone    *string                  // nil = unchanged
	Rule        *schedule.RuleEnvelope   // nil = unchanged
	EffectiveAt *time.Time              // nil = now
	Reason      string
}

// Update modifies a schedule with a deterministic effective boundary.
// Triggers before the boundary are immutable history; triggers at/after
// the boundary (and not yet delivered) are recomputed under the new rule.
func (s *Scheduler) Update(ctx context.Context, in UpdateInput) (*schedule.Schedule, *UpdateReport, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(ctx, in)
}

func (s *Scheduler) updateLocked(ctx context.Context, in UpdateInput) (*schedule.Schedule, *UpdateReport, error) {
	if in.ActorID == "" {
		return nil, nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	now := s.now(nil)
	effectiveAt := now
	if in.EffectiveAt != nil {
		effectiveAt = *in.EffectiveAt
	}

	sch, err := s.store.GetSchedule(ctx, in.ScheduleID)
	if err != nil {
		return nil, nil, err
	}
	if sch.OwnerID != in.ActorID {
		s.audit(ctx, in.ActorID, "schedule.update", in.ScheduleID, "denied", "unauthorized", "")
		return nil, nil, fmt.Errorf("%w: cannot modify another user's schedule", schedule.ErrUnauthorized)
	}
	if sch.Status != schedule.StatusActive {
		return nil, nil, fmt.Errorf("%w: schedule is %s", schedule.ErrScheduleNotActive, sch.Status)
	}

	newRule := sch.Rule
	if in.Rule != nil {
		if _, err := schedule.RecurrenceFromRule(*in.Rule); err != nil {
			return nil, nil, err
		}
		newRule = *in.Rule
	}
	newTZ := sch.Timezone
	if in.Timezone != nil {
		loc, err := s.tzp.Resolve(*in.Timezone)
		if err != nil {
			return nil, nil, err
		}
		_ = loc
		newTZ = *in.Timezone
	}
	loc, err := s.tzp.Resolve(newTZ)
	if err != nil {
		return nil, nil, err
	}

	// Boundary: history is before recomputeFrom; future is recomputed.
	recomputeFrom := effectiveAt
	if recomputeFrom.Before(now) {
		recomputeFrom = now
	}

	existing, err := s.store.ListTriggers(ctx, sch.ID)
	if err != nil {
		return nil, nil, err
	}
	var keptSeqs, removedSeqs []int64
	for _, t := range existing {
		if t.ScheduledAt.Before(recomputeFrom) {
			keptSeqs = append(keptSeqs, t.Seq)
			continue
		}
		// Future trigger: remove if not yet delivered.
		if t.Status == schedule.TriggerPlanned || t.Status == schedule.TriggerFailed {
			removedSeqs = append(removedSeqs, t.Seq)
		} else {
			keptSeqs = append(keptSeqs, t.Seq)
		}
	}

	// Recompute future occurrences under the new rule.
	occs, err := s.engine.Generate(newRule, loc, recomputeFrom, recomputeFrom.Add(s.cfg.Horizon), sch.NextSeq)
	if err != nil {
		return nil, nil, err
	}
	if len(occs) > s.cfg.MaxOccurrences {
		occs = occs[:s.cfg.MaxOccurrences]
	}

	// Persist: delete removed, insert new, update schedule + revision.
	if _, err := s.store.DeletePlannedTriggers(ctx, sch.ID, recomputeFrom); err != nil {
		return nil, nil, err
	}
	for _, o := range occs {
		t := occurrenceToTrigger(o, sch.ID, now)
		if err := s.store.PutTrigger(ctx, t); err != nil {
			return nil, nil, err
		}
	}

	sch.Rule = newRule
	sch.Timezone = newTZ
	sch.UpdatedAt = now
	sch.Revision++
	sch.NextSeq = sch.NextSeq + int64(len(occs))
	if err := s.store.PutSchedule(ctx, sch); err != nil {
		return nil, nil, err
	}
	rev := &schedule.Revision{
		ID:          sch.Revision,
		ScheduleID:  sch.ID,
		Revision:    sch.Revision,
		Rule:        newRule,
		EffectiveAt: effectiveAt,
		RulesVersion: s.engine.RulesVersion(),
		CreatedAt:   now,
		ActorID:     in.ActorID,
		Reason:      in.Reason,
	}
	if err := s.store.PutRevision(ctx, rev); err != nil {
		return nil, nil, err
	}

	warnings := []string{}
	if effectiveAt.Before(now) {
		warnings = append(warnings, "effectiveAt is in the past; triggers in [effectiveAt, now) under the new rule are not retroactively materialized")
	}
	report := &UpdateReport{
		ScheduleID:  sch.ID,
		Revision:    sch.Revision,
		EffectiveAt: effectiveAt,
		KeptSeqs:    keptSeqs,
		RemovedSeqs: removedSeqs,
		Added:       occs,
		Warnings:    warnings,
	}
	s.audit(ctx, in.ActorID, "schedule.update", sch.ID, "ok", "", fmt.Sprintf("rev=%d kept=%d removed=%d added=%d", sch.Revision, len(keptSeqs), len(removedSeqs), len(occs)))
	return sch, report, nil
}

// Cancel cancels a schedule, removing future triggers but keeping history.
func (s *Scheduler) Cancel(ctx context.Context, actorID, scheduleID, reason string) (*schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cancelLocked(ctx, actorID, scheduleID, reason)
}

func (s *Scheduler) cancelLocked(ctx context.Context, actorID, scheduleID, reason string) (*schedule.Schedule, error) {
	if actorID == "" {
		return nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	sch, err := s.store.GetSchedule(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	if sch.OwnerID != actorID {
		s.audit(ctx, actorID, "schedule.cancel", scheduleID, "denied", "unauthorized", "")
		return nil, fmt.Errorf("%w: cannot cancel another user's schedule", schedule.ErrUnauthorized)
	}
	now := s.now(nil)
	if _, err := s.store.DeletePlannedTriggers(ctx, sch.ID, now); err != nil {
		return nil, err
	}
	sch.Status = schedule.StatusCancelled
	sch.UpdatedAt = now
	sch.Revision++
	if err := s.store.PutSchedule(ctx, sch); err != nil {
		return nil, err
	}
	rev := &schedule.Revision{
		ID:          sch.Revision,
		ScheduleID:  sch.ID,
		Revision:    sch.Revision,
		Rule:        sch.Rule,
		EffectiveAt: now,
		RulesVersion: s.engine.RulesVersion(),
		CreatedAt:   now,
		ActorID:     actorID,
		Reason:      reason,
	}
	if err := s.store.PutRevision(ctx, rev); err != nil {
		return nil, err
	}
	s.audit(ctx, actorID, "schedule.cancel", scheduleID, "ok", "", reason)
	return sch, nil
}

// Get returns a schedule (owner-checked).
func (s *Scheduler) Get(ctx context.Context, actorID, scheduleID string) (*schedule.Schedule, error) {
	if actorID == "" {
		return nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sch, err := s.store.GetSchedule(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	if sch.OwnerID != actorID {
		s.audit(ctx, actorID, "schedule.get", scheduleID, "denied", "unauthorized", "")
		return nil, fmt.Errorf("%w: cannot access another user's schedule", schedule.ErrUnauthorized)
	}
	return sch, nil
}

// List returns schedules owned by actorID.
func (s *Scheduler) List(ctx context.Context, actorID string) ([]*schedule.Schedule, error) {
	if actorID == "" {
		return nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.ListSchedules(ctx, actorID)
}

// Triggers returns materialized triggers for a schedule (owner-checked).
func (s *Scheduler) Triggers(ctx context.Context, actorID, scheduleID string) ([]*schedule.Trigger, error) {
	if actorID == "" {
		return nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sch, err := s.store.GetSchedule(ctx, scheduleID)
	if err != nil {
		return nil, err
	}
	if sch.OwnerID != actorID {
		s.audit(ctx, actorID, "schedule.triggers", scheduleID, "denied", "unauthorized", "")
		return nil, fmt.Errorf("%w: cannot access another user's schedule", schedule.ErrUnauthorized)
	}
	return s.store.ListTriggers(ctx, scheduleID)
}

// UpdateReport describes the effect of a modification.
type UpdateReport struct {
	ScheduleID  string                `json:"schedule_id"`
	Revision    int64                 `json:"revision"`
	EffectiveAt time.Time             `json:"effective_at"`
	KeptSeqs    []int64               `json:"kept_seqs"`
	RemovedSeqs []int64               `json:"removed_seqs"`
	Added       []schedule.Occurrence `json:"added"`
	Warnings    []string              `json:"warnings"`
}

func (s *Scheduler) now(override *time.Time) time.Time {
	if override != nil {
		return override.UTC()
	}
	return s.clock.Now().UTC()
}

func occurrenceToTrigger(o schedule.Occurrence, scheduleID string, now time.Time) *schedule.Trigger {
	return &schedule.Trigger{
		ScheduleID:   scheduleID,
		Seq:          o.Seq,
		ScheduledAt:  o.ScheduledAt,
		Wall:         o.Wall,
		Status:       schedule.TriggerPlanned,
		Ambiguous:    o.Ambiguous,
		Skipped:      o.Skipped,
		Shifted:      o.Shifted,
		Resolution:   o.Reason,
		RulesVersion: o.RulesVersion,
		CreatedAt:    now,
	}
}

func describeRule(r schedule.RuleEnvelope) string {
	switch r.Kind {
	case schedule.KindWeekly:
		if r.Weekly != nil {
			return r.Weekly.Describe()
		}
	case schedule.KindMonthly:
		if r.Monthly != nil {
			return r.Monthly.Describe()
		}
	}
	return "unknown"
}

func newID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

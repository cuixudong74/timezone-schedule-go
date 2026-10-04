package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
)

// RescheduleItem is one entry in a batch reschedule.
type RescheduleItem struct {
	ScheduleID  string
	Timezone    *string                // nil = unchanged
	Rule        *schedule.RuleEnvelope // nil = unchanged
	EffectiveAt *time.Time             // nil = now
	Cancel      bool                   // true = cancel instead of update
	Reason      string
}

// ItemResult is the per-item outcome of a batch reschedule.
type ItemResult struct {
	ScheduleID string
	Action     string // "updated", "cancelled", "error"
	Report     *UpdateReport
	Error      string
}

// BatchResult is the aggregate outcome.
type BatchResult struct {
	Results []ItemResult
}

// BatchReschedule applies a batch of modifications atomically with respect
// to other scheduler mutations: concurrent create/update/cancel calls
// serialize on the scheduler lock, so requests arriving during the batch
// are applied afterwards and never lost. Each item succeeds or fails
// independently; the result is fully explainable per item.
func (s *Scheduler) BatchReschedule(ctx context.Context, actorID string, items []RescheduleItem) (*BatchResult, error) {
	if actorID == "" {
		return nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	out := &BatchResult{Results: make([]ItemResult, 0, len(items))}
	for _, item := range items {
		res := ItemResult{ScheduleID: item.ScheduleID}
		if item.Cancel {
			_, err := s.cancelLocked(ctx, actorID, item.ScheduleID, item.Reason)
			if err != nil {
				res.Action = "error"
				res.Error = err.Error()
			} else {
				res.Action = "cancelled"
			}
			out.Results = append(out.Results, res)
			continue
		}
		_, report, err := s.updateLocked(ctx, UpdateInput{
			ActorID:     actorID,
			ScheduleID:  item.ScheduleID,
			Timezone:    item.Timezone,
			Rule:        item.Rule,
			EffectiveAt: item.EffectiveAt,
			Reason:      item.Reason,
		})
		if err != nil {
			res.Action = "error"
			res.Error = err.Error()
		} else {
			res.Action = "updated"
			res.Report = report
		}
		out.Results = append(out.Results, res)
	}
	s.audit(ctx, actorID, "schedule.batch_reschedule", "", "ok", "", fmt.Sprintf("items=%d", len(items)))
	return out, nil
}

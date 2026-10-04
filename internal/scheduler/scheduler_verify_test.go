package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

func newTestScheduler(t *testing.T, now time.Time) *Scheduler {
	t.Helper()
	st := store.NewMemStore()
	tzp := tz.NewEmbeddedProvider("test")
	engine := schedule.NewEngine(tzp, tz.DefaultPolicy(), schedule.MonthDaySkip, 400*24*time.Hour)
	return New(st, tzp, engine, clock.NewFake(now), Config{Horizon: 400 * 24 * time.Hour})
}

func TestCreateMaterializes(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newTestScheduler(t, now)
	sch, occs, err := s.Create(context.Background(), CreateInput{
		ActorID: "alice", Timezone: "America/New_York",
		Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
			Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sch.ID == "" || sch.Revision != 1 {
		t.Fatalf("bad schedule: %+v", sch)
	}
	if len(occs) == 0 {
		t.Fatal("no occurrences")
	}
	trigs, err := s.Triggers(context.Background(), "alice", sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trigs) != len(occs) {
		t.Fatalf("stored %d triggers, want %d", len(trigs), len(occs))
	}
	// First trigger: Jun 4 2024 10:00 NY -> 14:00 UTC (EDT)
	if trigs[0].ScheduledAt.UTC().Format("2006-01-02 15:04") != "2024-06-04 14:00" {
		t.Fatalf("first trigger: %s", trigs[0].ScheduledAt.UTC())
	}
}

func TestUpdateBoundary(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newTestScheduler(t, now)
	sch, _, err := s.Create(context.Background(), CreateInput{
		ActorID: "alice", Timezone: "America/New_York",
		Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
			Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Move to Aug 1; update rule to Fridays at 10, effective Sep 1.
	s.clock.(*clock.Fake).Set(time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC))
	eff := time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC)
	newRule := schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
		Weekdays: []time.Weekday{time.Friday}, Hour: 10,
	}}
	updated, report, err := s.Update(context.Background(), UpdateInput{
		ActorID: "alice", ScheduleID: sch.ID, Rule: &newRule, EffectiveAt: &eff,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision: %d", updated.Revision)
	}
	if len(report.KeptSeqs) == 0 {
		t.Fatal("expected kept history")
	}
	if len(report.RemovedSeqs) == 0 {
		t.Fatal("expected removed future triggers")
	}
	if len(report.Added) == 0 {
		t.Fatal("expected added triggers")
	}
	// All added triggers must be Fridays and >= Sep 1.
	for _, o := range report.Added {
		if o.ScheduledAt.Weekday() != time.Friday {
			t.Fatalf("added not Friday: %s", o.ScheduledAt.UTC())
		}
		if o.ScheduledAt.Before(eff) {
			t.Fatalf("added before effective: %s", o.ScheduledAt.UTC())
		}
	}
	// History (before Sep 1) must still be Tuesdays.
	trigs, _ := s.Triggers(context.Background(), "alice", sch.ID)
	for _, tr := range trigs {
		if tr.ScheduledAt.Before(eff) && tr.Status == schedule.TriggerPlanned {
			if tr.ScheduledAt.Weekday() != time.Tuesday {
				t.Fatalf("history changed: %s %s", tr.ScheduledAt.UTC(), tr.ScheduledAt.Weekday())
			}
		}
	}
}

func TestCancel(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newTestScheduler(t, now)
	sch, _, err := s.Create(context.Background(), CreateInput{
		ActorID: "alice", Timezone: "America/New_York",
		Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
			Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.clock.(*clock.Fake).Set(time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC))
	cancelled, err := s.Cancel(context.Background(), "alice", sch.ID, "user request")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Status != schedule.StatusCancelled {
		t.Fatalf("status: %s", cancelled.Status)
	}
	trigs, _ := s.Triggers(context.Background(), "alice", sch.ID)
	for _, tr := range trigs {
		if tr.Status == schedule.TriggerPlanned && !tr.ScheduledAt.Before(time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("future trigger not removed: %s", tr.ScheduledAt.UTC())
		}
	}
}

func TestAuthorization(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newTestScheduler(t, now)
	sch, _, err := s.Create(context.Background(), CreateInput{
		ActorID: "alice", Timezone: "America/New_York",
		Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
			Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Bob cannot access Alice's schedule.
	_, err = s.Get(context.Background(), "bob", sch.ID)
	if err == nil {
		t.Fatal("expected unauthorized")
	}
	// Bob cannot update it.
	newRule := schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{Weekdays: []time.Weekday{time.Friday}, Hour: 10}}
	_, _, err = s.Update(context.Background(), UpdateInput{ActorID: "bob", ScheduleID: sch.ID, Rule: &newRule})
	if err == nil {
		t.Fatal("expected unauthorized update")
	}
	// Alice can.
	_, err = s.Get(context.Background(), "alice", sch.ID)
	if err != nil {
		t.Fatalf("alice should access: %v", err)
	}
}

func TestInvalidTimezone(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newTestScheduler(t, now)
	_, _, err := s.Create(context.Background(), CreateInput{
		ActorID: "alice", Timezone: "Not/AZone",
		Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
			Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
		}},
	})
	if err == nil {
		t.Fatal("expected invalid timezone error")
	}
}

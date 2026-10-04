package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

func TestBatchReschedule(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	s := newTestScheduler(t, now)

	// Create 3 schedules.
	var ids []string
	for i := 0; i < 3; i++ {
		sch, _, err := s.Create(context.Background(), CreateInput{
			ActorID: "alice", Timezone: "America/New_York",
			Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
				Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sch.ID)
	}

	// Batch: update schedule 0, cancel schedule 1, leave schedule 2.
	newRule := schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
		Weekdays: []time.Weekday{time.Friday}, Hour: 10,
	}}
	res, err := s.BatchReschedule(context.Background(), "alice", []RescheduleItem{
		{ScheduleID: ids[0], Rule: &newRule, Reason: "batch update"},
		{ScheduleID: ids[1], Cancel: true, Reason: "batch cancel"},
		{ScheduleID: ids[2]}, // no-op (no rule change)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(res.Results))
	}
	if res.Results[0].Action != "updated" {
		t.Fatalf("item 0: %s", res.Results[0].Action)
	}
	if res.Results[1].Action != "cancelled" {
		t.Fatalf("item 1: %s", res.Results[1].Action)
	}
	if res.Results[2].Action != "updated" {
		// No rule change: updateLocked still runs (recomputes). Accept "updated".
		t.Fatalf("item 2: %s", res.Results[2].Action)
	}

	// Verify schedule 0 now fires on Fridays.
	trigs, _ := s.Triggers(context.Background(), "alice", ids[0])
	eff := now
	for _, tr := range trigs {
		if tr.ScheduledAt.After(eff) && tr.ScheduledAt.Weekday() != time.Friday {
			t.Fatalf("schedule 0 trigger not Friday: %s", tr.ScheduledAt.UTC())
		}
	}
	// Verify schedule 1 is cancelled.
	sch1, _ := s.Get(context.Background(), "alice", ids[1])
	if sch1.Status != schedule.StatusCancelled {
		t.Fatalf("schedule 1 status: %s", sch1.Status)
	}
}

func TestBatchConcurrentRequestsNotLost(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	st := store.NewMemStore()
	tzp := tz.NewEmbeddedProvider("test")
	engine := schedule.NewEngine(tzp, tz.DefaultPolicy(), schedule.MonthDaySkip, 400*24*time.Hour)
	s := New(st, tzp, engine, clock.NewFake(now), Config{Horizon: 400 * 24 * time.Hour})

	// Create 5 schedules.
	var ids []string
	for i := 0; i < 5; i++ {
		sch, _, err := s.Create(context.Background(), CreateInput{
			ActorID: "alice", Timezone: "America/New_York",
			Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
				Weekdays: []time.Weekday{time.Tuesday}, Hour: 10,
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sch.ID)
	}

	// Concurrently create new schedules while the batch runs.
	var wg sync.WaitGroup
	concurrentCreated := 0
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.Create(context.Background(), CreateInput{
				ActorID: "alice", Timezone: "America/New_York",
				Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
					Weekdays: []time.Weekday{time.Wednesday}, Hour: 10,
				}},
			})
			if err == nil {
				mu.Lock()
				concurrentCreated++
				mu.Unlock()
			}
		}(i)
	}

	// Run the batch.
	newRule := schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
		Weekdays: []time.Weekday{time.Friday}, Hour: 10,
	}}
	items := make([]RescheduleItem, len(ids))
	for i, id := range ids {
		items[i] = RescheduleItem{ScheduleID: id, Rule: &newRule}
	}
	_, err := s.BatchReschedule(context.Background(), "alice", items)
	if err != nil {
		t.Fatal(err)
	}
	wg.Wait()

	// All concurrent creates must have succeeded (no loss).
	if concurrentCreated != 10 {
		t.Fatalf("concurrent creates lost: %d/10", concurrentCreated)
	}
	// Total schedules: 5 original + 10 concurrent = 15.
	all, err := s.List(context.Background(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 15 {
		t.Fatalf("expected 15 schedules, got %d", len(all))
	}
}

func TestBatchUnauthorizedItem(t *testing.T) {
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
	// Bob tries to batch-modify Alice's schedule.
	newRule := schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
		Weekdays: []time.Weekday{time.Friday}, Hour: 10,
	}}
	res, err := s.BatchReschedule(context.Background(), "bob", []RescheduleItem{
		{ScheduleID: sch.ID, Rule: &newRule},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Results[0].Action != "error" {
		t.Fatalf("expected error for unauthorized item, got %s", res.Results[0].Action)
	}
	if res.Results[0].Error == "" {
		t.Fatal("expected error message")
	}
	// Alice's schedule unchanged.
	got, _ := s.Get(context.Background(), "alice", sch.ID)
	if got.Rule.Weekly.Weekdays[0] != time.Tuesday {
		t.Fatal("schedule was modified by unauthorized batch")
	}
}

func TestBatchAlignment(t *testing.T) {
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
	eff := time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC)
	newRule := schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
		Weekdays: []time.Weekday{time.Friday}, Hour: 10,
	}}
	res, err := s.BatchReschedule(context.Background(), "alice", []RescheduleItem{
		{ScheduleID: sch.ID, Rule: &newRule, EffectiveAt: &eff},
	})
	if err != nil {
		t.Fatal(err)
	}
	report := res.Results[0].Report
	if report == nil {
		t.Fatal("nil report")
	}
	// Alignment: kept + removed + added should cover the trigger space.
	// Kept = history (before eff), removed = future old triggers, added = new future.
	if len(report.KeptSeqs) == 0 {
		t.Fatal("no kept triggers")
	}
	if len(report.RemovedSeqs) == 0 {
		t.Fatal("no removed triggers")
	}
	if len(report.Added) == 0 {
		t.Fatal("no added triggers")
	}
	// All added triggers >= eff and are Fridays.
	for _, o := range report.Added {
		if o.ScheduledAt.Before(eff) {
			t.Fatalf("added before effective: %s", o.ScheduledAt.UTC())
		}
		if o.ScheduledAt.Weekday() != time.Friday {
			t.Fatalf("added not Friday: %s", o.ScheduledAt.UTC())
		}
	}
	// No seq appears in both kept and removed.
	kept := map[int64]bool{}
	for _, seq := range report.KeptSeqs {
		kept[seq] = true
	}
	for _, seq := range report.RemovedSeqs {
		if kept[seq] {
			t.Fatalf("seq %d both kept and removed", seq)
		}
	}
	_ = fmt.Sprintf
}

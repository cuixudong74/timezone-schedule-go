package tz_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/scheduler"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz/tztest"
)

// writeZone writes a crafted zone file and VERSION.
func writeZone(t *testing.T, dir, version string, transitions []tztest.Transition) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "Test"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := tztest.Spec{
		Name:          "Test/Zone",
		InitialOffset: -5 * 3600,
		InitialAbbr:   "EST",
		Transitions:   transitions,
	}
	if err := os.WriteFile(filepath.Join(dir, "Test", "Zone"), tztest.Encode(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(version), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nyLikeTransitions(fallDate time.Time) []tztest.Transition {
	return []tztest.Transition{
		{Time: time.Date(2024, 3, 10, 7, 0, 0, 0, time.UTC), Offset: -4 * 3600, IsDST: true, Abbr: "EDT"},
		{Time: fallDate, Offset: -5 * 3600, IsDST: false, Abbr: "EST"},
	}
}

func TestRulesUpgradeHistoryStable(t *testing.T) {
	dir := t.TempDir()
	// v1: fall back on 2024-11-03.
	writeZone(t, dir, "2024a", nyLikeTransitions(time.Date(2024, 11, 3, 6, 0, 0, 0, time.UTC)))

	p, err := tz.NewReloadableProvider(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Version() != "2024a" {
		t.Fatalf("version: %s", p.Version())
	}

	st := store.NewMemStore()
	engine := schedule.NewEngine(p, tz.DefaultPolicy(), schedule.MonthDaySkip, 400*24*time.Hour)
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	sched := scheduler.New(st, p, engine, clock.NewFake(now), scheduler.Config{Horizon: 400 * 24 * time.Hour})

	sch, _, err := sched.Create(context.Background(), scheduler.CreateInput{
		ActorID: "alice", Timezone: "Test/Zone",
		Rule: schedule.RuleEnvelope{Kind: schedule.KindWeekly, Weekly: &schedule.WeeklyRule{
			Weekdays: []time.Weekday{time.Sunday}, Hour: 1, Minute: 30,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	trigs, err := sched.Triggers(context.Background(), "alice", sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trigs) == 0 {
		t.Fatal("no triggers")
	}
	// Record history.
	historyUTC := make([]time.Time, len(trigs))
	historyVer := make([]string, len(trigs))
	for i, tr := range trigs {
		historyUTC[i] = tr.ScheduledAt
		historyVer[i] = tr.RulesVersion
	}

	// v2: move fall back to 2024-11-10 (one week later).
	writeZone(t, dir, "2024b", nyLikeTransitions(time.Date(2024, 11, 10, 6, 0, 0, 0, time.UTC)))
	changed, err := p.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected version change")
	}
	if p.Version() != "2024b" {
		t.Fatalf("version after reload: %s", p.Version())
	}

	// History must be unchanged.
	trigs2, err := sched.Triggers(context.Background(), "alice", sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(trigs2) != len(trigs) {
		t.Fatalf("trigger count changed: %d -> %d", len(trigs), len(trigs2))
	}
	for i, tr := range trigs2 {
		if !tr.ScheduledAt.Equal(historyUTC[i]) {
			t.Fatalf("history trigger %d changed: %s -> %s", i, historyUTC[i], tr.ScheduledAt)
		}
		if tr.RulesVersion != "2024a" {
			t.Fatalf("history rules version changed: %s", tr.RulesVersion)
		}
	}

	// New computation uses new rules: generate fresh triggers with v2.
	loc, err := p.Resolve("Test/Zone")
	if err != nil {
		t.Fatal(err)
	}
	newOccs, err := engine.Generate(sch.Rule, loc, now, now.Add(400*24*time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	// The ambiguous Sunday should now be 2024-11-10 (not 2024-11-03).
	found := false
	for _, o := range newOccs {
		if o.Ambiguous && o.ScheduledAt.Month() == time.November {
			if o.ScheduledAt.Day() != 10 {
				t.Fatalf("ambiguous Sunday under v2: day %d, want 10", o.ScheduledAt.Day())
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no ambiguous November trigger under v2")
	}
}

package schedule

import (
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

func newTestEngine(t *testing.T, pol tz.DSTPolicy) *Engine {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	_ = loc
	return NewEngine(tz.NewEmbeddedProvider("test"), pol, MonthDaySkip, 400*24*time.Hour)
}

func TestWeeklyAcrossDST(t *testing.T) {
	e := newTestEngine(t, tz.DefaultPolicy())
	rule := RuleEnvelope{Kind: KindWeekly, Weekly: &WeeklyRule{
		Weekdays: []time.Weekday{time.Tuesday}, Hour: 10, Minute: 0,
	}}
	loc, _ := time.LoadLocation("America/New_York")
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	occs, err := e.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(occs) != 53 {
		t.Fatalf("expected 53 Tuesdays in leap year 2024, got %d", len(occs))
	}
	// July 2 2024 10:00 NY -> 14:00 UTC (EDT)
	first := occs[0]
	if first.ScheduledAt.UTC().Format("2006-01-02 15:04") != "2024-01-02 15:00" {
		t.Fatalf("first: %s", first.ScheduledAt.UTC())
	}
	// Find July occurrence
	var jul *Occurrence
	for i := range occs {
		if occs[i].ScheduledAt.Month() == time.July {
			jul = &occs[i]
			break
		}
	}
	if jul == nil || jul.ScheduledAt.UTC().Format("15:04") != "14:00" {
		t.Fatalf("jul: %v", jul)
	}
	// Seqs must be contiguous
	for i, o := range occs {
		if o.Seq != int64(i+1) {
			t.Fatalf("seq gap at %d: %d", i, o.Seq)
		}
	}
}

func TestWeeklyAmbiguous(t *testing.T) {
	e := newTestEngine(t, tz.DSTPolicy{Repeated: tz.RepeatedFirst, Missing: tz.MissingSkip})
	rule := RuleEnvelope{Kind: KindWeekly, Weekly: &WeeklyRule{
		Weekdays: []time.Weekday{time.Sunday}, Hour: 1, Minute: 30,
	}}
	loc, _ := time.LoadLocation("America/New_York")
	from := time.Date(2024, 10, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 12, 1, 0, 0, 0, 0, time.UTC)
	occs, err := e.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Find the Nov 3 occurrence
	var nov3 *Occurrence
	for i := range occs {
		if occs[i].ScheduledAt.Month() == time.November && occs[i].ScheduledAt.Day() == 3 {
			nov3 = &occs[i]
		}
	}
	if nov3 == nil {
		t.Fatal("missing Nov 3 occurrence")
	}
	if !nov3.Ambiguous {
		t.Fatal("expected ambiguous flag")
	}
	if nov3.ScheduledAt.UTC().Format("15:04") != "05:30" {
		t.Fatalf("first occurrence: %s", nov3.ScheduledAt.UTC())
	}
}

func TestWeeklyMissingSkipped(t *testing.T) {
	e := newTestEngine(t, tz.DSTPolicy{Repeated: tz.RepeatedFirst, Missing: tz.MissingSkip})
	rule := RuleEnvelope{Kind: KindWeekly, Weekly: &WeeklyRule{
		Weekdays: []time.Weekday{time.Sunday}, Hour: 2, Minute: 30,
	}}
	loc, _ := time.LoadLocation("America/New_York")
	from := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	occs, err := e.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	// March 10 02:30 is missing (spring forward); it must be skipped.
	for _, o := range occs {
		if o.ScheduledAt.Month() == time.March && o.ScheduledAt.Day() == 10 {
			t.Fatalf("March 10 02:30 should be skipped, got %s", o.ScheduledAt.UTC())
		}
	}
	if len(occs) == 0 {
		t.Fatal("expected other March Sundays to fire")
	}
}

func TestMonthlyDay31(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	rule := RuleEnvelope{Kind: KindMonthly, Monthly: &MonthlyRule{
		MonthDays: []int{31}, Hour: 9, Minute: 0,
	}}
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	eSkip := NewEngine(tz.NewEmbeddedProvider("test"), tz.DefaultPolicy(), MonthDaySkip, 400*24*time.Hour)
	skip, err := eSkip.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Months with 31 days: Jan, Mar, May, Jul, Aug, Oct, Dec = 7
	if len(skip) != 7 {
		t.Fatalf("skip: expected 7, got %d", len(skip))
	}

	eClamp := NewEngine(tz.NewEmbeddedProvider("test"), tz.DefaultPolicy(), MonthDayClamp, 400*24*time.Hour)
	clamp, err := eClamp.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(clamp) != 12 {
		t.Fatalf("clamp: expected 12, got %d", len(clamp))
	}
	// Feb 2024 clamp -> 29th
	var feb *Occurrence
	for i := range clamp {
		if clamp[i].ScheduledAt.Month() == time.February {
			feb = &clamp[i]
		}
	}
	if feb == nil || feb.ScheduledAt.Day() != 29 {
		t.Fatalf("feb clamp: %v", feb)
	}
}

func TestDeterminism(t *testing.T) {
	e := newTestEngine(t, tz.DefaultPolicy())
	rule := RuleEnvelope{Kind: KindWeekly, Weekly: &WeeklyRule{
		Weekdays: []time.Weekday{time.Monday, time.Wednesday, time.Friday}, Hour: 8, Minute: 15,
	}}
	loc, _ := time.LoadLocation("America/New_York")
	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	a, err := e.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.Generate(rule, loc, from, to, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != len(b) {
		t.Fatal("length mismatch")
	}
	for i := range a {
		if a[i].ScheduledAt != b[i].ScheduledAt || a[i].Seq != b[i].Seq {
			t.Fatalf("mismatch at %d", i)
		}
	}
}

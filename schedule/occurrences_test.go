package schedule

import (
	"os"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

func weeklyAt(loc *time.Location, wd time.Weekday, h, m int, pol Policy) Schedule {
	return Schedule{
		ID:      "s1",
		Version: 1,
		Rule: Rule{
			Freq:     Weekly,
			Location: loc,
			Weekdays: []time.Weekday{wd},
			Hour:     h, Minute: m,
		},
		Policy: pol,
	}
}

// 美国东部 2021-03-14 02:00 拨快到 03:00，02:30 不存在。
func TestGapShiftForward(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	s := weeklyAt(loc, time.Sunday, 2, 30, DefaultPolicy())
	after := time.Date(2021, 3, 13, 0, 0, 0, 0, time.UTC)
	limit := after.Add(48 * time.Hour)
	occs := Next(s, after, limit, 0)
	if len(occs) != 1 {
		t.Fatalf("want 1 occurrence, got %d", len(occs))
	}
	o := occs[0]
	if o.Skipped {
		t.Fatalf("should not be skipped: %v", o.Reason)
	}
	// 顺延到间隙后第一个有效本地时间 03:00 EDT = 07:00 UTC。
	want := time.Date(2021, 3, 14, 7, 0, 0, 0, time.UTC)
	if !o.UTC.Equal(want) {
		t.Fatalf("got %v, want %v (reason: %s)", o.UTC, want, o.Reason)
	}
	t.Logf("reason: %s", o.Reason)
}

func TestGapSkip(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	pol := Policy{Gap: GapSkip, Overlap: OverlapFirst}
	s := weeklyAt(loc, time.Sunday, 2, 30, pol)
	after := time.Date(2021, 3, 13, 0, 0, 0, 0, time.UTC)
	occs := Next(s, after, after.Add(48*time.Hour), 0)
	if len(occs) != 1 || !occs[0].Skipped {
		t.Fatalf("want 1 skipped occurrence, got %+v", occs)
	}
	t.Logf("reason: %s", occs[0].Reason)
}

// 2021-11-07 01:30 在美东出现两次（EDT 与 EST），必须至多触发一次。
func TestOverlapFirstAndLast(t *testing.T) {
	loc := mustLoc(t, "America/New_York")
	after := time.Date(2021, 11, 6, 0, 0, 0, 0, time.UTC)
	limit := after.Add(48 * time.Hour)

	first := weeklyAt(loc, time.Sunday, 1, 30, Policy{Gap: GapShiftForward, Overlap: OverlapFirst})
	occs := Next(first, after, limit, 0)
	if len(occs) != 1 {
		t.Fatalf("want exactly 1 occurrence, got %d", len(occs))
	}
	// 第一次出现：01:30 EDT = 05:30 UTC。
	if want := time.Date(2021, 11, 7, 5, 30, 0, 0, time.UTC); !occs[0].UTC.Equal(want) {
		t.Fatalf("first: got %v want %v", occs[0].UTC, want)
	}

	last := weeklyAt(loc, time.Sunday, 1, 30, Policy{Gap: GapShiftForward, Overlap: OverlapLast})
	occs = Next(last, after, limit, 0)
	if len(occs) != 1 {
		t.Fatalf("want exactly 1 occurrence, got %d", len(occs))
	}
	// 第二次出现：01:30 EST = 06:30 UTC。
	if want := time.Date(2021, 11, 7, 6, 30, 0, 0, time.UTC); !occs[0].UTC.Equal(want) {
		t.Fatalf("last: got %v want %v", occs[0].UTC, want)
	}
}

// 同一日程在不同默认时区下结果必须一致。
func TestDeterministicAcrossLocalZones(t *testing.T) {
	loc := mustLoc(t, "Asia/Shanghai")
	s := weeklyAt(loc, time.Monday, 9, 0, DefaultPolicy())
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limit := after.Add(90 * 24 * time.Hour)

	var baseline []Occurrence
	for _, tz := range []string{"UTC", "America/Los_Angeles", "Pacific/Auckland"} {
		l, err := time.LoadLocation(tz)
		if err != nil {
			t.Fatal(err)
		}
		time.Local = l
		got := Next(s, after, limit, 0)
		if baseline == nil {
			baseline = got
			continue
		}
		if len(got) != len(baseline) {
			t.Fatalf("tz %s: len %d != %d", tz, len(got), len(baseline))
		}
		for i := range got {
			if !got[i].UTC.Equal(baseline[i].UTC) {
				t.Fatalf("tz %s: occurrence %d differs: %v vs %v", tz, i, got[i].UTC, baseline[i].UTC)
			}
		}
	}
	time.Local = time.UTC
	if len(baseline) == 0 {
		t.Fatal("no occurrences")
	}
}

func TestMonthlySkipsMissingDay(t *testing.T) {
	s := Schedule{
		ID: "m1", Version: 1,
		Rule:   Rule{Freq: Monthly, Location: time.UTC, MonthDays: []int{31}, Hour: 10},
		Policy: DefaultPolicy(),
	}
	after := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	occs := Next(s, after, after.Add(120*24*time.Hour), 0)
	// 1/31、3/31 命中；2 月与 4 月无 31 日，跳过。
	if len(occs) != 2 {
		t.Fatalf("want 2, got %d: %+v", len(occs), occs)
	}
	if occs[0].UTC.Month() != time.January || occs[1].UTC.Month() != time.March {
		t.Fatalf("unexpected months: %+v", occs)
	}
}

func TestMain(m *testing.M) {
	time.Local = time.UTC
	os.Exit(m.Run())
}

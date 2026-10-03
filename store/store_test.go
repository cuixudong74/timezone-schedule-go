package store

import (
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/schedule"
)

func TestRoundTrip(t *testing.T) {
	path := t.TempDir() + "/log.jsonl"
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("America/New_York")
	s := schedule.Schedule{
		ID: "s1", OwnerID: "alice", Version: 1,
		Rule:   schedule.Rule{Freq: schedule.Weekly, Location: loc, Weekdays: []time.Weekday{time.Monday}, Hour: 9},
		Policy: schedule.DefaultPolicy(),
	}
	if _, err := st.Append(Event{Type: EventCreated, Schedule: s, TZDataVer: "2024a", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	occ := schedule.Occurrence{ScheduleID: "s1", Version: 1, UTC: time.Date(2026, 1, 5, 14, 0, 0, 0, time.UTC), Reason: "normal"}
	if _, err := st.Append(Event{Type: EventFired, Schedule: s, Occ: &occ, TZDataVer: "2024a", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// 模拟进程重启：重新打开并重放。
	st2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	evs := st2.Events()
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %d", len(evs))
	}
	if evs[0].Seq != 1 || evs[1].Seq != 2 {
		t.Fatalf("bad seq: %d %d", evs[0].Seq, evs[1].Seq)
	}
	got := evs[0].Schedule
	if got.Rule.Location.String() != "America/New_York" {
		t.Fatalf("location lost: %v", got.Rule.Location)
	}
	if got.Rule.Weekdays[0] != time.Monday || got.Rule.Hour != 9 {
		t.Fatalf("rule corrupted: %+v", got.Rule)
	}
	if evs[1].Occ == nil || !evs[1].Occ.UTC.Equal(occ.UTC) || evs[1].TZDataVer != "2024a" {
		t.Fatalf("fired event corrupted: %+v", evs[1])
	}
}

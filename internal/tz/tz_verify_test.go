package tz

import (
	"testing"
	"time"
)

func TestVerifyNY(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	// Normal summer/winter times
	summer := Resolve(WallTime{Year: 2024, Month: 7, Day: 2, Hour: 10}, loc, DefaultPolicy())
	if summer.Chosen.UTC().Format("15:04") != "14:00" {
		t.Fatalf("summer: got %s", summer.Chosen.UTC())
	}
	winter := Resolve(WallTime{Year: 2024, Month: 1, Day: 2, Hour: 10}, loc, DefaultPolicy())
	if winter.Chosen.UTC().Format("15:04") != "15:00" {
		t.Fatalf("winter: got %s", winter.Chosen.UTC())
	}

	// Ambiguous: 2024-11-03 01:30
	amb := Resolve(WallTime{Year: 2024, Month: 11, Day: 3, Hour: 1, Minute: 30}, loc, DefaultPolicy())
	if !amb.Ambiguous {
		t.Fatal("expected ambiguous")
	}
	if amb.Chosen.UTC().Format("15:04") != "05:30" {
		t.Fatalf("first: got %s", amb.Chosen.UTC())
	}
	ambLast := Resolve(WallTime{Year: 2024, Month: 11, Day: 3, Hour: 1, Minute: 30}, loc, DSTPolicy{Repeated: RepeatedLast})
	if ambLast.Chosen.UTC().Format("15:04") != "06:30" {
		t.Fatalf("last: got %s", ambLast.Chosen.UTC())
	}

	// Missing: 2024-03-10 02:30
	miss := Resolve(WallTime{Year: 2024, Month: 3, Day: 10, Hour: 2, Minute: 30}, loc, DefaultPolicy())
	if !miss.Skipped {
		t.Fatal("expected skipped")
	}
	missFwd := Resolve(WallTime{Year: 2024, Month: 3, Day: 10, Hour: 2, Minute: 30}, loc, DSTPolicy{Missing: MissingShiftForward})
	if missFwd.Chosen.UTC().Format("15:04") != "07:30" {
		t.Fatalf("shift-forward: got %s", missFwd.Chosen.UTC())
	}
	missBack := Resolve(WallTime{Year: 2024, Month: 3, Day: 10, Hour: 2, Minute: 30}, loc, DSTPolicy{Missing: MissingShiftBackward})
	if missBack.Chosen.UTC().Format("15:04") != "06:30" {
		t.Fatalf("shift-backward: got %s", missBack.Chosen.UTC())
	}
}

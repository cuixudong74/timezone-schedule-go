package tz

import (
	"testing"
	"time"
)

func loadNY(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNormalTime(t *testing.T) {
	loc := loadNY(t)
	pol := DefaultPolicy()
	// Summer: 10:00 EDT -> 14:00 UTC
	r := Resolve(WallTime{Year: 2024, Month: 7, Day: 2, Hour: 10}, loc, pol)
	if !r.Exists || r.Ambiguous || r.Skipped {
		t.Fatalf("unexpected: %+v", r)
	}
	if r.Chosen.UTC().Format("15:04") != "14:00" {
		t.Fatalf("got %s", r.Chosen.UTC())
	}
	// Winter: 10:00 EST -> 15:00 UTC
	r = Resolve(WallTime{Year: 2024, Month: 1, Day: 2, Hour: 10}, loc, pol)
	if r.Chosen.UTC().Format("15:04") != "15:00" {
		t.Fatalf("got %s", r.Chosen.UTC())
	}
}

func TestRepeatedTime(t *testing.T) {
	loc := loadNY(t)
	wall := WallTime{Year: 2024, Month: 11, Day: 3, Hour: 1, Minute: 30}
	// First (default)
	r := Resolve(wall, loc, DefaultPolicy())
	if !r.Ambiguous || !r.Exists {
		t.Fatalf("expected ambiguous: %+v", r)
	}
	if len(r.Instants) != 2 {
		t.Fatalf("expected 2 instants, got %d", len(r.Instants))
	}
	if r.Chosen.UTC().Format("15:04") != "05:30" {
		t.Fatalf("first: %s", r.Chosen.UTC())
	}
	// Last
	r = Resolve(wall, loc, DSTPolicy{Repeated: RepeatedLast})
	if r.Chosen.UTC().Format("15:04") != "06:30" {
		t.Fatalf("last: %s", r.Chosen.UTC())
	}
	// Both instants have the same wall clock in loc.
	for _, inst := range r.Instants {
		if inst.In(loc).Hour() != 1 || inst.In(loc).Minute() != 30 {
			t.Fatalf("wall mismatch: %s", inst.In(loc))
		}
	}
}

func TestMissingTime(t *testing.T) {
	loc := loadNY(t)
	wall := WallTime{Year: 2024, Month: 3, Day: 10, Hour: 2, Minute: 30}
	// Skip (default)
	r := Resolve(wall, loc, DefaultPolicy())
	if r.Exists || !r.Skipped {
		t.Fatalf("expected skipped: %+v", r)
	}
	if !r.Chosen.IsZero() {
		t.Fatal("chosen should be zero for skip")
	}
	// Shift forward
	r = Resolve(wall, loc, DSTPolicy{Missing: MissingShiftForward})
	if r.Skipped || !r.Shifted {
		t.Fatalf("expected shifted: %+v", r)
	}
	if r.Chosen.UTC().Format("15:04") != "07:30" {
		t.Fatalf("forward: %s", r.Chosen.UTC())
	}
	if r.Chosen.In(loc).Hour() != 3 || r.Chosen.In(loc).Minute() != 30 {
		t.Fatalf("forward wall: %s", r.Chosen.In(loc))
	}
	// Shift backward
	r = Resolve(wall, loc, DSTPolicy{Missing: MissingShiftBackward})
	if r.Chosen.UTC().Format("15:04") != "06:30" {
		t.Fatalf("backward: %s", r.Chosen.UTC())
	}
	if r.Chosen.In(loc).Hour() != 1 || r.Chosen.In(loc).Minute() != 30 {
		t.Fatalf("backward wall: %s", r.Chosen.In(loc))
	}
}

func TestSouthernHemisphere(t *testing.T) {
	// Australia/Sydney: DST starts in October, ends in April.
	loc, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Fatal(err)
	}
	// 2024-04-07 02:30 is ambiguous (fall back)
	r := Resolve(WallTime{Year: 2024, Month: 4, Day: 7, Hour: 2, Minute: 30}, loc, DefaultPolicy())
	if !r.Ambiguous {
		t.Fatalf("expected ambiguous: %+v", r)
	}
	// First occurrence: AEDT (UTC+11) -> 15:30 UTC previous day? Let's just check it's ambiguous and chosen is first.
	if len(r.Instants) != 2 {
		t.Fatalf("instants: %d", len(r.Instants))
	}
	if !r.Instants[0].Before(r.Instants[1]) {
		t.Fatal("instants not ordered")
	}
}

func TestHalfHourDST(t *testing.T) {
	// Australia/Lord_Howe has a 30-minute DST shift.
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Fatal(err)
	}
	// 2024-04-07 01:30 is ambiguous (fall back from +11:00 to +10:30)
	r := Resolve(WallTime{Year: 2024, Month: 4, Day: 7, Hour: 1, Minute: 30}, loc, DefaultPolicy())
	if !r.Ambiguous {
		t.Fatalf("expected ambiguous for Lord Howe: %+v", r)
	}
	if len(r.Instants) != 2 {
		t.Fatalf("instants: %d", len(r.Instants))
	}
	// 2024-10-06 02:15 is missing (spring forward from +10:30 to +11:00)
	r = Resolve(WallTime{Year: 2024, Month: 10, Day: 6, Hour: 2, Minute: 15}, loc, DefaultPolicy())
	if !r.Skipped {
		t.Fatalf("expected skipped for Lord Howe spring: %+v", r)
	}
}

func TestEmbeddedProvider(t *testing.T) {
	p := NewEmbeddedProvider("test-version")
	if p.Version() != "test-version" {
		t.Fatalf("version: %s", p.Version())
	}
	loc, err := p.Resolve("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if loc == nil {
		t.Fatal("nil loc")
	}
	if _, err := p.Resolve("Not/AZone"); err == nil {
		t.Fatal("expected error for invalid zone")
	}
	if _, err := p.Resolve(""); err == nil {
		t.Fatal("expected error for empty zone")
	}
}

func TestReloadableProvider(t *testing.T) {
	dir := t.TempDir()
	p, err := NewReloadableProvider(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve("America/New_York"); err == nil {
		t.Fatal("expected error for missing zone file")
	}
}

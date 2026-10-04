package tztest

import (
	"testing"
	"time"
)

func TestEncodeLoads(t *testing.T) {
	// Craft a zone with NY-like transitions for 2024.
	spec := Spec{
		Name:          "Test/Zone",
		InitialOffset: -5 * 3600,
		InitialAbbr:   "EST",
		Transitions: []Transition{
			{Time: time.Date(2024, 3, 10, 7, 0, 0, 0, time.UTC), Offset: -4 * 3600, IsDST: true, Abbr: "EDT"},
			{Time: time.Date(2024, 11, 3, 6, 0, 0, 0, time.UTC), Offset: -5 * 3600, IsDST: false, Abbr: "EST"},
		},
	}
	data := Encode(spec)
	loc, err := time.LoadLocationFromTZData("Test/Zone", data)
	if err != nil {
		t.Fatal(err)
	}
	// 2024-07-02 10:00 wall -> EDT (-4) -> 14:00 UTC
	summer := time.Date(2024, 7, 2, 10, 0, 0, 0, loc)
	if summer.UTC().Format("15:04") != "14:00" {
		t.Fatalf("summer: %s", summer.UTC())
	}
	// 2024-01-02 10:00 wall -> EST (-5) -> 15:00 UTC
	winter := time.Date(2024, 1, 2, 10, 0, 0, 0, loc)
	if winter.UTC().Format("15:04") != "15:00" {
		t.Fatalf("winter: %s", winter.UTC())
	}
	// Ambiguous: 2024-11-03 01:30 -> first occurrence 05:30 UTC (EDT)
	amb := time.Date(2024, 11, 3, 1, 30, 0, 0, loc)
	if amb.UTC().Format("15:04") != "05:30" {
		t.Fatalf("amb: %s", amb.UTC())
	}
}

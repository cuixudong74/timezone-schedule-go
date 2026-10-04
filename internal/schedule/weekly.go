package schedule

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// WeeklyRule fires on selected weekdays at a fixed local wall time.
type WeeklyRule struct {
	Weekdays      []time.Weekday `json:"weekdays"`
	Hour          int            `json:"hour"`
	Minute        int            `json:"minute"`
	Second        int            `json:"second"`
	IntervalWeeks int            `json:"interval_weeks"`
	Anchor        WallDate       `json:"anchor,omitempty"`
	Until         *WallDate      `json:"until,omitempty"`
}

// Kind returns KindWeekly.
func (r *WeeklyRule) Kind() RuleKind { return KindWeekly }

// Describe returns a human-readable description.
func (r *WeeklyRule) Describe() string {
	names := make([]string, len(r.Weekdays))
	for i, d := range r.Weekdays {
		names[i] = d.String()
	}
	return fmt.Sprintf("weekly on %s at %02d:%02d:%02d", strings.Join(names, ","), r.Hour, r.Minute, r.Second)
}

// Normalize fills defaults and sorts/dedups weekdays.
func (r *WeeklyRule) Normalize() {
	if r.IntervalWeeks <= 0 {
		r.IntervalWeeks = 1
	}
	seen := map[time.Weekday]bool{}
	var out []time.Weekday
	for _, d := range r.Weekdays {
		if d < time.Sunday || d > time.Saturday {
			continue
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	r.Weekdays = out
}

// Validate returns an error if the rule is malformed.
func (r *WeeklyRule) Validate() error {
	if len(r.Weekdays) == 0 {
		return fmt.Errorf("%w: weekly rule requires at least one weekday", ErrInvalidRecurrence)
	}
	if r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 || r.Second < 0 || r.Second > 59 {
		return fmt.Errorf("%w: invalid time %02d:%02d:%02d", ErrInvalidRecurrence, r.Hour, r.Minute, r.Second)
	}
	if r.IntervalWeeks < 1 {
		return fmt.Errorf("%w: interval must be >= 1", ErrInvalidRecurrence)
	}
	return nil
}

// NextAfter returns the earliest occurrence strictly after after.
func (r *WeeklyRule) NextAfter(after WallTime) (WallTime, bool) {
	interval := r.IntervalWeeks
	if interval <= 0 {
		interval = 1
	}
	anchor := r.Anchor
	if anchor.Year == 0 {
		anchor = wallTimeToDate(after)
	}

	// Start from the date of `after`; if the firing time today is already
	// <= after, begin searching from tomorrow.
	d := wallTimeToDate(after)
	candidate := WallTime{Year: d.Year, Month: d.Month, Day: d.Day, Hour: r.Hour, Minute: r.Minute, Second: r.Second}
	if !wallLess(after, candidate) {
		d = addDays(d, 1)
	}

	for i := 0; i < 200000; i++ {
		wd := wallDateToTime(d).Weekday()
		if containsWeekday(r.Weekdays, wd) {
			weeks := daysBetween(anchor, d) / 7
			if weeks >= 0 && weeks%interval == 0 {
				if r.Until != nil && compareDate(d, *r.Until) > 0 {
					return WallTime{}, false
				}
				out := WallTime{Year: d.Year, Month: d.Month, Day: d.Day, Hour: r.Hour, Minute: r.Minute, Second: r.Second}
				if wallLess(after, out) {
					return out, true
				}
			}
		}
		d = addDays(d, 1)
	}
	return WallTime{}, false
}

func containsWeekday(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func addDays(d WallDate, n int) WallDate {
	t := wallDateToTime(d).AddDate(0, 0, n)
	return wallDateFromTime(t)
}

func compareDate(a, b WallDate) int {
	if a.Year != b.Year {
		if a.Year < b.Year {
			return -1
		}
		return 1
	}
	if a.Month != b.Month {
		if a.Month < b.Month {
			return -1
		}
		return 1
	}
	if a.Day != b.Day {
		if a.Day < b.Day {
			return -1
		}
		return 1
	}
	return 0
}

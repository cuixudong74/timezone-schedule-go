package schedule

import (
	"fmt"
	"sort"
	"time"
)

// MonthlyRule fires on selected days of the month at a fixed local wall time.
// Positive days count from the start of the month (1 = first); negative days
// count from the end (-1 = last day).
type MonthlyRule struct {
	MonthDays      []int
	Hour           int
	Minute         int
	Second         int
	IntervalMonths int // 0 or 1 means every month
	Anchor         YearMonth
	Until          *YearMonth
	// OutOfRange controls what happens to days > month length.
	// The engine sets this from its configured policy.
	OutOfRange MonthDayPolicy
}

// Kind returns KindMonthly.
func (r *MonthlyRule) Kind() RuleKind { return KindMonthly }

// Describe returns a human-readable description.
func (r *MonthlyRule) Describe() string {
	return fmt.Sprintf("monthly on days %v at %02d:%02d:%02d", r.MonthDays, r.Hour, r.Minute, r.Second)
}

// Normalize fills defaults and sorts/dedups days.
func (r *MonthlyRule) Normalize() {
	if r.IntervalMonths <= 0 {
		r.IntervalMonths = 1
	}
	seen := map[int]bool{}
	var out []int
	for _, d := range r.MonthDays {
		if d == 0 || d < -31 || d > 31 {
			continue
		}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Ints(out)
	r.MonthDays = out
}

// Validate returns an error if the rule is malformed.
func (r *MonthlyRule) Validate() error {
	if len(r.MonthDays) == 0 {
		return fmt.Errorf("%w: monthly rule requires at least one day", ErrInvalidRecurrence)
	}
	if r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 || r.Second < 0 || r.Second > 59 {
		return fmt.Errorf("%w: invalid time %02d:%02d:%02d", ErrInvalidRecurrence, r.Hour, r.Minute, r.Second)
	}
	if r.IntervalMonths < 1 {
		return fmt.Errorf("%w: interval must be >= 1", ErrInvalidRecurrence)
	}
	return nil
}

// NextAfter returns the earliest occurrence strictly after after.
func (r *MonthlyRule) NextAfter(after WallTime) (WallTime, bool) {
	interval := r.IntervalMonths
	if interval <= 0 {
		interval = 1
	}
	anchor := r.Anchor
	if anchor.Year == 0 {
		anchor = YearMonth{Year: after.Year, Month: after.Month}
	}

	ym := YearMonth{Year: after.Year, Month: after.Month}
	for i := 0; i < 2400; i++ {
		if r.Until != nil && compareYearMonth(ym, *r.Until) > 0 {
			return WallTime{}, false
		}
		months := (ym.Year-anchor.Year)*12 + int(ym.Month-anchor.Month)
		if months >= 0 && months%interval == 0 {
			for _, day := range r.MonthDays {
				wallDay, ok := resolveMonthDay(day, ym.Year, ym.Month, r.OutOfRange)
				if !ok {
					continue
				}
				out := WallTime{Year: ym.Year, Month: ym.Month, Day: wallDay, Hour: r.Hour, Minute: r.Minute, Second: r.Second}
				if wallLess(after, out) {
					return out, true
				}
			}
		}
		ym = nextMonth(ym)
	}
	return WallTime{}, false
}

// resolveMonthDay maps a rule day (positive from start, negative from end)
// to an actual day in the given month.
func resolveMonthDay(day int, year int, month time.Month, oob MonthDayPolicy) (int, bool) {
	dim := daysInMonth(year, month)
	if day > 0 {
		if day <= dim {
			return day, true
		}
		if oob == MonthDayClamp {
			return dim, true
		}
		return 0, false
	}
	// negative: -1 = last day
	target := dim + day + 1
	if target >= 1 {
		return target, true
	}
	if oob == MonthDayClamp {
		return 1, true
	}
	return 0, false
}

func nextMonth(ym YearMonth) YearMonth {
	if ym.Month == time.December {
		return YearMonth{Year: ym.Year + 1, Month: time.January}
	}
	return YearMonth{Year: ym.Year, Month: ym.Month + 1}
}

func compareYearMonth(a, b YearMonth) int {
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
	return 0
}

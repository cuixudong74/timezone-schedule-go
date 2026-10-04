package schedule

import "time"

// wallDateToTime converts a naive wall date to a UTC time.Time (for date math).
func wallDateToTime(d WallDate) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

// wallTimeToDate extracts the date part of a WallTime.
func wallTimeToDate(w WallTime) WallDate {
	return WallDate{Year: w.Year, Month: w.Month, Day: w.Day}
}

// wallDateFromTime converts a time.Time to a WallDate (using its wall components).
func wallDateFromTime(t time.Time) WallDate {
	return WallDate{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// daysBetween returns the number of days from a to b (b - a) using naive UTC dates.
func daysBetween(a, b WallDate) int {
	return int(wallDateToTime(b).Sub(wallDateToTime(a)).Hours() / 24)
}

// wallLess reports whether a < b in wall coordinates.
func wallLess(a, b WallTime) bool {
	if a.Year != b.Year {
		return a.Year < b.Year
	}
	if a.Month != b.Month {
		return a.Month < b.Month
	}
	if a.Day != b.Day {
		return a.Day < b.Day
	}
	if a.Hour != b.Hour {
		return a.Hour < b.Hour
	}
	if a.Minute != b.Minute {
		return a.Minute < b.Minute
	}
	return a.Second < b.Second
}

// wallEqual reports whether a == b in wall coordinates.
func wallEqual(a, b WallTime) bool {
	return a.Year == b.Year && a.Month == b.Month && a.Day == b.Day &&
		a.Hour == b.Hour && a.Minute == b.Minute && a.Second == b.Second
}

// daysInMonth returns the number of days in the given year/month.
func daysInMonth(year int, month time.Month) int {
	return time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// validWallTime reports whether w is a well-formed wall date-time.
func validWallTime(w WallTime) bool {
	if w.Year < 1 || w.Year > 9999 {
		return false
	}
	if w.Month < 1 || w.Month > 12 {
		return false
	}
	if w.Day < 1 || w.Day > daysInMonth(w.Year, w.Month) {
		return false
	}
	if w.Hour < 0 || w.Hour > 23 || w.Minute < 0 || w.Minute > 59 || w.Second < 0 || w.Second > 59 {
		return false
	}
	return true
}

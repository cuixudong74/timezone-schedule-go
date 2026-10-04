// Package tz provides timezone resolution with explicit, configurable
// handling of DST transitions (repeated and missing local times).
package tz

import (
	"errors"
	"time"
)

// Errors distinguishable by callers.
var (
	// ErrInvalidTimezone is returned for unknown / malformed timezone identifiers.
	ErrInvalidTimezone = errors.New("tz: invalid timezone identifier")
)

// RepeatedPolicy selects which occurrence to use when a local time
// appears twice due to a fall-back DST transition.
type RepeatedPolicy int

const (
	// RepeatedFirst uses the first occurrence (earlier UTC instant).
	RepeatedFirst RepeatedPolicy = iota
	// RepeatedLast uses the second occurrence (later UTC instant).
	RepeatedLast
)

// MissingPolicy selects how to handle a local time that does not exist
// due to a spring-forward DST transition.
type MissingPolicy int

const (
	// MissingSkip skips the occurrence entirely.
	MissingSkip MissingPolicy = iota
	// MissingShiftForward shifts the trigger forward to the first existing
	// local time after the gap.
	MissingShiftForward
	// MissingShiftBackward shifts the trigger backward to the last existing
	// local time before the gap.
	MissingShiftBackward
)

// DSTPolicy bundles the configurable DST trade-offs.
type DSTPolicy struct {
	Repeated RepeatedPolicy
	Missing  MissingPolicy
}

// DefaultPolicy returns the documented default policy.
func DefaultPolicy() DSTPolicy {
	return DSTPolicy{Repeated: RepeatedFirst, Missing: MissingSkip}
}

// WallTime is a naive calendar date-time in some local timezone.
type WallTime struct {
	Year   int
	Month  int
	Day    int
	Hour   int
	Minute int
	Second int
}

// Resolution describes how a wall time was resolved to a UTC instant.
type Resolution struct {
	Wall      WallTime
	Exists    bool
	Ambiguous bool
	Skipped   bool
	Shifted   bool
	Instants  []time.Time // 1 element normally, 2 when ambiguous
	Chosen    time.Time   // zero if skipped
	Reason    string
}

// Provider resolves timezone identifiers and reports the rules version.
type Provider interface {
	Resolve(name string) (*time.Location, error)
	Version() string
}

// Resolve maps a wall time in loc to a UTC instant per the policy.
//
// The algorithm:
//  1. time.Date(wall, loc) resolves the wall time. If the resulting wall
//     components differ from the request, the local time does not exist
//     (spring-forward gap).
//  2. Otherwise the time exists. To detect a repeated local time we collect
//     candidate UTC offsets observed in the 72 hours after the resolved
//     instant and validate each candidate by round-tripping through loc.
//     Two distinct valid instants means the wall time is ambiguous.
//  3. The configured policy picks the instant (or skips).
func Resolve(wall WallTime, loc *time.Location, pol DSTPolicy) Resolution {
	res := Resolution{Wall: wall}

	// Go resolves the wall time; for missing times it returns an instant
	// whose wall components are shifted.
	t := time.Date(wall.Year, time.Month(wall.Month), wall.Day, wall.Hour, wall.Minute, wall.Second, 0, loc)
	if !wallMatches(t, wall) {
		// Missing local time (spring-forward gap).
		res.Exists = false
		switch pol.Missing {
		case MissingSkip:
			res.Skipped = true
			res.Reason = "local time does not exist (spring-forward gap); skipped per policy"
		case MissingShiftBackward:
			res.Shifted = true
			res.Chosen = t
			res.Instants = []time.Time{t}
			res.Reason = "local time does not exist (spring-forward gap); shifted backward to last existing instant"
		case MissingShiftForward:
			delta := firstOffsetChange(t, 72*time.Hour)
			if delta > 0 {
				shifted := t.Add(time.Duration(delta) * time.Second)
				res.Shifted = true
				res.Chosen = shifted
				res.Instants = []time.Time{shifted}
				res.Reason = "local time does not exist (spring-forward gap); shifted forward to first existing instant"
			} else {
				// Should not happen for a spring-forward gap; fall back to
				// backward shift and explain.
				res.Shifted = true
				res.Chosen = t
				res.Instants = []time.Time{t}
				res.Reason = "local time does not exist (spring-forward gap); could not determine forward shift, shifted backward"
			}
		}
		return res
	}

	// The wall time exists. Detect ambiguity: collect candidate offsets
	// observed in the next 72 hours and validate each by round-trip.
	offsets := map[int]bool{}
	_, off0 := t.Zone()
	offsets[off0] = true
	for h := 1; h <= 72; h++ {
		_, off := t.Add(time.Duration(h) * time.Hour).Zone()
		offsets[off] = true
	}

	seen := map[int64]bool{}
	for off := range offsets {
		cand := time.Date(wall.Year, time.Month(wall.Month), wall.Day, wall.Hour, wall.Minute, wall.Second, 0, time.FixedZone("candidate", off))
		inLoc := cand.In(loc)
		_, actualOff := inLoc.Zone()
		if actualOff != off || !wallMatches(inLoc, wall) {
			continue // not a valid resolution of this wall time
		}
		u := inLoc.Unix()
		if seen[u] {
			continue
		}
		seen[u] = true
		// Return the instant in loc so callers get a canonical Location.
		res.Instants = append(res.Instants, inLoc)
	}
	sortTimes(res.Instants)

	if len(res.Instants) > 1 {
		res.Ambiguous = true
		res.Exists = true
		switch pol.Repeated {
		case RepeatedFirst:
			res.Chosen = res.Instants[0]
			res.Reason = "local time is repeated (fall-back); first occurrence chosen per policy"
		case RepeatedLast:
			res.Chosen = res.Instants[len(res.Instants)-1]
			res.Reason = "local time is repeated (fall-back); last occurrence chosen per policy"
		}
		return res
	}

	// Normal, unambiguous time.
	res.Exists = true
	if len(res.Instants) == 1 {
		res.Chosen = res.Instants[0]
	} else {
		res.Chosen = t
	}
	res.Instants = []time.Time{res.Chosen}
	res.Reason = "normal local time"
	return res
}

// wallMatches reports whether t's wall-clock components equal wall.
func wallMatches(t time.Time, wall WallTime) bool {
	return t.Year() == wall.Year &&
		int(t.Month()) == wall.Month &&
		t.Day() == wall.Day &&
		t.Hour() == wall.Hour &&
		t.Minute() == wall.Minute &&
		t.Second() == wall.Second
}

// firstOffsetChange scans forward from t (inclusive of t's offset) in 1-hour
// steps and returns the first non-zero offset delta observed.
func firstOffsetChange(t time.Time, limit time.Duration) int {
	_, base := t.Zone()
	for d := time.Hour; d <= limit; d += time.Hour {
		_, off := t.Add(d).Zone()
		if off != base {
			return off - base
		}
	}
	return 0
}

// sortTimes sorts times in place by UTC instant.
func sortTimes(ts []time.Time) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j-1].After(ts[j]); j-- {
			ts[j-1], ts[j] = ts[j], ts[j-1]
		}
	}
}

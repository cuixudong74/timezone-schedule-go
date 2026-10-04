package schedule

import (
	"fmt"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

// Engine materializes trigger occurrences from recurrence rules.
type Engine struct {
	tzp      tz.Provider
	policy   tz.DSTPolicy
	monthDay MonthDayPolicy
	horizon  time.Duration
}

// NewEngine returns an Engine.
func NewEngine(tzp tz.Provider, policy tz.DSTPolicy, monthDay MonthDayPolicy, horizon time.Duration) *Engine {
	return &Engine{tzp: tzp, policy: policy, monthDay: monthDay, horizon: horizon}
}

// Horizon returns the materialization horizon.
func (e *Engine) Horizon() time.Duration { return e.horizon }

// RulesVersion returns the current timezone rules version label.
func (e *Engine) RulesVersion() string { return e.tzp.Version() }

// RecurrenceFromRule validates a rule envelope and returns its Recurrence.
func RecurrenceFromRule(rule RuleEnvelope) (Recurrence, error) {
	switch rule.Kind {
	case KindWeekly:
		if rule.Weekly == nil {
			return nil, fmt.Errorf("%w: weekly rule payload missing", ErrInvalidRecurrence)
		}
		r := *rule.Weekly
		r.Normalize()
		if err := r.Validate(); err != nil {
			return nil, err
		}
		return &r, nil
	case KindMonthly:
		if rule.Monthly == nil {
			return nil, fmt.Errorf("%w: monthly rule payload missing", ErrInvalidRecurrence)
		}
		r := *rule.Monthly
		r.Normalize()
		if err := r.Validate(); err != nil {
			return nil, err
		}
		return &r, nil
	default:
		return nil, fmt.Errorf("%w: unknown rule kind %q", ErrInvalidRecurrence, rule.Kind)
	}
}

// Generate computes occurrences in [fromUTC, toUTC) continuing seq numbering
// from startSeq (the next seq to assign). Skipped (non-existent) local times
// do not produce occurrences but still advance the recurrence.
func (e *Engine) Generate(rule RuleEnvelope, loc *time.Location, fromUTC, toUTC time.Time, startSeq int64) ([]Occurrence, error) {
	rec, err := RecurrenceFromRule(rule)
	if err != nil {
		return nil, err
	}
	if mr, ok := rec.(*MonthlyRule); ok {
		mr.OutOfRange = e.monthDay
	}
	if toUTC.Before(fromUTC) {
		return nil, fmt.Errorf("%w: to before from", ErrInvalidTimeInput)
	}

	fromWall := wallTimeFromTime(fromUTC.In(loc))
	ver := e.tzp.Version()
	var out []Occurrence
	seq := startSeq
	guard := 0
	for {
		guard++
		if guard > 200000 {
			break
		}
		next, ok := rec.NextAfter(fromWall)
		if !ok {
			break
		}
		if !validWallTime(next) {
			fromWall = next
			continue
		}
		res := tz.Resolve(tz.WallTime{
			Year: next.Year, Month: int(next.Month), Day: next.Day,
			Hour: next.Hour, Minute: next.Minute, Second: next.Second,
		}, loc, e.policy)
		// Advance the recurrence in wall coordinates regardless of outcome.
		fromWall = next

		if res.Skipped {
			continue
		}
		chosen := res.Chosen
		if chosen.IsZero() {
			continue
		}
		if !chosen.Before(toUTC) {
			break
		}
		if chosen.Before(fromUTC) {
			continue
		}
		out = append(out, Occurrence{
			Seq:          seq,
			ScheduledAt:  chosen,
			Wall:         next,
			Ambiguous:    res.Ambiguous,
			Skipped:      false,
			Shifted:      res.Shifted,
			Reason:       res.Reason,
			RulesVersion: ver,
		})
		seq++
	}
	return out, nil
}

// Materialize generates occurrences from now up to the horizon.
func (e *Engine) Materialize(rule RuleEnvelope, loc *time.Location, now time.Time, startSeq int64) ([]Occurrence, error) {
	return e.Generate(rule, loc, now, now.Add(e.horizon), startSeq)
}

// wallTimeFromTime extracts wall components from a time.Time.
func wallTimeFromTime(t time.Time) WallTime {
	return WallTime{
		Year: t.Year(), Month: t.Month(), Day: t.Day(),
		Hour: t.Hour(), Minute: t.Minute(), Second: t.Second(),
	}
}

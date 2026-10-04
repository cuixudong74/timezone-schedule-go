// Package schedule defines recurrence rules, schedule revisions and the
// trigger materialization engine.
package schedule

import (
	"errors"
	"time"
)

// Distinguishable errors.
var (
	ErrNotFound           = errors.New("schedule: not found")
	ErrUnauthorized       = errors.New("schedule: unauthorized")
	ErrInvalidRecurrence  = errors.New("schedule: invalid recurrence rule")
	ErrInvalidTimeInput   = errors.New("schedule: invalid time input")
	ErrConflict           = errors.New("schedule: revision conflict")
	ErrScheduleNotActive  = errors.New("schedule: schedule is not active")
)

// ScheduleStatus is the lifecycle state of a schedule.
type ScheduleStatus string

const (
	StatusActive    ScheduleStatus = "active"
	StatusCancelled ScheduleStatus = "cancelled"
)

// TriggerStatus is the delivery lifecycle state of a trigger.
type TriggerStatus string

const (
	TriggerPlanned    TriggerStatus = "planned"
	TriggerDelivering TriggerStatus = "delivering"
	TriggerDelivered  TriggerStatus = "delivered"
	TriggerFailed     TriggerStatus = "failed"
	TriggerCancelled  TriggerStatus = "cancelled"
)

// MonthDayPolicy controls what happens to "day 31" style rules in months
// that have fewer days.
type MonthDayPolicy int

const (
	// MonthDaySkip skips months that do not contain the day.
	MonthDaySkip MonthDayPolicy = iota
	// MonthDayClamp clamps to the last day of the month.
	MonthDayClamp
)

// WallTime is a naive calendar date-time in some local timezone.
type WallTime struct {
	Year   int
	Month  time.Month
	Day    int
	Hour   int
	Minute int
	Second int
}

// WallDate is a naive calendar date.
type WallDate struct {
	Year  int
	Month time.Month
	Day   int
}

// YearMonth is a naive year-month pair.
type YearMonth struct {
	Year  int
	Month time.Month
}

// RuleKind identifies the recurrence family.
type RuleKind string

const (
	KindWeekly  RuleKind = "weekly"
	KindMonthly RuleKind = "monthly"
)

// RuleEnvelope carries one concrete recurrence rule.
type RuleEnvelope struct {
	Kind    RuleKind
	Weekly  *WeeklyRule
	Monthly *MonthlyRule
}

// Recurrence generates wall-clock occurrences in local coordinates.
type Recurrence interface {
	// NextAfter returns the earliest occurrence strictly after after.
	NextAfter(after WallTime) (WallTime, bool)
	Kind() RuleKind
	Describe() string
}

// Schedule is a user-owned periodic schedule.
type Schedule struct {
	ID        string
	OwnerID   string
	Timezone  string
	Rule      RuleEnvelope
	Status    ScheduleStatus
	CreatedAt time.Time
	UpdatedAt time.Time
	Revision  int64
	NextSeq   int64
}

// Revision is an immutable record of a rule change.
type Revision struct {
	ID          int64
	ScheduleID  string
	Revision    int64
	Rule        RuleEnvelope
	EffectiveAt time.Time
	RulesVersion string
	CreatedAt   time.Time
	ActorID     string
	Reason      string
}

// Trigger is one materialized occurrence of a schedule.
type Trigger struct {
	ScheduleID   string
	Seq          int64
	ScheduledAt  time.Time
	Wall         WallTime
	Status       TriggerStatus
	Ambiguous    bool
	Skipped      bool
	Shifted      bool
	Resolution   string
	RulesVersion string
	CreatedAt    time.Time

	// Delivery state
	LeaseOwner  string
	LeaseUntil  time.Time
	Attempts    int
	LastError   string
	NextRetryAt time.Time
	DeliveredAt *time.Time
}

// Occurrence is a computed trigger candidate before persistence.
type Occurrence struct {
	Seq          int64
	ScheduledAt  time.Time
	Wall         WallTime
	Ambiguous    bool
	Skipped      bool
	Shifted      bool
	Reason       string
	RulesVersion string
}

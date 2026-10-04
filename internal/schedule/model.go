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
	Year   int         `json:"year"`
	Month  time.Month  `json:"month"`
	Day    int         `json:"day"`
	Hour   int         `json:"hour"`
	Minute int         `json:"minute"`
	Second int         `json:"second"`
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
	Kind    RuleKind    `json:"kind"`
	Weekly  *WeeklyRule `json:"weekly,omitempty"`
	Monthly *MonthlyRule `json:"monthly,omitempty"`
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
	ID        string         `json:"id"`
	OwnerID   string         `json:"owner_id"`
	Timezone  string         `json:"timezone"`
	Rule      RuleEnvelope   `json:"rule"`
	Status    ScheduleStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Revision  int64          `json:"revision"`
	NextSeq   int64          `json:"next_seq"`
}

// Revision is an immutable record of a rule change.
type Revision struct {
	ID           int64        `json:"id"`
	ScheduleID   string       `json:"schedule_id"`
	Revision     int64        `json:"revision"`
	Rule         RuleEnvelope `json:"rule"`
	EffectiveAt  time.Time    `json:"effective_at"`
	RulesVersion string       `json:"rules_version"`
	CreatedAt    time.Time    `json:"created_at"`
	ActorID      string       `json:"actor_id"`
	Reason       string       `json:"reason"`
}

// Trigger is one materialized occurrence of a schedule.
type Trigger struct {
	ScheduleID   string        `json:"schedule_id"`
	Seq          int64         `json:"seq"`
	ScheduledAt  time.Time     `json:"scheduled_at"`
	Wall         WallTime      `json:"wall"`
	Status       TriggerStatus `json:"status"`
	Ambiguous    bool          `json:"ambiguous"`
	Skipped      bool          `json:"skipped"`
	Shifted      bool          `json:"shifted"`
	Resolution   string        `json:"resolution"`
	RulesVersion string        `json:"rules_version"`
	CreatedAt    time.Time     `json:"created_at"`

	// Delivery state
	LeaseOwner  string     `json:"lease_owner,omitempty"`
	LeaseUntil  time.Time  `json:"lease_until,omitempty"`
	Attempts    int        `json:"attempts"`
	LastError   string     `json:"last_error,omitempty"`
	NextRetryAt time.Time  `json:"next_retry_at,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
}

// Occurrence is a computed trigger candidate before persistence.
type Occurrence struct {
	Seq          int64     `json:"seq"`
	ScheduledAt  time.Time `json:"scheduled_at"`
	Wall         WallTime  `json:"wall"`
	Ambiguous    bool      `json:"ambiguous"`
	Skipped      bool      `json:"skipped"`
	Shifted      bool      `json:"shifted"`
	Reason       string    `json:"reason"`
	RulesVersion string    `json:"rules_version"`
}

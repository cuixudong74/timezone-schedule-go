// Package store persists schedules, revisions, triggers and audit records.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
)

// Distinguishable errors.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

// AuditRecord is an append-only trace of a security-relevant operation.
type AuditRecord struct {
	ID        int64
	Timestamp time.Time
	ActorID   string
	Action    string
	TargetID  string
	Result    string // "ok" or "denied"
	Reason    string
	Detail    string
}

// Store is the persistence boundary.
type Store interface {
	Close() error

	// Schedules
	PutSchedule(ctx context.Context, s *schedule.Schedule) error
	GetSchedule(ctx context.Context, id string) (*schedule.Schedule, error)
	ListSchedules(ctx context.Context, ownerID string) ([]*schedule.Schedule, error)

	// Revisions
	PutRevision(ctx context.Context, r *schedule.Revision) error
	ListRevisions(ctx context.Context, scheduleID string) ([]*schedule.Revision, error)

	// Triggers
	PutTrigger(ctx context.Context, t *schedule.Trigger) error
	GetTrigger(ctx context.Context, scheduleID string, seq int64) (*schedule.Trigger, error)
	ListTriggers(ctx context.Context, scheduleID string) ([]*schedule.Trigger, error)
	DeletePlannedTriggers(ctx context.Context, scheduleID string, fromScheduledAt time.Time) (int, error)
	// ClaimDue atomically claims due triggers for delivery.
	ClaimDue(ctx context.Context, now time.Time, limit int, workerID string, leaseUntil time.Time) ([]*schedule.Trigger, error)
	// ReclaimExpired re-claims triggers whose lease expired.
	ReclaimExpired(ctx context.Context, now time.Time, workerID string, leaseUntil time.Time) (int, error)

	// Delivery records (idempotency)
	PutDelivery(ctx context.Context, scheduleID string, seq int64, deliveredAt time.Time, workerID string) error
	HasDelivery(ctx context.Context, scheduleID string, seq int64) (bool, error)
	ListDeliveries(ctx context.Context, scheduleID string) (map[int64]time.Time, error)

	// Audit
	PutAudit(ctx context.Context, a *AuditRecord) error
	ListAudit(ctx context.Context, limit int) ([]*AuditRecord, error)

	// Tx runs fn in a transaction (best-effort; implementations may serialize).
	Tx(ctx context.Context, fn func(Store) error) error
}

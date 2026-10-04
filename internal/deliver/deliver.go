// Package deliver runs the at-least-once delivery worker with idempotent
// outbox semantics and crash recovery.
package deliver

import (
	"context"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
)

// Sender delivers a reminder. Implementations must be idempotent with
// respect to the (scheduleID, seq) key: sending the same key twice must
// not produce two reminders.
type Sender interface {
	Send(ctx context.Context, scheduleID string, seq int64, scheduledAt time.Time, wall schedule.WallTime) error
}

// SenderFunc adapts a function to Sender.
type SenderFunc func(ctx context.Context, scheduleID string, seq int64, scheduledAt time.Time, wall schedule.WallTime) error

// Send calls f.
func (f SenderFunc) Send(ctx context.Context, scheduleID string, seq int64, scheduledAt time.Time, wall schedule.WallTime) error {
	return f(ctx, scheduleID, seq, scheduledAt, wall)
}

// Config tunes the delivery worker.
type Config struct {
	WorkerID     string
	PollInterval time.Duration
	Lease        time.Duration
	BatchLimit   int
	MaxAttempts  int
	RetryBackoff time.Duration
}

// DefaultConfig returns sensible defaults.
func DefaultConfig(workerID string) Config {
	return Config{
		WorkerID:     workerID,
		PollInterval: time.Second,
		Lease:        30 * time.Second,
		BatchLimit:   100,
		MaxAttempts:  5,
		RetryBackoff: 10 * time.Second,
	}
}

// Worker claims due triggers and delivers them.
type Worker struct {
	store  store.Store
	sender Sender
	clock  clock.Clock
	cfg    Config
}

// NewWorker returns a Worker.
func NewWorker(st store.Store, sender Sender, clk clock.Clock, cfg Config) *Worker {
	if cfg.BatchLimit <= 0 {
		cfg.BatchLimit = 100
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.Lease <= 0 {
		cfg.Lease = 30 * time.Second
	}
	if cfg.RetryBackoff <= 0 {
		cfg.RetryBackoff = 10 * time.Second
	}
	return &Worker{store: st, sender: sender, clock: clk, cfg: cfg}
}

// Run loops until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_, _ = w.RunOnce(ctx)
		}
	}
}

// RunOnce performs one reclaim-claim-deliver cycle (exposed for tests).
// It returns the number of triggers claimed.
func (w *Worker) RunOnce(ctx context.Context) (int, error) {
	now := w.clock.Now()
	// Reclaim triggers whose lease expired (crashed worker).
	if _, err := w.store.ReclaimExpired(ctx, now, w.cfg.WorkerID, now.Add(w.cfg.Lease)); err != nil {
		return 0, err
	}
	claimed, err := w.store.ClaimDue(ctx, now, w.cfg.BatchLimit, w.cfg.WorkerID, now.Add(w.cfg.Lease))
	if err != nil {
		return 0, err
	}
	for _, t := range claimed {
		w.deliverOne(ctx, t)
	}
	return len(claimed), nil
}

func (w *Worker) deliverOne(ctx context.Context, t *schedule.Trigger) {
	// Idempotency: if a delivery record already exists, just reconcile state.
	has, err := w.store.HasDelivery(ctx, t.ScheduleID, t.Seq)
	if err != nil {
		w.markFailed(ctx, t, err)
		return
	}
	if has {
		w.markDelivered(ctx, t)
		return
	}
	if err := w.sender.Send(ctx, t.ScheduleID, t.Seq, t.ScheduledAt, t.Wall); err != nil {
		w.markFailed(ctx, t, err)
		return
	}
	// Outbox record: idempotent insert.
	if err := w.store.PutDelivery(ctx, t.ScheduleID, t.Seq, w.clock.Now(), w.cfg.WorkerID); err != nil {
		w.markFailed(ctx, t, err)
		return
	}
	w.markDelivered(ctx, t)
}

func (w *Worker) markDelivered(ctx context.Context, t *schedule.Trigger) {
	t.Status = schedule.TriggerDelivered
	now := w.clock.Now()
	t.DeliveredAt = &now
	t.LeaseOwner = ""
	if err := w.store.PutTrigger(ctx, t); err != nil {
		// Best effort; next cycle reconciles via HasDelivery.
		_ = err
	}
}

func (w *Worker) markFailed(ctx context.Context, t *schedule.Trigger, cause error) {
	t.Attempts++
	t.LastError = cause.Error()
	t.LeaseOwner = ""
	if t.Attempts >= w.cfg.MaxAttempts {
		// Dead-letter: park far in the future so it is never reclaimed.
		t.Status = schedule.TriggerFailed
		t.NextRetryAt = w.clock.Now().Add(100 * 365 * 24 * time.Hour)
	} else {
		t.Status = schedule.TriggerFailed
		t.NextRetryAt = w.clock.Now().Add(w.cfg.RetryBackoff * time.Duration(t.Attempts))
	}
	if err := w.store.PutTrigger(ctx, t); err != nil {
		_ = err
	}
}

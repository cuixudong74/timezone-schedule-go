package scheduler

import (
	"context"
	"fmt"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"

	"github.com/cuixudong74/timezone-schedule-go/internal/store"
)

// audit appends an audit record. It never fails the calling operation;
// audit errors are swallowed to avoid breaking the mutation path.
func (s *Scheduler) audit(ctx context.Context, actorID, action, targetID, result, reason, detail string) {
	rec := &store.AuditRecord{
		Timestamp: s.now(nil),
		ActorID:   actorID,
		Action:    action,
		TargetID:  targetID,
		Result:    result,
		Reason:    reason,
		Detail:    detail,
	}
	_ = s.store.PutAudit(ctx, rec)
}

// Audit returns recent audit records.
func (s *Scheduler) Audit(ctx context.Context, actorID string, limit int) ([]*store.AuditRecord, error) {
	if actorID == "" {
		return nil, fmt.Errorf("%w: missing actor", schedule.ErrUnauthorized)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.ListAudit(ctx, limit)
}

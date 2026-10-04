package store

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
)

// MemStore is an in-memory Store for tests.
type MemStore struct {
	mu        sync.Mutex
	schedules map[string]*schedule.Schedule
	revisions map[string][]*schedule.Revision
	triggers  map[string]map[int64]*schedule.Trigger
	deliveries map[string]map[int64]time.Time
	audit     []*AuditRecord
	nextAudit int64
}

// NewMemStore returns an empty MemStore.
func NewMemStore() *MemStore {
	return &MemStore{
		schedules:  map[string]*schedule.Schedule{},
		revisions:  map[string][]*schedule.Revision{},
		triggers:   map[string]map[int64]*schedule.Trigger{},
		deliveries: map[string]map[int64]time.Time{},
	}
}

func (s *MemStore) Close() error { return nil }

func (s *MemStore) PutSchedule(ctx context.Context, sch *schedule.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *sch
	s.schedules[sch.ID] = &cp
	return nil
}

func (s *MemStore) GetSchedule(ctx context.Context, id string) (*schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sch, ok := s.schedules[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *sch
	return &cp, nil
}

func (s *MemStore) ListSchedules(ctx context.Context, ownerID string) ([]*schedule.Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*schedule.Schedule
	for _, sch := range s.schedules {
		if ownerID == "" || sch.OwnerID == ownerID {
			cp := *sch
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (s *MemStore) PutRevision(ctx context.Context, r *schedule.Revision) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *r
	s.revisions[r.ScheduleID] = append(s.revisions[r.ScheduleID], &cp)
	return nil
}

func (s *MemStore) ListRevisions(ctx context.Context, scheduleID string) ([]*schedule.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := s.revisions[scheduleID]
	out := make([]*schedule.Revision, len(src))
	for i, r := range src {
		cp := *r
		out[i] = &cp
	}
	return out, nil
}

func (s *MemStore) PutTrigger(ctx context.Context, t *schedule.Trigger) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.triggers[t.ScheduleID] == nil {
		s.triggers[t.ScheduleID] = map[int64]*schedule.Trigger{}
	}
	cp := *t
	s.triggers[t.ScheduleID][t.Seq] = &cp
	return nil
}

func (s *MemStore) GetTrigger(ctx context.Context, scheduleID string, seq int64) (*schedule.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.triggers[scheduleID]
	if m == nil {
		return nil, ErrNotFound
	}
	t, ok := m[seq]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *t
	return &cp, nil
}

func (s *MemStore) ListTriggers(ctx context.Context, scheduleID string) ([]*schedule.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.triggers[scheduleID]
	out := make([]*schedule.Trigger, 0, len(m))
	for _, t := range m {
		cp := *t
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (s *MemStore) DeletePlannedTriggers(ctx context.Context, scheduleID string, fromScheduledAt time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.triggers[scheduleID]
	n := 0
	for seq, t := range m {
		if t.ScheduledAt.Before(fromScheduledAt) {
			continue
		}
		if t.Status == schedule.TriggerPlanned || t.Status == schedule.TriggerFailed {
			delete(m, seq)
			n++
		}
	}
	return n, nil
}

func (s *MemStore) ClaimDue(ctx context.Context, now time.Time, limit int, workerID string, leaseUntil time.Time) ([]*schedule.Trigger, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var claimed []*schedule.Trigger
	for _, m := range s.triggers {
		for _, t := range m {
			if len(claimed) >= limit {
				break
			}
			if !isClaimable(t, now) {
				continue
			}
			t.Status = schedule.TriggerDelivering
			t.LeaseOwner = workerID
			t.LeaseUntil = leaseUntil
			cp := *t
			claimed = append(claimed, &cp)
		}
	}
	return claimed, nil
}

func (s *MemStore) ReclaimExpired(ctx context.Context, now time.Time, workerID string, leaseUntil time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, m := range s.triggers {
		for _, t := range m {
			if t.Status != schedule.TriggerDelivering || !t.LeaseUntil.Before(now) {
				continue
			}
			t.LeaseOwner = workerID
			t.LeaseUntil = leaseUntil
			n++
		}
	}
	return n, nil
}

func (s *MemStore) PutDelivery(ctx context.Context, scheduleID string, seq int64, deliveredAt time.Time, workerID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deliveries[scheduleID] == nil {
		s.deliveries[scheduleID] = map[int64]time.Time{}
	}
	if _, exists := s.deliveries[scheduleID][seq]; !exists {
		s.deliveries[scheduleID][seq] = deliveredAt
	}
	return nil
}

func (s *MemStore) HasDelivery(ctx context.Context, scheduleID string, seq int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.deliveries[scheduleID]
	_, ok := m[seq]
	return ok, nil
}

func (s *MemStore) ListDeliveries(ctx context.Context, scheduleID string) (map[int64]time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.deliveries[scheduleID]
	out := make(map[int64]time.Time, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out, nil
}

func (s *MemStore) PutAudit(ctx context.Context, a *AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *a
	s.nextAudit++
	cp.ID = s.nextAudit
	s.audit = append(s.audit, &cp)
	return nil
}

func (s *MemStore) ListAudit(ctx context.Context, limit int) ([]*AuditRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 || limit > len(s.audit) {
		limit = len(s.audit)
	}
	out := make([]*AuditRecord, 0, limit)
	for i := len(s.audit) - limit; i < len(s.audit); i++ {
		cp := *s.audit[i]
		out = append(out, &cp)
	}
	return out, nil
}

func (s *MemStore) Tx(ctx context.Context, fn func(Store) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fn(s)
}

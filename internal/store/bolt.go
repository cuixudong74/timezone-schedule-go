package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	bolt "go.etcd.io/bbolt"
)

// BoltStore is a bbolt-backed Store for persistent, restart-safe operation.
type BoltStore struct {
	db *bolt.DB
}

var (
	bSchedules  = []byte("schedules")
	bRevisions  = []byte("revisions")
	bTriggers   = []byte("triggers")
	bDeliveries = []byte("deliveries")
	bAudit      = []byte("audit")
	bMeta       = []byte("meta")
)

// OpenBolt opens (or creates) a BoltStore at path.
func OpenBolt(path string) (*BoltStore, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 5 * time.Second})
	if err != nil {
		return nil, err
	}
	s := &BoltStore{db: db}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bSchedules, bRevisions, bTriggers, bDeliveries, bAudit, bMeta} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *BoltStore) Close() error { return s.db.Close() }

func (s *BoltStore) PutSchedule(ctx context.Context, sch *schedule.Schedule) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bSchedules)
		data, err := json.Marshal(sch)
		if err != nil {
			return err
		}
		return b.Put([]byte(sch.ID), data)
	})
}

func (s *BoltStore) GetSchedule(ctx context.Context, id string) (*schedule.Schedule, error) {
	var out *schedule.Schedule
	err := s.db.View(func(tx *bolt.Tx) error {
		data := tx.Bucket(bSchedules).Get([]byte(id))
		if data == nil {
			return ErrNotFound
		}
		var sch schedule.Schedule
		if err := json.Unmarshal(data, &sch); err != nil {
			return err
		}
		out = &sch
		return nil
	})
	return out, err
}

func (s *BoltStore) ListSchedules(ctx context.Context, ownerID string) ([]*schedule.Schedule, error) {
	var out []*schedule.Schedule
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bSchedules).ForEach(func(k, v []byte) error {
			var sch schedule.Schedule
			if err := json.Unmarshal(v, &sch); err != nil {
				return err
			}
			if ownerID == "" || sch.OwnerID == ownerID {
				out = append(out, &sch)
			}
			return nil
		})
	})
	return out, err
}

func (s *BoltStore) PutRevision(ctx context.Context, r *schedule.Revision) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(bRevisions).CreateBucketIfNotExists([]byte(r.ScheduleID))
		if err != nil {
			return err
		}
		data, err := json.Marshal(r)
		if err != nil {
			return err
		}
		key := []byte(fmt.Sprintf("%020d", r.Revision))
		return b.Put(key, data)
	})
}

func (s *BoltStore) ListRevisions(ctx context.Context, scheduleID string) ([]*schedule.Revision, error) {
	var out []*schedule.Revision
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bRevisions).Bucket([]byte(scheduleID))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var r schedule.Revision
			if err := json.Unmarshal(v, &r); err != nil {
				return err
			}
			out = append(out, &r)
			return nil
		})
	})
	return out, err
}

func (s *BoltStore) PutTrigger(ctx context.Context, t *schedule.Trigger) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(bTriggers).CreateBucketIfNotExists([]byte(t.ScheduleID))
		if err != nil {
			return err
		}
		data, err := json.Marshal(t)
		if err != nil {
			return err
		}
		return b.Put([]byte(fmt.Sprintf("%020d", t.Seq)), data)
	})
}

func (s *BoltStore) GetTrigger(ctx context.Context, scheduleID string, seq int64) (*schedule.Trigger, error) {
	var out *schedule.Trigger
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bTriggers).Bucket([]byte(scheduleID))
		if b == nil {
			return ErrNotFound
		}
		data := b.Get([]byte(fmt.Sprintf("%020d", seq)))
		if data == nil {
			return ErrNotFound
		}
		var t schedule.Trigger
		if err := json.Unmarshal(data, &t); err != nil {
			return err
		}
		out = &t
		return nil
	})
	return out, err
}

func (s *BoltStore) ListTriggers(ctx context.Context, scheduleID string) ([]*schedule.Trigger, error) {
	var out []*schedule.Trigger
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bTriggers).Bucket([]byte(scheduleID))
		if b == nil {
			return nil
		}
		return b.ForEach(func(k, v []byte) error {
			var t schedule.Trigger
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			out = append(out, &t)
			return nil
		})
	})
	return out, err
}

func (s *BoltStore) DeletePlannedTriggers(ctx context.Context, scheduleID string, fromScheduledAt time.Time) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bTriggers).Bucket([]byte(scheduleID))
		if b == nil {
			return nil
		}
		var keys [][]byte
		var vals [][]byte
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var t schedule.Trigger
			if err := json.Unmarshal(v, &t); err != nil {
				return err
			}
			if !t.ScheduledAt.Before(fromScheduledAt) &&
				(t.Status == schedule.TriggerPlanned || t.Status == schedule.TriggerFailed) {
				keys = append(keys, append([]byte(nil), k...))
				vals = append(vals, v)
			}
		}
		_ = vals
		for _, k := range keys {
			if err := b.Delete(k); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func (s *BoltStore) ClaimDue(ctx context.Context, now time.Time, limit int, workerID string, leaseUntil time.Time) ([]*schedule.Trigger, error) {
	var claimed []*schedule.Trigger
	err := s.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bTriggers)
		return sb.ForEach(func(schedID, _ []byte) error {
			if len(claimed) >= limit {
				return nil
			}
			b := sb.Bucket(schedID)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				if len(claimed) >= limit {
					break
				}
				var t schedule.Trigger
				if err := json.Unmarshal(v, &t); err != nil {
					return err
				}
				if !isClaimable(&t, now) {
					continue
				}
				t.Status = schedule.TriggerDelivering
				t.LeaseOwner = workerID
				t.LeaseUntil = leaseUntil
				data, err := json.Marshal(&t)
				if err != nil {
					return err
				}
				if err := b.Put(k, data); err != nil {
					return err
				}
				claimed = append(claimed, &t)
			}
			return nil
		})
	})
	return claimed, err
}

func (s *BoltStore) ReclaimExpired(ctx context.Context, now time.Time, workerID string, leaseUntil time.Time) (int, error) {
	n := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		sb := tx.Bucket(bTriggers)
		return sb.ForEach(func(schedID, _ []byte) error {
			b := sb.Bucket(schedID)
			if b == nil {
				return nil
			}
			c := b.Cursor()
			for k, v := c.First(); k != nil; k, v = c.Next() {
				var t schedule.Trigger
				if err := json.Unmarshal(v, &t); err != nil {
					return err
				}
				if t.Status != schedule.TriggerDelivering || !t.LeaseUntil.Before(now) {
					continue
				}
				t.LeaseOwner = workerID
				t.LeaseUntil = leaseUntil
				data, err := json.Marshal(&t)
				if err != nil {
					return err
				}
				if err := b.Put(k, data); err != nil {
					return err
				}
				n++
			}
			return nil
		})
	})
	return n, err
}

func isClaimable(t *schedule.Trigger, now time.Time) bool {
	switch t.Status {
	case schedule.TriggerPlanned:
		return !t.ScheduledAt.After(now)
	case schedule.TriggerFailed:
		return t.NextRetryAt.IsZero() || !t.NextRetryAt.After(now)
	case schedule.TriggerDelivering:
		return !t.LeaseUntil.After(now)
	}
	return false
}

func (s *BoltStore) PutDelivery(ctx context.Context, scheduleID string, seq int64, deliveredAt time.Time, workerID string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(bDeliveries).CreateBucketIfNotExists([]byte(scheduleID))
		if err != nil {
			return err
		}
		key := []byte(fmt.Sprintf("%020d", seq))
		if b.Get(key) != nil {
			return nil // idempotent: already delivered
		}
		rec := map[string]any{"delivered_at": deliveredAt, "worker_id": workerID}
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		return b.Put(key, data)
	})
}

func (s *BoltStore) HasDelivery(ctx context.Context, scheduleID string, seq int64) (bool, error) {
	var ok bool
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bDeliveries).Bucket([]byte(scheduleID))
		if b == nil {
			return nil
		}
		ok = b.Get([]byte(fmt.Sprintf("%020d", seq))) != nil
		return nil
	})
	return ok, err
}

func (s *BoltStore) ListDeliveries(ctx context.Context, scheduleID string) (map[int64]time.Time, error) {
	var out map[int64]time.Time
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bDeliveries).Bucket([]byte(scheduleID))
		if b == nil {
			return nil
		}
		out = map[int64]time.Time{}
		return b.ForEach(func(k, v []byte) error {
			var rec struct {
				DeliveredAt time.Time `json:"delivered_at"`
			}
			if err := json.Unmarshal(v, &rec); err != nil {
				return err
			}
			var seq int64
			if _, err := fmt.Sscanf(string(k), "%d", &seq); err != nil {
				return err
			}
			out[seq] = rec.DeliveredAt
			return nil
		})
	})
	return out, err
}

func (s *BoltStore) PutAudit(ctx context.Context, a *AuditRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAudit)
		id, _ := b.NextSequence()
		a.ID = int64(id)
		data, err := json.Marshal(a)
		if err != nil {
			return err
		}
		return b.Put([]byte(fmt.Sprintf("%020d", a.ID)), data)
	})
}

func (s *BoltStore) ListAudit(ctx context.Context, limit int) ([]*AuditRecord, error) {
	var out []*AuditRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(bAudit)
		c := b.Cursor()
		var all []*AuditRecord
		for k, v := c.Last(); k != nil; k, v = c.Prev() {
			var a AuditRecord
			if err := json.Unmarshal(v, &a); err != nil {
				return err
			}
			all = append(all, &a)
		}
		if limit > 0 && len(all) > limit {
			all = all[:limit]
		}
		// reverse to chronological order
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
		out = all
		return nil
	})
	return out, err
}

func (s *BoltStore) Tx(ctx context.Context, fn func(Store) error) error {
	// bbolt transactions are not re-entrant; run fn against s.
	return fn(s)
}

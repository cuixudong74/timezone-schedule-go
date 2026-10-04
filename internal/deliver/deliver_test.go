package deliver

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
)

// recordingSender records deliveries and can fail the first N attempts.
type recordingSender struct {
	mu         sync.Mutex
	deliveries map[string]int // key -> count
	failTimes  map[string]int // key -> remaining failures
}

func newRecordingSender() *recordingSender {
	return &recordingSender{deliveries: map[string]int{}, failTimes: map[string]int{}}
}

func (s *recordingSender) key(scheduleID string, seq int64) string {
	return fmt.Sprintf("%s:%d", scheduleID, seq)
}

func (s *recordingSender) Send(ctx context.Context, scheduleID string, seq int64, scheduledAt time.Time, wall schedule.WallTime) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := s.key(scheduleID, seq)
	if s.failTimes[k] > 0 {
		s.failTimes[k]--
		return fmt.Errorf("simulated failure for %s", k)
	}
	s.deliveries[k]++
	return nil
}

func (s *recordingSender) count(scheduleID string, seq int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deliveries[s.key(scheduleID, seq)]
}

func (s *recordingSender) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.deliveries {
		n += c
	}
	return n
}

// seedTriggers creates n triggers starting at base, spaced 1 second apart.
func seedTriggers(t *testing.T, st store.Store, scheduleID string, base time.Time, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		tr := &schedule.Trigger{
			ScheduleID:  scheduleID,
			Seq:         int64(i + 1),
			ScheduledAt: base.Add(time.Duration(i) * time.Second),
			Wall:        schedule.WallTime{Year: 2024, Month: 6, Day: 1, Hour: 10, Minute: 0, Second: 0},
			Status:      schedule.TriggerPlanned,
			CreatedAt:   base,
		}
		if err := st.PutTrigger(context.Background(), tr); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeliverOnce(t *testing.T) {
	st := store.NewMemStore()
	now := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	seedTriggers(t, st, "sch1", now.Add(-time.Second), 5)

	sender := newRecordingSender()
	clk := clock.NewFake(now)
	w := NewWorker(st, sender, clk, Config{
		WorkerID: "w1", PollInterval: 10 * time.Millisecond, Lease: time.Second,
		BatchLimit: 100, MaxAttempts: 3, RetryBackoff: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// Advance clock past all triggers.
	clk.Advance(10 * time.Second)
	time.Sleep(200 * time.Millisecond)

	if sender.total() != 5 {
		t.Fatalf("expected 5 deliveries, got %d", sender.total())
	}
	for i := 1; i <= 5; i++ {
		if sender.count("sch1", int64(i)) != 1 {
			t.Fatalf("trigger %d delivered %d times", i, sender.count("sch1", int64(i)))
		}
	}
}

func TestRetryThenSucceed(t *testing.T) {
	st := store.NewMemStore()
	now := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	seedTriggers(t, st, "sch1", now.Add(-time.Second), 1)

	sender := newRecordingSender()
	sender.failTimes["sch1:1"] = 2 // fail twice, then succeed
	clk := clock.Real{}
	w := NewWorker(st, sender, clk, Config{
		WorkerID: "w1", PollInterval: 10 * time.Millisecond, Lease: time.Second,
		BatchLimit: 100, MaxAttempts: 5, RetryBackoff: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	time.Sleep(500 * time.Millisecond)

	if sender.count("sch1", 1) != 1 {
		t.Fatalf("expected exactly 1 delivery after retries, got %d", sender.count("sch1", 1))
	}
	tr, _ := st.GetTrigger(context.Background(), "sch1", 1)
	if tr.Status != schedule.TriggerDelivered {
		t.Fatalf("status: %s", tr.Status)
	}
	if tr.Attempts != 2 {
		t.Fatalf("attempts: %d", tr.Attempts)
	}
}

func TestRestartRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := dir + "/test.db"

	now := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	seed := func(st store.Store) {
		seedTriggers(t, st, "sch1", now.Add(-time.Second), 5)
	}

	// First "process": open store, seed, deliver some.
	st1, err := store.OpenBolt(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	seed(st1)
	sender1 := newRecordingSender()
	clk1 := clock.NewFake(now)
	w1 := NewWorker(st1, sender1, clk1, Config{
		WorkerID: "w1", PollInterval: 10 * time.Millisecond, Lease: 50 * time.Millisecond,
		BatchLimit: 100, MaxAttempts: 3, RetryBackoff: 10 * time.Millisecond,
	})
	ctx1, cancel1 := context.WithCancel(context.Background())
	go w1.Run(ctx1)
	clk1.Advance(10 * time.Second)
	time.Sleep(150 * time.Millisecond)
	cancel1()
	st1.Close()

	// Second "process": reopen store, new worker, same sender semantics.
	st2, err := store.OpenBolt(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	sender2 := newRecordingSender()
	clk2 := clock.NewFake(now.Add(10 * time.Second))
	w2 := NewWorker(st2, sender2, clk2, Config{
		WorkerID: "w2", PollInterval: 10 * time.Millisecond, Lease: 50 * time.Millisecond,
		BatchLimit: 100, MaxAttempts: 3, RetryBackoff: 10 * time.Millisecond,
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go w2.Run(ctx2)
	time.Sleep(200 * time.Millisecond)

	// Total deliveries across both processes must be exactly 5 (no dup, no miss).
	total := sender1.total() + sender2.total()
	if total != 5 {
		t.Fatalf("expected 5 total deliveries after restart, got %d (s1=%d s2=%d)", total, sender1.total(), sender2.total())
	}
	for i := 1; i <= 5; i++ {
		c := sender1.count("sch1", int64(i)) + sender2.count("sch1", int64(i))
		if c != 1 {
			t.Fatalf("trigger %d delivered %d times total", i, c)
		}
	}
}

func TestConcurrentWorkers(t *testing.T) {
	st := store.NewMemStore()
	now := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	seedTriggers(t, st, "sch1", now.Add(-time.Second), 20)

	sender := newRecordingSender()
	clk := clock.NewFake(now)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i := 0; i < 3; i++ {
		w := NewWorker(st, sender, clk, Config{
			WorkerID: fmt.Sprintf("w%d", i), PollInterval: 5 * time.Millisecond, Lease: 50 * time.Millisecond,
			BatchLimit: 100, MaxAttempts: 3, RetryBackoff: 10 * time.Millisecond,
		})
		go w.Run(ctx)
	}

	clk.Advance(25 * time.Second)
	time.Sleep(300 * time.Millisecond)

	if sender.total() != 20 {
		t.Fatalf("expected 20 deliveries, got %d", sender.total())
	}
	for i := 1; i <= 20; i++ {
		if sender.count("sch1", int64(i)) != 1 {
			t.Fatalf("trigger %d delivered %d times", i, sender.count("sch1", int64(i)))
		}
	}
}

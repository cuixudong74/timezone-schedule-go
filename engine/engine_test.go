package engine

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/schedule"
	"github.com/cuixudong74/timezone-schedule-go/store"
)

type harness struct {
	eng     *Engine
	st      *store.Store
	now     time.Time
	mu      sync.Mutex
	got     []schedule.Occurrence
	failIDs map[string]bool // 模拟投递失败
}

func newHarness(t *testing.T, path string, tzVer string, start time.Time) *harness {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{st: st, now: start, failIDs: map[string]bool{}}
	h.eng = New(st, tzVer, func(o schedule.Occurrence) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.failIDs[o.Key()] {
			return fmt.Errorf("simulated delivery failure")
		}
		h.got = append(h.got, o)
		return nil
	})
	h.eng.SetClock(func() time.Time { return h.now })
	return h
}

func (h *harness) delivered() []schedule.Occurrence {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]schedule.Occurrence(nil), h.got...)
}

func weeklySched(id, tz string, wd time.Weekday, hour int) schedule.Schedule {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		panic(err)
	}
	return schedule.Schedule{
		ID: id,
		Rule: schedule.Rule{
			Freq: schedule.Weekly, Location: loc,
			Weekdays: []time.Weekday{wd}, Hour: hour,
		},
		Policy: schedule.DefaultPolicy(),
	}
}

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // 周四

func TestCreateUpdateCancelAndAuthz(t *testing.T) {
	h := newHarness(t, t.TempDir()+"/log", "2024a", t0)
	id, err := h.eng.Create("alice", weeklySched("s1", "Asia/Shanghai", time.Monday, 9))
	if err != nil {
		t.Fatal(err)
	}
	// 越权修改/取消/查询必须被拒绝且原因可区分。
	if err := h.eng.Update("bob", id, weeklySched("s1", "UTC", time.Monday, 9).Rule, schedule.DefaultPolicy()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if err := h.eng.Cancel("bob", id); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if _, err := h.eng.Get("bob", id); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if err := h.eng.Cancel("alice", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	// 非法时区与非法时间输入。
	bad := weeklySched("bad", "Asia/Shanghai", time.Monday, 25)
	if _, err := h.eng.Create("alice", bad); !errors.Is(err, ErrInvalidTime) {
		t.Fatalf("want ErrInvalidTime, got %v", err)
	}
	noLoc := weeklySched("bad2", "Asia/Shanghai", time.Monday, 9)
	noLoc.Rule.Location = nil
	if _, err := h.eng.Create("alice", noLoc); !errors.Is(err, ErrInvalidTimezone) {
		t.Fatalf("want ErrInvalidTimezone, got %v", err)
	}
	noDays := weeklySched("bad3", "UTC", time.Monday, 9)
	noDays.Rule.Weekdays = nil
	if _, err := h.eng.Create("alice", noDays); !errors.Is(err, ErrInvalidRule) {
		t.Fatalf("want ErrInvalidRule, got %v", err)
	}
	// 取消后不可再修改。
	if err := h.eng.Cancel("alice", id); err != nil {
		t.Fatal(err)
	}
	if err := h.eng.Update("alice", id, weeklySched("s1", "UTC", time.Monday, 9).Rule, schedule.DefaultPolicy()); !errors.Is(err, ErrCancelled) {
		t.Fatalf("want ErrCancelled, got %v", err)
	}
	// 审计日志包含关键操作。
	ops := map[string]bool{}
	for _, a := range h.eng.Audit() {
		ops[a.Op] = true
	}
	if !ops["create"] || !ops["cancel"] {
		t.Fatalf("audit missing ops: %+v", h.eng.Audit())
	}
}

func TestUpdateBoundaryKeepsHistory(t *testing.T) {
	h := newHarness(t, t.TempDir()+"/log", "2024a", t0)
	// 每周一 09:00（UTC）。
	id, _ := h.eng.Create("alice", weeklySched("s1", "UTC", time.Monday, 9))
	// 推进到第一个周一（2026-01-05）触发之后。
	h.now = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	if n, _ := h.eng.DispatchDue(h.now); n != 1 {
		t.Fatalf("want 1 delivered, got %d", n)
	}
	first := h.delivered()[0]
	if first.Version != 1 || first.UTC.Hour() != 9 {
		t.Fatalf("unexpected first: %+v", first)
	}
	// 修改为每周一 10:00，生效边界为现在。
	if err := h.eng.Update("alice", id, weeklySched("s1", "UTC", time.Monday, 10).Rule, schedule.DefaultPolicy()); err != nil {
		t.Fatal(err)
	}
	// 推进两周：历史记录不得被改写，新触发按 10:00。
	h.now = h.now.Add(14 * 24 * time.Hour)
	if _, err := h.eng.DispatchDue(h.now); err != nil {
		t.Fatal(err)
	}
	fired := h.eng.Fired(id)
	if len(fired) != 3 {
		t.Fatalf("want 3 fired, got %d", len(fired))
	}
	if !fired[0].UTC.Equal(first.UTC) || fired[0].Version != 1 {
		t.Fatalf("history rewritten: %+v", fired[0])
	}
	for _, f := range fired[1:] {
		if f.Version != 2 || f.UTC.Hour() != 10 {
			t.Fatalf("new rule not applied: %+v", f)
		}
	}
}

func TestIdempotentConcurrentDispatch(t *testing.T) {
	h := newHarness(t, t.TempDir()+"/log", "2024a", t0)
	h.eng.Create("alice", weeklySched("s1", "UTC", time.Monday, 9))
	h.eng.Create("alice", weeklySched("s2", "Asia/Shanghai", time.Monday, 9))
	h.now = time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC) // 覆盖第一个周一
	// 并发重复调度。
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.eng.DispatchDue(h.now)
		}()
	}
	wg.Wait()
	// 再重复调度与重试。
	h.eng.DispatchDue(h.now)
	h.eng.DispatchDue(h.now)
	got := h.delivered()
	if len(got) != 2 {
		t.Fatalf("want exactly 2 deliveries, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, o := range got {
		if seen[o.Key()] {
			t.Fatalf("duplicate delivery: %s", o.Key())
		}
		seen[o.Key()] = true
	}
}

func TestDeliveryRetryOnFailure(t *testing.T) {
	h := newHarness(t, t.TempDir()+"/log", "2024a", t0)
	h.eng.Create("alice", weeklySched("s1", "UTC", time.Monday, 9))
	h.now = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	// 先算出门槛键并让其失败一次。
	key := h.deliveredKey(idOf(h, "s1"))
	h.failIDs[key] = true
	if n, _ := h.eng.DispatchDue(h.now); n != 0 {
		t.Fatalf("want 0 delivered on failure, got %d", n)
	}
	delete(h.failIDs, key)
	if n, _ := h.eng.DispatchDue(h.now); n != 1 {
		t.Fatalf("want 1 delivered on retry, got %d", n)
	}
	if n, _ := h.eng.DispatchDue(h.now); n != 0 {
		t.Fatalf("want idempotent after success, got %d", n)
	}
}

func idOf(h *harness, id string) string { return id }

func (h *harness) deliveredKey(id string) string {
	// 2026-01-05 周一 09:00 UTC。
	return id + "@" + time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
}

func TestRestartRecovery(t *testing.T) {
	path := t.TempDir() + "/log"
	h := newHarness(t, path, "2024a", t0)
	h.eng.Create("alice", weeklySched("s1", "UTC", time.Monday, 9))
	h.now = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	h.eng.DispatchDue(h.now)
	if len(h.delivered()) != 1 {
		t.Fatal("setup delivery failed")
	}
	h.st.Close()

	// 模拟进程重启：两周后才恢复，期间漏掉的触发要补上，已投递的不得重复。
	h2 := newHarness(t, path, "2024a", time.Date(2026, 1, 20, 0, 0, 0, 0, time.UTC))
	defer h2.st.Close()
	n, err := h2.eng.DispatchDue(h2.now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // 1/12 与 1/19 两个周一
		t.Fatalf("want 2 recovered deliveries, got %d", n)
	}
	fired := h2.eng.Fired("s1")
	if len(fired) != 3 {
		t.Fatalf("want 3 total fired, got %d", len(fired))
	}
	keys := map[string]bool{}
	for _, f := range fired {
		if keys[f.Key()] {
			t.Fatalf("duplicate after restart: %s", f.Key())
		}
		keys[f.Key()] = true
	}
}

func TestTZDataUpgradeKeepsHistory(t *testing.T) {
	path := t.TempDir() + "/log"
	h := newHarness(t, path, "2024a", t0)
	h.eng.Create("alice", weeklySched("s1", "UTC", time.Monday, 9))
	h.now = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	h.eng.DispatchDue(h.now)
	before := h.eng.Fired("s1")

	// 时区数据运行期升级。
	h.eng.SetTZDataVersion("2026b")
	h.now = h.now.Add(7 * 24 * time.Hour)
	h.eng.DispatchDue(h.now)
	after := h.eng.Fired("s1")
	if len(after) != 2 {
		t.Fatalf("want 2 fired, got %d", len(after))
	}
	if !after[0].UTC.Equal(before[0].UTC) || after[0].Version != before[0].Version {
		t.Fatalf("history changed after tzdata upgrade: %+v vs %+v", after[0], before[0])
	}
	// 事件日志中的历史记录仍标记旧版本时区数据。
	var firedEvents []store.Event
	for _, ev := range h.st.Events() {
		if ev.Type == store.EventFired {
			firedEvents = append(firedEvents, ev)
		}
	}
	if firedEvents[0].TZDataVer != "2024a" || firedEvents[1].TZDataVer != "2026b" {
		t.Fatalf("tz versions not stamped: %+v", firedEvents)
	}
	h.st.Close()

	// 重启后历史依然稳定。
	h2 := newHarness(t, path, "2026b", h.now)
	defer h2.st.Close()
	fired := h2.eng.Fired("s1")
	if len(fired) != 2 || !fired[0].UTC.Equal(before[0].UTC) {
		t.Fatalf("history not stable across restart: %+v", fired)
	}
}

func TestReplanWithConcurrentChanges(t *testing.T) {
	h := newHarness(t, t.TempDir()+"/log", "2024a", t0)
	h.eng.Create("alice", weeklySched("s1", "UTC", time.Monday, 9))
	h.eng.Create("alice", weeklySched("s2", "Asia/Shanghai", time.Wednesday, 18))
	horizon := t0.Add(30 * 24 * time.Hour)

	// 重排期间并发到达新增与修改，不得丢失。
	var wg sync.WaitGroup
	var rep ReplanReport
	wg.Add(3)
	go func() { defer wg.Done(); rep, _ = h.eng.Replan(t0, horizon, nil) }()
	go func() { defer wg.Done(); h.eng.Create("alice", weeklySched("s3", "UTC", time.Friday, 12)) }()
	go func() {
		defer wg.Done()
		h.eng.Update("alice", "s1", weeklySched("s1", "UTC", time.Tuesday, 9).Rule, schedule.DefaultPolicy())
	}()
	wg.Wait()

	// 重排后再次重排：结果必须能反映全部请求。
	rep, err := h.eng.Replan(t0, horizon, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Entries) != 3 {
		t.Fatalf("concurrent create lost: entries=%d", len(rep.Entries))
	}
	byID := map[string]ReplanEntry{}
	for _, e := range rep.Entries {
		byID[e.ScheduleID] = e
	}
	// s1 已改为周二触发。
	for _, o := range byID["s1"].After {
		if o.UTC.Weekday() != time.Tuesday {
			t.Fatalf("update lost in replan: %+v", o)
		}
	}
	if len(byID["s3"].After) == 0 {
		t.Fatal("new schedule missing from replan")
	}
	for _, e := range rep.Entries {
		if e.Note == "" {
			t.Fatalf("missing alignment note for %s", e.ScheduleID)
		}
		t.Logf("%s: %s", e.ScheduleID, e.Note)
	}
}

func TestDSTGapDeliveryViaEngine(t *testing.T) {
	h := newHarness(t, t.TempDir()+"/log", "2024a", time.Date(2021, 3, 1, 0, 0, 0, 0, time.UTC))
	// 美东每周日 02:30 —— 2021-03-14 落在间隙，默认策略顺延到 03:00 EDT。
	h.eng.Create("alice", weeklySched("s1", "America/New_York", time.Sunday, 2))
	s, _ := h.eng.Get("alice", "s1")
	s.Rule.Minute = 30
	h.eng.Update("alice", "s1", s.Rule, s.Policy)
	h.now = time.Date(2021, 3, 15, 0, 0, 0, 0, time.UTC)
	n, _ := h.eng.DispatchDue(h.now)
	if n != 2 { // 3/7 正常 + 3/14 顺延
		t.Fatalf("want 2, got %d", n)
	}
	got := h.delivered()
	shifted := got[1]
	want := time.Date(2021, 3, 14, 7, 0, 0, 0, time.UTC)
	if !shifted.UTC.Equal(want) {
		t.Fatalf("gap not shifted correctly: %v (reason %s)", shifted.UTC, shifted.Reason)
	}
	t.Logf("gap reason: %s", shifted.Reason)
}

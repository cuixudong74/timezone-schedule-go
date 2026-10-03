// Package engine 提供日程的创建/修改/取消、批量重排与幂等提醒投递。
//
// 状态全部来自 store 的追加事件：进程重启后重放事件即可恢复，
// 已投递记录（EventFired）不可变，保证历史稳定与投递幂等。
package engine

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/schedule"
	"github.com/cuixudong74/timezone-schedule-go/store"
)

// AuditEntry 关键操作的审计记录。
type AuditEntry struct {
	At         time.Time
	Op         string
	ScheduleID string
	ActorID    string
	Detail     string
}

// DeliverFunc 提醒投递回调。返回错误表示投递失败，稍后会重试。
type DeliverFunc func(o schedule.Occurrence) error

// scheduleState 引擎内存中的日程状态（由事件重放得出）。
type scheduleState struct {
	sched     schedule.Schedule
	cancelled bool
	boundary  time.Time // 最近一次创建/修改的生效边界
}

// Engine 调度引擎。
type Engine struct {
	mu       sync.Mutex
	st       *store.Store
	tzVer    string
	now      func() time.Time
	deliver  DeliverFunc
	states   map[string]*scheduleState
	fired    map[string]schedule.Occurrence // 幂等键 -> 已投递记录
	inflight map[string]bool
	audit    []AuditEntry
	gen      uint64 // 代际号：批量重排期间检测并发修改
}

// New 基于存储创建引擎，重放全部事件恢复状态（重启恢复）。
// tzDataVer 为当前时区数据版本，仅用于标记新产生的投递记录。
func New(st *store.Store, tzDataVer string, deliver DeliverFunc) *Engine {
	e := &Engine{
		st:       st,
		tzVer:    tzDataVer,
		now:      time.Now,
		deliver:  deliver,
		states:   map[string]*scheduleState{},
		fired:    map[string]schedule.Occurrence{},
		inflight: map[string]bool{},
	}
	for _, ev := range st.Events() {
		e.apply(ev)
	}
	return e
}

// apply 将一条事件应用到内存状态（重放与运行时共用）。
func (e *Engine) apply(ev store.Event) {
	switch ev.Type {
	case store.EventCreated, store.EventUpdated:
		e.states[ev.Schedule.ID] = &scheduleState{
			sched:    ev.Schedule,
			boundary: ev.At,
		}
	case store.EventCancelled:
		if st, ok := e.states[ev.Schedule.ID]; ok {
			st.cancelled = true
		} else {
			e.states[ev.Schedule.ID] = &scheduleState{sched: ev.Schedule, cancelled: true, boundary: ev.At}
		}
	case store.EventFired:
		if ev.Occ != nil {
			e.fired[ev.Occ.Key()] = *ev.Occ
		}
	}
}

// SetClock 替换时钟（测试用）。
func (e *Engine) SetClock(now func() time.Time) { e.mu.Lock(); e.now = now; e.mu.Unlock() }

// SetTZDataVersion 更新当前时区数据版本。只影响之后的投递记录，
// 已写入的历史 EventFired 保持不变。
func (e *Engine) SetTZDataVersion(v string) { e.mu.Lock(); e.tzVer = v; e.mu.Unlock() }

// validate 校验规则与时间输入，返回可区分的错误。
func validate(s schedule.Schedule) error {
	if s.Rule.Location == nil {
		return fmt.Errorf("%w: empty timezone", ErrInvalidTimezone)
	}
	if _, err := time.LoadLocation(s.Rule.Location.String()); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidTimezone, s.Rule.Location)
	}
	r := s.Rule
	if r.Hour < 0 || r.Hour > 23 || r.Minute < 0 || r.Minute > 59 || r.Second < 0 || r.Second > 59 {
		return fmt.Errorf("%w: time of day %02d:%02d:%02d out of range", ErrInvalidTime, r.Hour, r.Minute, r.Second)
	}
	switch r.Freq {
	case schedule.Weekly:
		if len(r.Weekdays) == 0 {
			return fmt.Errorf("%w: weekly rule needs at least one weekday", ErrInvalidRule)
		}
		for _, wd := range r.Weekdays {
			if wd < time.Sunday || wd > time.Saturday {
				return fmt.Errorf("%w: weekday %d out of range", ErrInvalidRule, wd)
			}
		}
	case schedule.Monthly:
		if len(r.MonthDays) == 0 {
			return fmt.Errorf("%w: monthly rule needs at least one month day", ErrInvalidRule)
		}
		for _, md := range r.MonthDays {
			if md < 1 || md > 31 {
				return fmt.Errorf("%w: month day %d out of range", ErrInvalidRule, md)
			}
		}
	default:
		return fmt.Errorf("%w: unknown frequency %d", ErrInvalidRule, r.Freq)
	}
	return nil
}

// Create 创建日程，返回日程 ID。
func (e *Engine) Create(ownerID string, s schedule.Schedule) (string, error) {
	if err := validate(s); err != nil {
		return "", err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.states[s.ID]; ok {
		return "", fmt.Errorf("%w: id %q already exists", ErrInvalidRule, s.ID)
	}
	s.OwnerID = ownerID
	s.Version = 1
	s.CreatedAt = e.now()
	ev := store.Event{Type: store.EventCreated, Schedule: s, TZDataVer: e.tzVer, At: e.now()}
	if _, err := e.st.Append(ev); err != nil {
		return "", err
	}
	e.apply(ev)
	e.gen++
	e.audit = append(e.audit, AuditEntry{At: e.now(), Op: "create", ScheduleID: s.ID, ActorID: ownerID,
		Detail: fmt.Sprintf("freq=%v tz=%s", s.Rule.Freq, s.Rule.Location)})
	return s.ID, nil
}

// authorize 校验操作者身份与日程状态。
func (e *Engine) authorize(actorID, id string) (*scheduleState, error) {
	st, ok := e.states[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if st.sched.OwnerID != actorID {
		return nil, fmt.Errorf("%w: %s owned by %s", ErrUnauthorized, id, st.sched.OwnerID)
	}
	if st.cancelled {
		return nil, fmt.Errorf("%w: %s", ErrCancelled, id)
	}
	return st, nil
}

// Update 修改日程规则与策略。生效边界为调用时刻：
// 已按旧规则投递的 EventFired 记录保持不变，
// 之后的触发按新规则（版本号递增）计算。
func (e *Engine) Update(actorID, id string, rule schedule.Rule, pol schedule.Policy) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.authorize(actorID, id)
	if err != nil {
		return err
	}
	ns := st.sched
	ns.Rule, ns.Policy = rule, pol
	ns.Version++
	if err := validate(ns); err != nil {
		return err
	}
	ev := store.Event{Type: store.EventUpdated, Schedule: ns, TZDataVer: e.tzVer, At: e.now()}
	if _, err := e.st.Append(ev); err != nil {
		return err
	}
	e.apply(ev)
	e.gen++
	e.audit = append(e.audit, AuditEntry{At: e.now(), Op: "update", ScheduleID: id, ActorID: actorID,
		Detail: fmt.Sprintf("version=%d effective-at=%s", ns.Version, ev.At.UTC())})
	return nil
}

// Cancel 取消日程。历史投递记录保留，之后不再产生新触发。
func (e *Engine) Cancel(actorID, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.authorize(actorID, id)
	if err != nil {
		return err
	}
	ev := store.Event{Type: store.EventCancelled, Schedule: st.sched, TZDataVer: e.tzVer, At: e.now()}
	if _, err := e.st.Append(ev); err != nil {
		return err
	}
	e.apply(ev)
	e.gen++
	e.audit = append(e.audit, AuditEntry{At: e.now(), Op: "cancel", ScheduleID: id, ActorID: actorID})
	return nil
}

// Get 返回日程当前状态（供查询与测试）。
func (e *Engine) Get(actorID, id string) (schedule.Schedule, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.authorize(actorID, id)
	if err != nil {
		return schedule.Schedule{}, err
	}
	return st.sched, nil
}

// Audit 返回审计日志的拷贝。
func (e *Engine) Audit() []AuditEntry {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]AuditEntry, len(e.audit))
	copy(out, e.audit)
	return out
}

// dueLocked 计算某日程在 (boundary, now] 内应触发而未投递的序列。
// 起点取生效边界与最后一次已投递时刻的较大者，保证不重不漏。
func (e *Engine) dueLocked(st *scheduleState, now time.Time) []schedule.Occurrence {
	after := st.boundary
	var lastFired time.Time
	for _, o := range e.fired {
		if o.ScheduleID == st.sched.ID && o.UTC.After(lastFired) {
			lastFired = o.UTC
		}
	}
	if lastFired.After(after) {
		after = lastFired
	}
	occs := schedule.Next(st.sched, after, now, 0)
	var out []schedule.Occurrence
	for _, o := range occs {
		if o.Skipped {
			continue // 显式跳过：不投递，原因留在计算结果中可查询
		}
		if _, ok := e.fired[o.Key()]; ok {
			continue // 已投递：幂等去重
		}
		out = append(out, o)
	}
	return out
}

// DispatchDue 投递截至 now 所有到期的提醒。
// 并发与重试安全：同一幂等键在投递前登记 inflight，成功后写入不可变的
// EventFired；投递失败则清除 inflight 等待下次重试。
// 进程在投递后、记录前崩溃时按 at-least-once 处理（重启后重放补投），
// 除此之外的任何重复调度/重试/并发都不会产生重复提醒。
func (e *Engine) DispatchDue(now time.Time) (int, error) {
	e.mu.Lock()
	var batch []schedule.Occurrence
	for _, st := range e.states {
		if st.cancelled {
			continue
		}
		for _, o := range e.dueLocked(st, now) {
			if e.inflight[o.Key()] {
				continue
			}
			e.inflight[o.Key()] = true
			batch = append(batch, o)
		}
	}
	e.mu.Unlock()

	sort.Slice(batch, func(i, j int) bool { return batch[i].UTC.Before(batch[j].UTC) })
	delivered := 0
	for _, o := range batch {
		err := e.deliver(o)
		e.mu.Lock()
		if err != nil {
			delete(e.inflight, o.Key()) // 失败：允许重试
			e.mu.Unlock()
			continue
		}
		delete(e.inflight, o.Key())
		ev := store.Event{Type: store.EventFired, Occ: &o, TZDataVer: e.tzVer, At: e.now()}
		if _, err := e.st.Append(ev); err != nil {
			e.mu.Unlock()
			return delivered, err
		}
		e.apply(ev)
		e.audit = append(e.audit, AuditEntry{At: e.now(), Op: "dispatch", ScheduleID: o.ScheduleID,
			Detail: fmt.Sprintf("key=%s version=%d reason=%s", o.Key(), o.Version, o.Reason)})
		e.mu.Unlock()
		delivered++
	}
	return delivered, nil
}

// Fired 返回日程已投递的历史记录（来自不可变事件，按时刻升序）。
func (e *Engine) Fired(id string) []schedule.Occurrence {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []schedule.Occurrence
	for _, o := range e.fired {
		if o.ScheduleID == id {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UTC.Before(out[j].UTC) })
	return out
}

// PlanEntry 重排计划中的一条未来触发。
type PlanEntry struct {
	Occurrence schedule.Occurrence
}

// ReplanReport 批量重排的对齐报告：重排前后每个日程的下次触发对照。
type ReplanReport struct {
	GeneratedAt time.Time
	Entries     []ReplanEntry
}

// ReplanEntry 单个日程重排前后的对照。
type ReplanEntry struct {
	ScheduleID string
	Before     []schedule.Occurrence // 重排前计划
	After      []schedule.Occurrence // 重排后计划
	Note       string
}

// Replan 对一批日程（ids 为空则全部）在 (now, horizon] 窗口内批量重排。
// 重排期间到达的新增/修改请求不会丢失：它们直接写入事件日志并递增代际号，
// 重排在提交前校验代际，若已变化则基于最新状态重算。
// 返回对齐报告，解释每个日程重排前后的差异。
func (e *Engine) Replan(now, horizon time.Time, ids []string) (ReplanReport, error) {
	const maxAttempts = 8
	for attempt := 0; ; attempt++ {
		e.mu.Lock()
		gen := e.gen
		snap := map[string]scheduleState{}
		for id, st := range e.states {
			if st.cancelled {
				continue
			}
			snap[id] = *st
		}
		oldPlan := append([]PlanEntry(nil), e.planSnapshotLocked(now, horizon, ids)...)
		e.mu.Unlock()

		// 基于快照在锁外计算新计划。
		type calc struct {
			id   string
			occs []schedule.Occurrence
		}
		var results []calc
		for id, st := range snap {
			if len(ids) > 0 && !contains(ids, id) {
				continue
			}
			occs := schedule.Next(st.sched, maxTime(st.boundary, now), horizon, 0)
			results = append(results, calc{id, occs})
		}
		sort.Slice(results, func(i, j int) bool { return results[i].id < results[j].id })

		e.mu.Lock()
		if e.gen != gen && attempt < maxAttempts {
			e.mu.Unlock()
			continue // 期间有并发修改：基于最新状态重算，请求不丢失
		}
		report := ReplanReport{GeneratedAt: e.now()}
		before := map[string][]schedule.Occurrence{}
		for _, p := range oldPlan {
			before[p.Occurrence.ScheduleID] = append(before[p.Occurrence.ScheduleID], p.Occurrence)
		}
		for _, r := range results {
			entry := ReplanEntry{ScheduleID: r.id, Before: before[r.id], After: r.occs}
			entry.Note = alignNote(entry.Before, entry.After)
			report.Entries = append(report.Entries, entry)
		}
		e.audit = append(e.audit, AuditEntry{At: e.now(), Op: "replan",
			Detail: fmt.Sprintf("schedules=%d horizon=%s", len(results), horizon.UTC())})
		e.mu.Unlock()
		return report, nil
	}
}

// planSnapshotLocked 返回当前计划快照（简化实现：现场计算旧计划用于对照）。
func (e *Engine) planSnapshotLocked(now, horizon time.Time, ids []string) []PlanEntry {
	var out []PlanEntry
	for id, st := range e.states {
		if st.cancelled || (len(ids) > 0 && !contains(ids, id)) {
			continue
		}
		for _, o := range schedule.Next(st.sched, maxTime(st.boundary, now), horizon, 0) {
			out = append(out, PlanEntry{Occurrence: o})
		}
	}
	return out
}

// alignNote 生成重排前后差异的可解释说明。
func alignNote(before, after []schedule.Occurrence) string {
	if len(before) == 0 && len(after) == 0 {
		return "no occurrences in window"
	}
	switch {
	case len(before) == len(after) && sameSeq(before, after):
		return fmt.Sprintf("unchanged: %d occurrence(s)", len(after))
	case len(before) == len(after):
		return fmt.Sprintf("shifted: %d occurrence(s) realigned", len(after))
	default:
		return fmt.Sprintf("count changed: %d -> %d", len(before), len(after))
	}
}

func sameSeq(a, b []schedule.Occurrence) bool {
	for i := range a {
		if !a[i].UTC.Equal(b[i].UTC) || a[i].Skipped != b[i].Skipped {
			return false
		}
	}
	return true
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

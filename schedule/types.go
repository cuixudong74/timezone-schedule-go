// Package schedule 定义跨时区周期日程的核心类型与触发时刻计算。
package schedule

import "time"

// Freq 周期频率。
type Freq int

const (
	Weekly  Freq = iota // 每周
	Monthly             // 每月
)

// GapPolicy 不存在本地时间（夏令时拨快跳过）的处理策略。
type GapPolicy int

const (
	GapShiftForward GapPolicy = iota // 顺延到间隙结束后的第一个有效本地时间
	GapSkip                          // 本次不触发（显式记录为 skipped，不静默丢弃）
)

// OverlapPolicy 重复本地时间（夏令时拨回重复）的处理策略。
type OverlapPolicy int

const (
	OverlapFirst OverlapPolicy = iota // 取第一次出现（较早的 UTC 时刻）
	OverlapLast                       // 取第二次出现
)

// Policy 不规则本地时间的取舍配置，可解释、可配置。
type Policy struct {
	Gap     GapPolicy
	Overlap OverlapPolicy
}

// DefaultPolicy 默认策略：间隙顺延、重复取第一次（保证至多触发一次）。
func DefaultPolicy() Policy {
	return Policy{Gap: GapShiftForward, Overlap: OverlapFirst}
}

// Rule 周期规则。触发时刻以 Location 时区的本地时间为准。
type Rule struct {
	Freq     Freq
	Location *time.Location
	// Weekly: 一周中的触发日（可多个）。
	Weekdays []time.Weekday
	// Monthly: 月中的触发日（1-31；若当月无该日则跳过该月）。
	MonthDays []int
	// 本地触发时刻。
	Hour, Minute, Second int
}

// Schedule 一条周期日程。
type Schedule struct {
	ID        string
	OwnerID   string
	Rule      Rule
	Policy    Policy
	CreatedAt time.Time
	// Version 每次修改递增；历史触发记录绑定产生它的版本。
	Version uint64
}

// Occurrence 一次具体触发。
type Occurrence struct {
	ScheduleID string
	Version    uint64 // 产生该次触发的规则版本
	// Local 目标时区下的本地名义时间（可能落在间隙/重复区）。
	Local time.Time
	// UTC 实际触发时刻（已按 Policy 解析）。
	UTC time.Time
	// Skipped 为 true 表示按 GapSkip 策略显式跳过，UTC 无效。
	Skipped bool
	// Reason 解释取舍原因，便于审计。
	Reason string
}

// Key 返回该次触发的幂等键。
func (o Occurrence) Key() string {
	return o.ScheduleID + "@" + o.UTC.UTC().Format(time.RFC3339Nano)
}

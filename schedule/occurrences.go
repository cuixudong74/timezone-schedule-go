package schedule

import (
	"fmt"
	"sort"
	"time"
)

// Next 返回 after（UTC，不含）之后、不超过 limit（含）的触发序列，按 UTC 升序。
// 计算只依赖 Rule/Policy 与传入的时区数据，从不读取机器本地时区，
// 因此在不同机器与不同默认时区下结果一致。
func Next(s Schedule, after, limit time.Time, max int) []Occurrence {
	loc := s.Rule.Location
	if loc == nil {
		loc = time.UTC
	}
	var out []Occurrence
	// 以目标时区的“本地日”为单位迭代，避免用 24h 步进跨越 DST 时错位。
	start := after.In(loc)
	day := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	end := limit.In(loc)
	endDay := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, loc)
	for !day.After(endDay) {
		if s.matchesDay(day) {
			y, mo, dd := day.Date()
			utc, skipped, reason := resolveLocal(loc, y, mo, dd,
				s.Rule.Hour, s.Rule.Minute, s.Rule.Second, s.Policy)
			occ := Occurrence{
				ScheduleID: s.ID,
				Version:    s.Version,
				// 注意：间隙日的 Local 会被 time.Date 归一化，
				// 名义墙钟以 Reason 中的说明为准。
				Local:   time.Date(y, mo, dd, s.Rule.Hour, s.Rule.Minute, s.Rule.Second, 0, loc),
				UTC:     utc,
				Skipped: skipped,
				Reason:  reason,
			}
			if skipped {
				// 跳过项没有 UTC，用名义本地日与窗口的本地日比较限定范围。
				nominal := time.Date(y, mo, dd, s.Rule.Hour, s.Rule.Minute, s.Rule.Second, 0, time.UTC)
				if nominal.After(wallAsUTC(after.In(loc))) && !nominal.After(wallAsUTC(end)) {
					out = append(out, occ)
				}
			} else if utc.After(after) && !utc.After(limit) {
				out = append(out, occ)
			}
		}
		day = time.Date(day.Year(), day.Month(), day.Day()+1, 0, 0, 0, 0, loc)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Skipped != b.Skipped {
			return !a.Skipped // 有效触发在前
		}
		return a.UTC.Before(b.UTC)
	})
	if max > 0 && len(out) > max {
		out = out[:max]
	}
	return out
}

// wallAsUTC 把本地墙钟分量平移到 UTC 表达，仅用于分量比较。
func wallAsUTC(l time.Time) time.Time {
	return time.Date(l.Year(), l.Month(), l.Day(), l.Hour(), l.Minute(), l.Second(), 0, time.UTC)
}

// matchesDay 判断目标时区的某个本地日是否命中规则。
func (s Schedule) matchesDay(day time.Time) bool {
	switch s.Rule.Freq {
	case Weekly:
		for _, wd := range s.Rule.Weekdays {
			if day.Weekday() == wd {
				return true
			}
		}
	case Monthly:
		for _, md := range s.Rule.MonthDays {
			if day.Day() == md {
				return true
			}
		}
	}
	return false
}

func sameWall(t time.Time, loc *time.Location, y int, mo time.Month, d, h, mi, sec int) bool {
	l := t.In(loc)
	return l.Year() == y && l.Month() == mo && l.Day() == d &&
		l.Hour() == h && l.Minute() == mi && l.Second() == sec
}

// resolveLocal 将目标时区的本地名义时间（以分量给出，避免被 time.Date 归一化）
// 解析为实际 UTC 触发时刻。
//
// 检测方法：把名义墙钟当作 UTC 得到锚点 u，在 u 前后充分范围内采样时区偏移
// （覆盖 ±14h 的全部法定偏移并留有余量），反解出所有满足
// wallclock(cand)==名义时间 的 UTC 候选：
//   - 0 个候选：该本地时间不存在（夏令时拨快的间隙），按 GapPolicy 处理；
//   - 1 个候选：正常映射；
//   - 2 个候选：该本地时间重复出现（夏令时拨回），按 OverlapPolicy 取其一，
//     保证至多触发一次。
func resolveLocal(loc *time.Location, y int, mo time.Month, d, h, mi, sec int, p Policy) (time.Time, bool, string) {
	u := time.Date(y, mo, d, h, mi, sec, 0, time.UTC) // 名义墙钟的 UTC 锚点

	seen := map[int64]bool{}
	var cands []time.Time
	for delta := -30 * time.Hour; delta <= 30*time.Hour; delta += 6 * time.Hour {
		_, off := u.Add(delta).In(loc).Zone()
		cand := u.Add(-time.Duration(off) * time.Second)
		if seen[cand.Unix()] {
			continue
		}
		if sameWall(cand, loc, y, mo, d, h, mi, sec) {
			seen[cand.Unix()] = true
			cands = append(cands, cand)
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Before(cands[j]) })

	nominal := u.Format("2006-01-02 15:04:05")
	switch len(cands) {
	case 1:
		return cands[0], false, "normal"
	case 2:
		if p.Overlap == OverlapLast {
			return cands[1], false, fmt.Sprintf("overlap: local time %s repeats in %s, chose last occurrence", nominal, loc)
		}
		return cands[0], false, fmt.Sprintf("overlap: local time %s repeats in %s, chose first occurrence", nominal, loc)
	}

	// 0 个候选：间隙，本地时间不存在。
	if p.Gap == GapSkip {
		return time.Time{}, true, fmt.Sprintf("gap: local time %s does not exist in %s, skipped by policy", nominal, loc)
	}
	// GapShiftForward：找到墙钟首次 >= 名义时间的时刻（即间隙结束后的第一个有效本地时间）。
	t := firstWallAtOrAfter(loc, y, mo, d, h, mi, sec)
	return t, false, fmt.Sprintf("gap: local time %s does not exist in %s, shifted forward to %s",
		nominal, loc, t.In(loc).Format("2006-01-02 15:04:05 MST"))
}

// firstWallAtOrAfter 返回墙钟首次大于等于名义分量的 UTC 时刻。
// 先以 15 分钟步长粗扫 bracket，再二分精确到秒。间隙通常不超过数小时。
func firstWallAtOrAfter(loc *time.Location, y int, mo time.Month, d, h, mi, sec int) time.Time {
	ge := func(t time.Time) bool { return wallAtOrAfter(t.In(loc), y, mo, d, h, mi, sec) }
	lo := time.Date(y, mo, d, h, mi, sec, 0, time.UTC).Add(-15 * time.Hour)
	hi := lo
	for i := 0; i < 256 && !ge(hi); i++ {
		hi = hi.Add(15 * time.Minute)
	}
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2)
		if ge(mid) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return hi
}

// wallAtOrAfter 比较 l 的墙钟分量是否 >= 给定分量（按字典序）。
func wallAtOrAfter(l time.Time, y int, mo time.Month, d, h, mi, sec int) bool {
	a := []int{l.Year(), int(l.Month()), l.Day(), l.Hour(), l.Minute(), l.Second()}
	b := []int{y, int(mo), d, h, mi, sec}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return true
}

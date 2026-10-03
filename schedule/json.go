package schedule

import (
	"encoding/json"
	"fmt"
	"time"
)

// ruleJSON 是 Rule 的序列化形态：时区以 IANA 名称落盘，
// 反序列化时经 time.LoadLocation 复原，保证跨进程一致。
type ruleJSON struct {
	Freq      Freq   `json:"freq"`
	Location  string `json:"location"`
	Weekdays  []int  `json:"weekdays,omitempty"`
	MonthDays []int  `json:"month_days,omitempty"`
	Hour      int    `json:"hour"`
	Minute    int    `json:"minute"`
	Second    int    `json:"second"`
}

// MarshalJSON 实现 json.Marshaler。
func (r Rule) MarshalJSON() ([]byte, error) {
	j := ruleJSON{Freq: r.Freq, Hour: r.Hour, Minute: r.Minute, Second: r.Second,
		MonthDays: r.MonthDays}
	if r.Location != nil {
		j.Location = r.Location.String()
	}
	for _, wd := range r.Weekdays {
		j.Weekdays = append(j.Weekdays, int(wd))
	}
	return json.Marshal(j)
}

// UnmarshalJSON 实现 json.Unmarshaler。非法时区标识在此被拒绝。
func (r *Rule) UnmarshalJSON(data []byte) error {
	var j ruleJSON
	if err := json.Unmarshal(data, &j); err != nil {
		return err
	}
	r.Freq, r.Hour, r.Minute, r.Second = j.Freq, j.Hour, j.Minute, j.Second
	r.MonthDays = j.MonthDays
	if j.Location == "" {
		r.Location = time.UTC
	} else {
		loc, err := time.LoadLocation(j.Location)
		if err != nil {
			return fmt.Errorf("invalid timezone identifier %q: %w", j.Location, err)
		}
		r.Location = loc
	}
	for _, wd := range j.Weekdays {
		r.Weekdays = append(r.Weekdays, time.Weekday(wd))
	}
	return nil
}

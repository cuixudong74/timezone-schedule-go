// Package store 提供事件溯源式持久化：日程的创建/修改/取消以追加事件记录，
// 历史触发记录（EventFired）一旦写入永不改写，进程重启后通过重放恢复。
package store

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/schedule"
)

// EventType 事件类型。
type EventType string

const (
	EventCreated   EventType = "created"
	EventUpdated   EventType = "updated"
	EventCancelled EventType = "cancelled"
	// EventFired 已投递的触发记录。它绑定产生它的规则版本与当时的
	// 时区数据版本，写入后不可变——时区规则升级不会重算历史。
	EventFired EventType = "fired"
)

// Event 一条追加式日志记录。
type Event struct {
	Seq       uint64               `json:"seq"`
	Type      EventType            `json:"type"`
	Schedule  schedule.Schedule    `json:"schedule"`
	Occ       *schedule.Occurrence `json:"occ,omitempty"` // 仅 EventFired
	TZDataVer string               `json:"tz_data_ver"`   // 事件发生时的时区数据版本
	At        time.Time            `json:"at"`
}

// Store 追加式事件存储，单文件 JSON Lines，写后 fsync。
type Store struct {
	mu     sync.Mutex
	path   string
	f      *os.File
	enc    *json.Encoder
	events []Event
	seq    uint64
}

// Open 打开（或创建）位于 path 的存储，加载已有事件。
func Open(path string) (*Store, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, f: f}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("corrupt event log at %s: %w", path, err)
		}
		s.events = append(s.events, e)
		if e.Seq > s.seq {
			s.seq = e.Seq
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, 2); err != nil {
		return nil, err
	}
	s.enc = json.NewEncoder(f)
	return s, nil
}

// Append 追加事件，分配递增 Seq，落盘并 fsync 后返回。
func (s *Store) Append(e Event) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	e.Seq = s.seq
	if err := s.enc.Encode(e); err != nil {
		return 0, err
	}
	if err := s.f.Sync(); err != nil {
		return 0, err
	}
	s.events = append(s.events, e)
	return e.Seq, nil
}

// Events 返回全部事件（按 Seq 升序）的拷贝。
func (s *Store) Events() []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

// Close 关闭底层文件。
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

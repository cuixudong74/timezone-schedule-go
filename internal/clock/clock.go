// Package clock abstracts time for testability.
package clock

import (
	"sync"
	"time"
)

// Clock is the time source used by the scheduler.
type Clock interface {
	Now() time.Time
}

// Real is the wall clock.
type Real struct{}

// Now returns the current time.
func (Real) Now() time.Time { return time.Now() }

// Fake is a manually controlled clock for tests.
type Fake struct {
	mu sync.Mutex
	t  time.Time
}

// NewFake returns a frozen clock at t.
func NewFake(t time.Time) *Fake {
	return &Fake{t: t}
}

// Now returns the frozen time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t
}

// Set freezes the clock at t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = t
}

// Advance moves the clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.t = f.t.Add(d)
}

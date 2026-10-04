// Package config loads runtime configuration from the environment.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

// Config is the complete runtime configuration.
type Config struct {
	Addr             string
	DBPath           string
	TZDir            string
	TZVersion        string
	Horizon          time.Duration
	MaxOccurrences   int
	WorkerEnabled   bool
	WorkerInterval  time.Duration
	Lease            time.Duration
	MaxAttempts     int
	RetryBackoff    time.Duration
	DSTRepeated     tz.RepeatedPolicy
	DSTMissing      tz.MissingPolicy
	MonthDayOutOfRange schedule.MonthDayPolicy
}

// FromEnv loads configuration from environment variables with defaults.
func FromEnv() Config {
	c := Config{
		Addr:            ":8080",
		Horizon:         400 * 24 * time.Hour,
		MaxOccurrences:  2000,
		WorkerEnabled:   true,
		WorkerInterval:  time.Second,
		Lease:           30 * time.Second,
		MaxAttempts:     5,
		RetryBackoff:    10 * time.Second,
		DSTRepeated:     tz.RepeatedFirst,
		DSTMissing:      tz.MissingSkip,
		MonthDayOutOfRange: schedule.MonthDaySkip,
	}
	if v := os.Getenv("TZ_SCHED_ADDR"); v != "" {
		c.Addr = v
	}
	if v := os.Getenv("TZ_SCHED_DB"); v != "" {
		c.DBPath = v
	}
	if v := os.Getenv("TZ_SCHED_TZDIR"); v != "" {
		c.TZDir = v
	}
	if v := os.Getenv("TZ_SCHED_TZ_VERSION"); v != "" {
		c.TZVersion = v
	}
	if v := os.Getenv("TZ_SCHED_HORIZON_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Horizon = time.Duration(n) * 24 * time.Hour
		}
	}
	if v := os.Getenv("TZ_SCHED_MAX_OCCURRENCES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxOccurrences = n
		}
	}
	if v := os.Getenv("TZ_SCHED_WORKER_ENABLED"); v != "" {
		c.WorkerEnabled = v == "1" || strings.EqualFold(v, "true")
	}
	if v := os.Getenv("TZ_SCHED_WORKER_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.WorkerInterval = d
		}
	}
	if v := os.Getenv("TZ_SCHED_LEASE_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.Lease = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("TZ_SCHED_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MaxAttempts = n
		}
	}
	if v := os.Getenv("TZ_SCHED_RETRY_BACKOFF"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.RetryBackoff = d
		}
	}
	if v := os.Getenv("TZ_SCHED_DST_REPEATED"); v != "" {
		switch strings.ToLower(v) {
		case "first":
			c.DSTRepeated = tz.RepeatedFirst
		case "last":
			c.DSTRepeated = tz.RepeatedLast
		}
	}
	if v := os.Getenv("TZ_SCHED_DST_MISSING"); v != "" {
		switch strings.ToLower(v) {
		case "skip":
			c.DSTMissing = tz.MissingSkip
		case "shift-forward":
			c.DSTMissing = tz.MissingShiftForward
		case "shift-backward":
			c.DSTMissing = tz.MissingShiftBackward
		}
	}
	if v := os.Getenv("TZ_SCHED_MONTH_DAY_OOB"); v != "" {
		switch strings.ToLower(v) {
		case "skip":
			c.MonthDayOutOfRange = schedule.MonthDaySkip
		case "clamp":
			c.MonthDayOutOfRange = schedule.MonthDayClamp
		}
	}
	return c
}

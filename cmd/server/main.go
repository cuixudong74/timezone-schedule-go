// Command server runs the timezone schedule HTTP service.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/api"
	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/config"
	"github.com/cuixudong74/timezone-schedule-go/internal/deliver"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/scheduler"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

func main() {
	cfg := config.FromEnv()

	var st store.Store
	if cfg.DBPath != "" {
		bs, err := store.OpenBolt(cfg.DBPath)
		if err != nil {
			log.Fatalf("open store: %v", err)
		}
		defer bs.Close()
		st = bs
		log.Printf("persistent store: %s", cfg.DBPath)
	} else {
		st = store.NewMemStore()
		log.Printf("in-memory store (set TZ_SCHED_DB for persistence)")
	}

	var tzp tz.Provider
	if cfg.TZDir != "" {
		p, err := tz.NewReloadableProvider(cfg.TZDir)
		if err != nil {
			log.Fatalf("load timezone dir: %v", err)
		}
		tzp = p
		log.Printf("reloadable timezone provider: %s (version %s)", cfg.TZDir, p.Version())
	} else {
		tzp = tz.NewEmbeddedProvider(cfg.TZVersion)
		log.Printf("embedded timezone provider (version %s)", tzp.Version())
	}

	engine := schedule.NewEngine(tzp, tz.DSTPolicy{Repeated: cfg.DSTRepeated, Missing: cfg.DSTMissing}, cfg.MonthDayOutOfRange, cfg.Horizon)
	sched := scheduler.New(st, tzp, engine, clock.Real{}, scheduler.Config{Horizon: cfg.Horizon, MaxOccurrences: cfg.MaxOccurrences})

	// Delivery worker with logging sender.
	if cfg.WorkerEnabled {
		worker := deliver.NewWorker(st, deliver.SenderFunc(func(ctx context.Context, scheduleID string, seq int64, scheduledAt time.Time, wall schedule.WallTime) error {
			log.Printf("DELIVER schedule=%s seq=%d at=%s wall=%v", scheduleID, seq, scheduledAt.UTC().Format(time.RFC3339), wall)
			return nil
		}), clock.Real{}, deliver.Config{
			WorkerID: "worker-1", PollInterval: cfg.WorkerInterval, Lease: cfg.Lease,
			BatchLimit: 100, MaxAttempts: cfg.MaxAttempts, RetryBackoff: cfg.RetryBackoff,
		})
		go func() {
			if err := worker.Run(context.Background()); err != nil && err != context.Canceled {
				log.Printf("worker: %v", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: api.New(sched).Handler(),
	}
	go func() {
		log.Printf("HTTP listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// Package api exposes the scheduler over HTTP with JSON.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/scheduler"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

// Server is the HTTP API.
type Server struct {
	sched *scheduler.Scheduler
}

// New returns a Server.
func New(sched *scheduler.Scheduler) *Server {
	return &Server{sched: sched}
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /api/schedules", s.handleCreate)
	mux.HandleFunc("GET /api/schedules", s.handleList)
	mux.HandleFunc("GET /api/schedules/{id}", s.handleGet)
	mux.HandleFunc("PATCH /api/schedules/{id}", s.handleUpdate)
	mux.HandleFunc("POST /api/schedules/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /api/schedules/batch-reschedule", s.handleBatch)
	mux.HandleFunc("GET /api/schedules/{id}/triggers", s.handleTriggers)
	mux.HandleFunc("GET /api/audit", s.handleAudit)
	return mux
}

func actorID(r *http.Request) (string, bool) {
	id := r.Header.Get("X-User-Id")
	return id, id != ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, schedule.ErrUnauthorized):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "unauthorized", "message": err.Error()})
	case errors.Is(err, tz.ErrInvalidTimezone):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_timezone", "message": err.Error()})
	case errors.Is(err, schedule.ErrInvalidRecurrence):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_recurrence", "message": err.Error()})
	case errors.Is(err, schedule.ErrInvalidTimeInput):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_time_input", "message": err.Error()})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not_found", "message": err.Error()})
	case errors.Is(err, schedule.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": "conflict", "message": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal", "message": err.Error()})
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type ruleDTO struct {
	Kind    string `json:"kind"`
	Weekly  *weeklyDTO  `json:"weekly,omitempty"`
	Monthly *monthlyDTO `json:"monthly,omitempty"`
}

type weeklyDTO struct {
	Weekdays      []int `json:"weekdays"`
	Hour          int   `json:"hour"`
	Minute        int   `json:"minute"`
	Second        int   `json:"second"`
	IntervalWeeks int   `json:"interval_weeks"`
}

type monthlyDTO struct {
	MonthDays      []int `json:"month_days"`
	Hour           int   `json:"hour"`
	Minute         int   `json:"minute"`
	Second         int   `json:"second"`
	IntervalMonths int   `json:"interval_months"`
}

func (d ruleDTO) toEnvelope() (schedule.RuleEnvelope, error) {
	env := schedule.RuleEnvelope{}
	switch d.Kind {
	case "weekly":
		if d.Weekly == nil {
			return env, schedule.ErrInvalidRecurrence
		}
		days := make([]time.Weekday, len(d.Weekly.Weekdays))
		for i, wd := range d.Weekly.Weekdays {
			days[i] = time.Weekday(wd)
		}
		env.Kind = schedule.KindWeekly
		env.Weekly = &schedule.WeeklyRule{
			Weekdays: days, Hour: d.Weekly.Hour, Minute: d.Weekly.Minute, Second: d.Weekly.Second,
			IntervalWeeks: d.Weekly.IntervalWeeks,
		}
	case "monthly":
		if d.Monthly == nil {
			return env, schedule.ErrInvalidRecurrence
		}
		env.Kind = schedule.KindMonthly
		env.Monthly = &schedule.MonthlyRule{
			MonthDays: d.Monthly.MonthDays, Hour: d.Monthly.Hour, Minute: d.Monthly.Minute, Second: d.Monthly.Second,
			IntervalMonths: d.Monthly.IntervalMonths,
		}
	default:
		return env, schedule.ErrInvalidRecurrence
	}
	return env, nil
}

type createRequest struct {
	Timezone string  `json:"timezone"`
	Rule     ruleDTO `json:"rule"`
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "message": err.Error()})
		return
	}
	env, err := req.Rule.toEnvelope()
	if err != nil {
		writeError(w, err)
		return
	}
	sch, occs, err := s.sched.Create(r.Context(), scheduler.CreateInput{
		ActorID: actor, Timezone: req.Timezone, Rule: env,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"schedule": sch, "occurrences": occs})
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	items, err := s.sched.List(r.Context(), actor)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedules": items})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	sch, err := s.sched.Get(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

type updateRequest struct {
	Timezone    *string `json:"timezone"`
	Rule        *ruleDTO `json:"rule"`
	EffectiveAt *time.Time `json:"effective_at"`
	Reason      string  `json:"reason"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "message": err.Error()})
		return
	}
	in := scheduler.UpdateInput{
		ActorID: actor, ScheduleID: r.PathValue("id"),
		Timezone: req.Timezone, EffectiveAt: req.EffectiveAt, Reason: req.Reason,
	}
	if req.Rule != nil {
		env, err := req.Rule.toEnvelope()
		if err != nil {
			writeError(w, err)
			return
		}
		in.Rule = &env
	}
	sch, report, err := s.sched.Update(r.Context(), in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"schedule": sch, "report": report})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sch, err := s.sched.Cancel(r.Context(), actor, r.PathValue("id"), body.Reason)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

type batchItemDTO struct {
	ScheduleID  string     `json:"schedule_id"`
	Timezone    *string    `json:"timezone"`
	Rule        *ruleDTO   `json:"rule"`
	EffectiveAt *time.Time `json:"effective_at"`
	Cancel      bool       `json:"cancel"`
	Reason      string     `json:"reason"`
}

type batchRequest struct {
	Items []batchItemDTO `json:"items"`
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	var req batchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_request", "message": err.Error()})
		return
	}
	items := make([]scheduler.RescheduleItem, 0, len(req.Items))
	for _, it := range req.Items {
		item := scheduler.RescheduleItem{
			ScheduleID: it.ScheduleID, Timezone: it.Timezone,
			EffectiveAt: it.EffectiveAt, Cancel: it.Cancel, Reason: it.Reason,
		}
		if it.Rule != nil {
			env, err := it.Rule.toEnvelope()
			if err != nil {
				writeError(w, err)
				return
			}
			item.Rule = &env
		}
		items = append(items, item)
	}
	res, err := s.sched.BatchReschedule(r.Context(), actor, items)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleTriggers(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	trigs, err := s.sched.Triggers(r.Context(), actor, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"triggers": trigs})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	actor, ok := actorID(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized", "message": "missing X-User-Id header"})
		return
	}
	recs, err := s.sched.Audit(r.Context(), actor, 100)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": recs})
}

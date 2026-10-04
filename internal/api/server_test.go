package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/cuixudong74/timezone-schedule-go/internal/clock"
	"github.com/cuixudong74/timezone-schedule-go/internal/schedule"
	"github.com/cuixudong74/timezone-schedule-go/internal/scheduler"
	"github.com/cuixudong74/timezone-schedule-go/internal/store"
	"github.com/cuixudong74/timezone-schedule-go/internal/tz"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	st := store.NewMemStore()
	tzp := tz.NewEmbeddedProvider("test")
	engine := schedule.NewEngine(tzp, tz.DefaultPolicy(), schedule.MonthDaySkip, 400*24*time.Hour)
	sched := scheduler.New(st, tzp, engine, clock.NewFake(time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)), scheduler.Config{Horizon: 400 * 24 * time.Hour})
	return New(sched)
}

func do(t *testing.T, s *Server, method, path string, body any, user string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestHealth(t *testing.T) {
	s := newTestServer(t)
	code, out := do(t, s, "GET", "/healthz", nil, "")
	if code != http.StatusOK {
		t.Fatalf("code: %d", code)
	}
	if out["status"] != "ok" {
		t.Fatalf("out: %v", out)
	}
}

func TestCreateSchedule(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"timezone": "America/New_York",
		"rule": map[string]any{
			"kind": "weekly",
			"weekly": map[string]any{"weekdays": []int{2}, "hour": 10, "minute": 0},
		},
	}
	code, out := do(t, s, "POST", "/api/schedules", body, "alice")
	if code != http.StatusCreated {
		t.Fatalf("code: %d body: %v", code, out)
	}
	sch := out["schedule"].(map[string]any)
	if sch["owner_id"] != "alice" {
		t.Fatalf("owner: %v", sch["owner_id"])
	}
	occs := out["occurrences"].([]any)
	if len(occs) == 0 {
		t.Fatal("no occurrences")
	}
}

func TestCreateRequiresAuth(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{"timezone": "America/New_York", "rule": map[string]any{"kind": "weekly"}}
	code, _ := do(t, s, "POST", "/api/schedules", body, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", code)
	}
}

func TestInvalidTimezone(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"timezone": "Not/AZone",
		"rule":     map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{2}, "hour": 10}},
	}
	code, out := do(t, s, "POST", "/api/schedules", body, "alice")
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", code)
	}
	if out["error"] != "invalid_timezone" {
		t.Fatalf("error: %v", out["error"])
	}
}

func TestGetUpdateCancel(t *testing.T) {
	s := newTestServer(t)
	// Create
	body := map[string]any{
		"timezone": "America/New_York",
		"rule":     map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{2}, "hour": 10}},
	}
	code, out := do(t, s, "POST", "/api/schedules", body, "alice")
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	sch := out["schedule"].(map[string]any)
	id := sch["id"].(string)

	// Get
	code, got := do(t, s, "GET", "/api/schedules/"+id, nil, "alice")
	if code != http.StatusOK {
		t.Fatalf("get: %d", code)
	}
	if got["id"] != id {
		t.Fatalf("id mismatch")
	}

	// Update
	upd := map[string]any{
		"rule": map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{5}, "hour": 10}},
	}
	code, out = do(t, s, "PATCH", "/api/schedules/"+id, upd, "alice")
	if code != http.StatusOK {
		t.Fatalf("update: %d %v", code, out)
	}
	report := out["report"].(map[string]any)
	if report["revision"].(float64) != 2 {
		t.Fatalf("revision: %v", report["revision"])
	}

	// Cancel
	code, out = do(t, s, "POST", "/api/schedules/"+id+"/cancel", map[string]any{"reason": "done"}, "alice")
	if code != http.StatusOK {
		t.Fatalf("cancel: %d %v", code, out)
	}
	if out["status"] != "cancelled" {
		t.Fatalf("status: %v", out["status"])
	}
}

func TestCannotAccessOthersSchedule(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"timezone": "America/New_York",
		"rule":     map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{2}, "hour": 10}},
	}
	code, out := do(t, s, "POST", "/api/schedules", body, "alice")
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	id := out["schedule"].(map[string]any)["id"].(string)

	// Bob cannot get Alice's schedule.
	code, _ = do(t, s, "GET", "/api/schedules/"+id, nil, "bob")
	if code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", code)
	}
}

func TestListAndTriggers(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"timezone": "America/New_York",
		"rule":     map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{2}, "hour": 10}},
	}
	code, out := do(t, s, "POST", "/api/schedules", body, "alice")
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	id := out["schedule"].(map[string]any)["id"].(string)

	code, list := do(t, s, "GET", "/api/schedules", nil, "alice")
	if code != http.StatusOK {
		t.Fatalf("list: %d", code)
	}
	schedules := list["schedules"].([]any)
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}

	code, tr := do(t, s, "GET", "/api/schedules/"+id+"/triggers", nil, "alice")
	if code != http.StatusOK {
		t.Fatalf("triggers: %d", code)
	}
	triggers := tr["triggers"].([]any)
	if len(triggers) == 0 {
		t.Fatal("no triggers")
	}
}

func TestBatchEndpoint(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"timezone": "America/New_York",
		"rule":     map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{2}, "hour": 10}},
	}
	code, out := do(t, s, "POST", "/api/schedules", body, "alice")
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	id := out["schedule"].(map[string]any)["id"].(string)

	batch := map[string]any{
		"items": []any{
			map[string]any{"schedule_id": id, "cancel": true, "reason": "batch"},
		},
	}
	code, res := do(t, s, "POST", "/api/schedules/batch-reschedule", batch, "alice")
	if code != http.StatusOK {
		t.Fatalf("batch: %d %v", code, res)
	}
	results := res["results"].([]any)
	if results[0].(map[string]any)["action"] != "cancelled" {
		t.Fatalf("action: %v", results[0])
	}
}

func TestAuditEndpoint(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"timezone": "America/New_York",
		"rule":     map[string]any{"kind": "weekly", "weekly": map[string]any{"weekdays": []int{2}, "hour": 10}},
	}
	do(t, s, "POST", "/api/schedules", body, "alice")
	code, out := do(t, s, "GET", "/api/audit", nil, "alice")
	if code != http.StatusOK {
		t.Fatalf("audit: %d", code)
	}
	audit := out["audit"].([]any)
	if len(audit) == 0 {
		t.Fatal("no audit records")
	}
	if audit[0].(map[string]any)["action"] != "schedule.create" {
		t.Fatalf("audit action: %v", audit[0])
	}
}

var _ = context.Background

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/autopilot"
	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	settingssvc "github.com/aoagents/agent-orchestrator/backend/internal/service/settings"
)

type fakeAutopilot struct {
	calls   []string
	callers []string
	err     error
	view    autopilot.View
	run     domain.AutopilotRun
	event   domain.AutopilotEvent
	done    autopilot.DoneResult
	stopped bool
	all     int
	maxIter int
}

func (f *fakeAutopilot) rec(ctx context.Context, call string) {
	f.calls = append(f.calls, call)
	f.callers = append(f.callers, caller.From(ctx))
}

func (f *fakeAutopilot) Start(ctx context.Context, id domain.SessionID, task string, max int) (autopilot.View, error) {
	f.rec(ctx, "start "+string(id)+"|"+task)
	f.maxIter = max
	return f.view, f.err
}
func (f *fakeAutopilot) View(ctx context.Context, id domain.SessionID) (autopilot.View, error) {
	f.rec(ctx, "view "+string(id))
	return f.view, f.err
}
func (f *fakeAutopilot) List(ctx context.Context) ([]domain.AutopilotRun, error) {
	f.rec(ctx, "list")
	return []domain.AutopilotRun{f.run}, f.err
}
func (f *fakeAutopilot) Stop(ctx context.Context, id domain.SessionID) (bool, error) {
	f.rec(ctx, "stop "+string(id))
	return f.stopped, f.err
}
func (f *fakeAutopilot) StopAll(ctx context.Context) (int, error) {
	f.rec(ctx, "stopall")
	return f.all, f.err
}
func (f *fakeAutopilot) Resume(ctx context.Context, id domain.SessionID) (autopilot.View, error) {
	f.rec(ctx, "resume "+string(id))
	return f.view, f.err
}
func (f *fakeAutopilot) SetCriteria(ctx context.Context, id domain.SessionID, c string) (domain.AutopilotRun, error) {
	f.rec(ctx, "criteria "+string(id)+"|"+c)
	return f.run, f.err
}
func (f *fakeAutopilot) Decide(ctx context.Context, id domain.SessionID, q, c, w string) (domain.AutopilotEvent, error) {
	f.rec(ctx, "decide "+string(id)+"|"+q+"|"+c+"|"+w)
	return f.event, f.err
}
func (f *fakeAutopilot) Park(ctx context.Context, id domain.SessionID, l domain.HardLimit, item, why string) (domain.AutopilotEvent, bool, error) {
	f.rec(ctx, "park "+string(id)+"|"+string(l)+"|"+item+"|"+why)
	return f.event, f.stopped, f.err
}
func (f *fakeAutopilot) Done(ctx context.Context, id domain.SessionID, ev, ci string) (autopilot.DoneResult, error) {
	f.rec(ctx, "done "+string(id)+"|"+ev+"|"+ci)
	return f.done, f.err
}
func (f *fakeAutopilot) ResolveParked(ctx context.Context, id string) (domain.AutopilotEvent, error) {
	f.rec(ctx, "resolve "+id)
	return f.event, f.err
}

func autopilotRouter(svc AutopilotService) http.Handler {
	r := chi.NewRouter()
	r.Use(caller.Middleware)
	(&AutopilotController{Svc: svc}).Register(r)
	return r
}

func doJSON(h http.Handler, method, path, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var apTime = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

func sampleRun() domain.AutopilotRun {
	return domain.AutopilotRun{
		ID: "ap_1", SessionID: "mer-orchestrator-1", ProjectID: "mer", Task: "Ship it", State: domain.AutopilotRunning,
		Iteration: 2, MaxIterations: 30, StartedAt: apTime, UpdatedAt: apTime,
	}
}

func TestAutopilotStartPassesTheTaskAndTheCapAndReturnsTheView(t *testing.T) {
	run := sampleRun()
	svc := &fakeAutopilot{view: autopilot.View{
		Run:    &run,
		Events: []domain.AutopilotEvent{{ID: "ape_1", RunID: "ap_1", SessionID: "mer-orchestrator-1", Kind: domain.AutopilotEventStarted, Summary: "Started", CreatedAt: apTime}},
	}}
	w := doJSON(autopilotRouter(svc), http.MethodPost, "/sessions/mer-orchestrator-1/autopilot", `{"task":"Ship it","maxIterations":12}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if svc.calls[0] != "start mer-orchestrator-1|Ship it" || svc.maxIter != 12 {
		t.Fatalf("calls = %v max = %d", svc.calls, svc.maxIter)
	}
	var got AutopilotViewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Run == nil || got.Run.State != "running" || got.Run.Iteration != 2 || got.Run.LastNudgeAt != nil || got.Run.EndedAt != nil {
		t.Fatalf("run = %+v", got.Run)
	}
	if len(got.Events) != 1 || got.Events[0].Kind != "started" || got.Parked == nil || got.Warnings == nil {
		t.Fatalf("view = %+v: lists must be arrays, never null", got)
	}
}

func TestAutopilotStartRefusesAnExplicitZeroCapAndABadBody(t *testing.T) {
	svc := &fakeAutopilot{}
	if w := doJSON(autopilotRouter(svc), http.MethodPost, "/sessions/x/autopilot", `{"task":"t","maxIterations":0}`); w.Code != http.StatusBadRequest {
		t.Fatalf("zero cap status = %d: %s", w.Code, w.Body.String())
	}
	if w := doJSON(autopilotRouter(svc), http.MethodPost, "/sessions/x/autopilot", `not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body status = %d", w.Code)
	}
	if len(svc.calls) != 0 {
		t.Fatalf("a refused request reached the service: %v", svc.calls)
	}
}

// The service decides who is calling from the session the CLI sends; the
// controller must hand it that, untouched, on every route.
func TestAutopilotRoutesCarryTheCallingSessionToTheService(t *testing.T) {
	svc := &fakeAutopilot{}
	h := autopilotRouter(svc)
	doJSON(h, http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/done", `{"evidence":"go test ok"}`, caller.Header, "mer-orchestrator-1")
	doJSON(h, http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/stop", ``)
	if len(svc.callers) != 2 || svc.callers[0] != "mer-orchestrator-1" || svc.callers[1] != "" {
		t.Fatalf("callers = %q, want the agent's session and then none", svc.callers)
	}
}

func TestAutopilotForbiddenAndConflictKeepTheirStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		status int
	}{
		{"an agent starting", http.MethodPost, "/sessions/x/autopilot", `{"task":"t"}`, apierr.Forbidden("AUTOPILOT_USER_ONLY", "no"), http.StatusForbidden},
		{"an agent stopping", http.MethodPost, "/sessions/x/autopilot/stop", ``, apierr.Forbidden("AUTOPILOT_USER_ONLY", "no"), http.StatusForbidden},
		{"an agent stopping all", http.MethodPost, "/autopilot/stop", ``, apierr.Forbidden("AUTOPILOT_USER_ONLY", "no"), http.StatusForbidden},
		{"the user declaring done", http.MethodPost, "/sessions/x/autopilot/done", `{"evidence":"e"}`, apierr.Forbidden("AUTOPILOT_AGENT_ONLY", "no"), http.StatusForbidden},
		{"already on", http.MethodPost, "/sessions/x/autopilot", `{"task":"t"}`, apierr.Conflict("AUTOPILOT_ALREADY_ON", "on", nil), http.StatusConflict},
		{"not on", http.MethodPost, "/sessions/x/autopilot/decisions", `{"question":"q","choice":"c","why":"w"}`, apierr.Conflict("AUTOPILOT_NOT_ON", "off", nil), http.StatusConflict},
		{"a hard limit", http.MethodPost, "/sessions/x/autopilot/decisions", `{"question":"q","choice":"c","why":"w"}`, apierr.Invalid("AUTOPILOT_HARD_LIMIT", "limit", nil), http.StatusBadRequest},
		{"unknown item", http.MethodPost, "/autopilot/items/nope/resolve", ``, apierr.NotFound("AUTOPILOT_ITEM_NOT_FOUND", "no"), http.StatusNotFound},
		{"unreachable session", http.MethodPost, "/sessions/x/autopilot/resume", ``, apierr.Unavailable("AUTOPILOT_RESUME_FAILED", "down"), http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(autopilotRouter(&fakeAutopilot{err: tc.err}), tc.method, tc.path, tc.body)
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.status, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.err.(*apierr.Error).Code) {
				t.Fatalf("body %s must carry the error code", w.Body.String())
			}
		})
	}
}

func TestAutopilotStopRoutes(t *testing.T) {
	svc := &fakeAutopilot{stopped: true, all: 3}
	h := autopilotRouter(svc)
	var one StopAutopilotResponse
	if w := doJSON(h, http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/stop", ``); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &one) != nil || !one.Stopped {
		t.Fatalf("stop: %d %s", w.Code, w.Body.String())
	}
	var all StopAllAutopilotResponse
	if w := doJSON(h, http.MethodPost, "/autopilot/stop", ``); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &all) != nil || all.Stopped != 3 {
		t.Fatalf("stop all: %d %s", w.Code, w.Body.String())
	}
}

func TestAutopilotAgentRoutes(t *testing.T) {
	svc := &fakeAutopilot{
		run:   sampleRun(),
		event: domain.AutopilotEvent{ID: "ape_9", RunID: "ap_1", SessionID: "mer-orchestrator-1", Kind: domain.AutopilotEventParked, Summary: "Buy minutes", CreatedAt: apTime},
		done:  autopilot.DoneResult{Accepted: false, Failures: []string{"PR #7 has no completed AO review"}, Run: sampleRun()},
	}
	h := autopilotRouter(svc)

	var done AutopilotDoneResponse
	w := doJSON(h, http.MethodPost, "/sessions/s/autopilot/done", `{"evidence":"tests ok","ciCannotRun":"billing"}`)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &done) != nil || done.Accepted || len(done.Failures) != 1 || done.Gaps == nil {
		t.Fatalf("done: %d %s", w.Code, w.Body.String())
	}
	if svc.calls[len(svc.calls)-1] != "done s|tests ok|billing" {
		t.Fatalf("calls = %v", svc.calls)
	}

	var park AutopilotParkResponse
	svc.stopped = true
	w = doJSON(h, http.MethodPost, "/sessions/s/autopilot/parked", `{"limit":"money","item":"Buy minutes","why":"Ran out"}`)
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &park) != nil || park.Event.ID != "ape_9" || !park.AlreadyParked {
		t.Fatalf("park: %d %s", w.Code, w.Body.String())
	}
	if svc.calls[len(svc.calls)-1] != "park s|money|Buy minutes|Ran out" {
		t.Fatalf("calls = %v", svc.calls)
	}

	var run AutopilotRunResponse
	if w := doJSON(h, http.MethodPost, "/sessions/s/autopilot/criteria", `{"criteria":"Done when green"}`); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &run) != nil || run.ID != "ap_1" {
		t.Fatalf("criteria: %d %s", w.Code, w.Body.String())
	}
	var dec AutopilotDecideResponse
	if w := doJSON(h, http.MethodPost, "/sessions/s/autopilot/decisions", `{"question":"q","choice":"c","why":"w"}`); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &dec) != nil || dec.Event.ID != "ape_9" {
		t.Fatalf("decide: %d %s", w.Code, w.Body.String())
	}
	var item ResolveAutopilotItemResponse
	if w := doJSON(h, http.MethodPost, "/autopilot/items/ape_9/resolve", ``); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &item) != nil || item.Event.ID != "ape_9" {
		t.Fatalf("resolve: %d %s", w.Code, w.Body.String())
	}
}

func TestAutopilotListAndReportShape(t *testing.T) {
	run := sampleRun()
	run.EndedAt = apTime.Add(time.Hour)
	svc := &fakeAutopilot{run: run, view: autopilot.View{
		Run: &run,
		Report: &autopilot.Report{
			RunID: "ap_1", Task: "Ship it", State: domain.AutopilotDone, EndReason: domain.AutopilotEndGoalDone, Iterations: 4,
			StartedAt: apTime, EndedAt: apTime.Add(time.Hour),
			MergedPRs: []autopilot.MergedPR{{URL: "https://github.com/o/r/pull/4", Title: "PR #4 merged", At: apTime}},
			Gaps:      []string{"CI could not run: billing"},
		},
	}}
	h := autopilotRouter(svc)

	var list ListAutopilotResponse
	if w := doJSON(h, http.MethodGet, "/autopilot", ``); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Runs) != 1 || list.Runs[0].EndedAt == nil {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var view AutopilotViewResponse
	if w := doJSON(h, http.MethodGet, "/sessions/s/autopilot", ``); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("view: %d %s", w.Code, w.Body.String())
	}
	rep := view.Report
	if rep == nil || rep.State != "done" || rep.EndReason != "goal_done" || len(rep.MergedPRs) != 1 || rep.MergedPRs[0].URL != "https://github.com/o/r/pull/4" ||
		rep.Decisions == nil || rep.Parked == nil || len(rep.Gaps) != 1 {
		t.Fatalf("report = %+v", rep)
	}

	// A session that never had a run reads as an empty view, not an error.
	empty := doJSON(autopilotRouter(&fakeAutopilot{}), http.MethodGet, "/sessions/s/autopilot", ``)
	if empty.Code != http.StatusOK || strings.Contains(empty.Body.String(), `"run"`) {
		t.Fatalf("empty view: %d %s", empty.Code, empty.Body.String())
	}
}

func TestAutopilotRoutesAreNotImplementedWithoutAService(t *testing.T) {
	r := chi.NewRouter()
	(&AutopilotController{}).Register(r)
	for _, path := range []string{"/autopilot", "/sessions/x/autopilot"} {
		if w := doJSON(r, http.MethodGet, path, ``); w.Code != http.StatusNotImplemented {
			t.Errorf("GET %s = %d, want 501", path, w.Code)
		}
	}
}

// autopilotSettingsStub is the daemon's settings service as far as the settings
// routes read it: an empty snapshot and no chat harnesses.
type autopilotSettingsStub struct{ SettingsService }

func (*autopilotSettingsStub) Get(context.Context) (settingssvc.Snapshot, error) {
	return settingssvc.Snapshot{}, nil
}
func (*autopilotSettingsStub) ChatHarnesses([]domain.AgentHarness) []domain.AgentHarness { return nil }
func (*autopilotSettingsStub) Offering() settingssvc.Offering                            { return settingssvc.Offering{} }

type fakeAutopilotSettings struct{ n int }

func (f *fakeAutopilotSettings) MaxIterations(context.Context) (int, error) { return f.n, nil }
func (f *fakeAutopilotSettings) SetMaxIterations(_ context.Context, n int) error {
	f.n = n
	return nil
}

func TestSettingsReportAndChangeTheAutopilotCap(t *testing.T) {
	settings := &fakeAutopilotSettings{n: 30}
	r := chi.NewRouter()
	(&SettingsController{Svc: &autopilotSettingsStub{}, Autopilot: settings}).Register(r)

	var got SettingsResponse
	if w := doJSON(r, http.MethodGet, "/settings", ``); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.AutopilotMaxIterations != 30 {
		t.Fatalf("get: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(r, http.MethodPatch, "/settings/autopilot", `{"maxIterations":12}`); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.AutopilotMaxIterations != 12 || settings.n != 12 {
		t.Fatalf("patch: %d %s (n=%d)", w.Code, w.Body.String(), settings.n)
	}
	for _, body := range []string{`{}`, `{"maxIterations":0}`, `{"maxIterations":501}`} {
		if w := doJSON(r, http.MethodPatch, "/settings/autopilot", body); w.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", body, w.Code)
		}
	}
	if settings.n != 12 {
		t.Fatalf("a refused change moved the cap to %d", settings.n)
	}

	// Without the Autopilot preference the default is reported and cannot be set.
	bare := chi.NewRouter()
	(&SettingsController{Svc: &autopilotSettingsStub{}}).Register(bare)
	if w := doJSON(bare, http.MethodGet, "/settings", ``); json.Unmarshal(w.Body.Bytes(), &got) != nil || got.AutopilotMaxIterations != domain.AutopilotDefaultMaxIterations {
		t.Fatalf("bare get: %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(bare, http.MethodPatch, "/settings/autopilot", `{"maxIterations":5}`); w.Code != http.StatusNotImplemented {
		t.Fatalf("bare patch = %d, want 501", w.Code)
	}
}

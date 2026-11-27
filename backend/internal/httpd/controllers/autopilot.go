package controllers

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/autopilot"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// AutopilotService is the controller-facing contract for Autopilot task runs.
type AutopilotService interface {
	Start(ctx context.Context, id domain.SessionID, task string, maxIterations int) (autopilot.View, error)
	View(ctx context.Context, id domain.SessionID) (autopilot.View, error)
	List(ctx context.Context) ([]domain.AutopilotRun, error)
	Stop(ctx context.Context, id domain.SessionID) (bool, error)
	StopAll(ctx context.Context) (int, error)
	Resume(ctx context.Context, id domain.SessionID) (autopilot.View, error)
	SetCriteria(ctx context.Context, id domain.SessionID, criteria string) (domain.AutopilotRun, error)
	Decide(ctx context.Context, id domain.SessionID, question, choice, why string) (domain.AutopilotEvent, error)
	Park(ctx context.Context, id domain.SessionID, limit domain.HardLimit, item, why string) (domain.AutopilotEvent, bool, error)
	Done(ctx context.Context, id domain.SessionID, evidence, ciCannotRun string) (autopilot.DoneResult, error)
	ResolveParked(ctx context.Context, eventID string) (domain.AutopilotEvent, error)
	UndoDecision(ctx context.Context, eventID string) (domain.AutopilotEvent, error)
}

// AutopilotController owns the routes that start, stop and read Autopilot runs
// (the user's) and the ones the agent doing the task calls.
//
// Which side a call is on is decided by the service from the calling session the
// CLI sends: the user's calls carry none, the agent's carry its own. That keeps an
// agent from switching Autopilot on or off for itself through the CLI; it is a
// guardrail for the CLI path, like every other caller-gated route, not a boundary
// against a process that crafts raw HTTP requests.
type AutopilotController struct {
	Svc AutopilotService
}

// Register mounts the routes.
func (c *AutopilotController) Register(r chi.Router) {
	r.Get("/autopilot", c.list)
	r.Post("/autopilot/stop", c.stopAll)
	r.Post("/autopilot/items/{eventId}/resolve", c.resolveItem)
	r.Post("/autopilot/items/{eventId}/undo", c.undoDecision)
	r.Get("/sessions/{sessionId}/autopilot", c.view)
	r.Post("/sessions/{sessionId}/autopilot", c.start)
	r.Post("/sessions/{sessionId}/autopilot/stop", c.stop)
	r.Post("/sessions/{sessionId}/autopilot/resume", c.resume)
	r.Post("/sessions/{sessionId}/autopilot/criteria", c.criteria)
	r.Post("/sessions/{sessionId}/autopilot/done", c.done)
	r.Post("/sessions/{sessionId}/autopilot/decisions", c.decide)
	r.Post("/sessions/{sessionId}/autopilot/advice", c.advice)
	r.Post("/sessions/{sessionId}/autopilot/parked", c.park)
}

func (c *AutopilotController) ready(w http.ResponseWriter, r *http.Request, method, path string) bool {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, method, path)
		return false
	}
	return true
}

func sessionParam(r *http.Request) domain.SessionID {
	return domain.SessionID(chi.URLParam(r, "sessionId"))
}

func (c *AutopilotController) list(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "GET", "/api/v1/autopilot") {
		return
	}
	runs, err := c.Svc.List(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]AutopilotRunResponse, 0, len(runs))
	for _, run := range runs {
		out = append(out, autopilotRunResponse(run, 0))
	}
	envelope.WriteJSON(w, http.StatusOK, ListAutopilotResponse{Runs: out})
}

func (c *AutopilotController) stopAll(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/autopilot/stop") {
		return
	}
	n, err := c.Svc.StopAll(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StopAllAutopilotResponse{Stopped: n})
}

func (c *AutopilotController) resolveItem(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/autopilot/items/{eventId}/resolve") {
		return
	}
	ev, err := c.Svc.ResolveParked(r.Context(), chi.URLParam(r, "eventId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResolveAutopilotItemResponse{Event: autopilotEventResponse(ev)})
}

func (c *AutopilotController) undoDecision(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/autopilot/items/{eventId}/undo") {
		return
	}
	ev, err := c.Svc.UndoDecision(r.Context(), chi.URLParam(r, "eventId"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, ResolveAutopilotItemResponse{Event: autopilotEventResponse(ev)})
}

func (c *AutopilotController) view(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "GET", "/api/v1/sessions/{sessionId}/autopilot") {
		return
	}
	v, err := c.Svc.View(r.Context(), sessionParam(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, autopilotViewResponse(v))
}

func (c *AutopilotController) start(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot") {
		return
	}
	var req StartAutopilotRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	maxIterations := 0
	if req.MaxIterations != nil {
		maxIterations = *req.MaxIterations
		if maxIterations == 0 {
			// An explicit 0 is not "use the default": omit the field for that.
			envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation",
				"AUTOPILOT_MAX_ITERATIONS_INVALID", "maxIterations must be at least 1; omit it to use the default", nil)
			return
		}
	}
	v, err := c.Svc.Start(r.Context(), sessionParam(r), req.Task, maxIterations)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, autopilotViewResponse(v))
}

func (c *AutopilotController) stop(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/stop") {
		return
	}
	stopped, err := c.Svc.Stop(r.Context(), sessionParam(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StopAutopilotResponse{Stopped: stopped})
}

func (c *AutopilotController) resume(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/resume") {
		return
	}
	v, err := c.Svc.Resume(r.Context(), sessionParam(r))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, autopilotViewResponse(v))
}

func (c *AutopilotController) criteria(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/criteria") {
		return
	}
	var req AutopilotCriteriaRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	run, err := c.Svc.SetCriteria(r.Context(), sessionParam(r), req.Criteria)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, autopilotRunResponse(run, 0))
}

func (c *AutopilotController) done(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/done") {
		return
	}
	var req AutopilotDoneRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	res, err := c.Svc.Done(r.Context(), sessionParam(r), req.Evidence, req.CICannotRun)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutopilotDoneResponse{
		Accepted: res.Accepted, Failures: nonNil(res.Failures), Gaps: nonNil(res.Gaps),
		Run: autopilotRunResponse(res.Run, 0),
	})
}

func (c *AutopilotController) decide(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/decisions") {
		return
	}
	var req AutopilotDecideRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	var ev domain.AutopilotEvent
	var err error
	if ext, ok := c.Svc.(autopilotAdvisorService); ok && (len(req.BasedOn) > 0 || req.Confidence != "") {
		ev, err = ext.DecideWithBasis(r.Context(), sessionParam(r), req.Question, req.Choice, req.Why,
			autopilot.Basis{PairIDs: req.BasedOn, Confidence: req.Confidence})
	} else {
		ev, err = c.Svc.Decide(r.Context(), sessionParam(r), req.Question, req.Choice, req.Why)
	}
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutopilotDecideResponse{Event: autopilotEventResponse(ev)})
}

func (c *AutopilotController) advice(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/advice") {
		return
	}
	ext, ok := c.Svc.(autopilotAdvisorService)
	if !ok {
		apispec.NotImplemented(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/advice")
		return
	}
	var req AutopilotAdviceRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	text, err := ext.Advise(r.Context(), sessionParam(r), req.Question, req.Options)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutopilotAdviceResponse{Advice: text})
}

func (c *AutopilotController) park(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/sessions/{sessionId}/autopilot/parked") {
		return
	}
	var req AutopilotParkRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	ev, already, err := c.Svc.Park(r.Context(), sessionParam(r), domain.HardLimit(req.Limit), req.Item, req.Why)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutopilotParkResponse{Event: autopilotEventResponse(ev), AlreadyParked: already})
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func autopilotRunResponse(run domain.AutopilotRun, openParked int) AutopilotRunResponse {
	return AutopilotRunResponse{
		ID: run.ID, SessionID: string(run.SessionID), ProjectID: string(run.ProjectID),
		Task: run.Task, Criteria: run.Criteria, State: string(run.State), EndReason: string(run.EndReason),
		Iteration: run.Iteration, MaxIterations: run.MaxIterations, StagnantIterations: run.Stagnant,
		LastGap: run.LastGap, Evidence: run.Evidence, CIGap: run.CIGap,
		PermissionsAllowed: run.PermissionsAllowed, PermissionsDenied: run.PermissionsDenied,
		StartedAt: run.StartedAt, UpdatedAt: run.UpdatedAt,
		LastNudgeAt: timePtr(run.LastNudgeAt), EndedAt: timePtr(run.EndedAt),
		OpenParkedItemCount: openParked,
	}
}

func autopilotEventResponse(ev domain.AutopilotEvent) AutopilotEventResponse {
	out := AutopilotEventResponse{
		ID: ev.ID, RunID: ev.RunID, SessionID: string(ev.SessionID), Kind: string(ev.Kind), Iteration: ev.Iteration,
		Summary: ev.Summary, Detail: ev.Detail, CreatedAt: ev.CreatedAt, ResolvedAt: timePtr(ev.ResolvedAt),
		BasedOn: []string{},
	}
	if ev.Kind == domain.AutopilotEventDecision {
		out.Detail, out.BasedOn, out.Confidence = splitDecisionBasis(ev.Detail)
	}
	return out
}

// splitDecisionBasis separates the "why" of a decision from the lines that say
// which of the owner's past choices it followed and how confident it was.
func splitDecisionBasis(detail string) (why string, basedOn []string, confidence string) {
	basedOn = []string{}
	var rest []string
	for _, line := range strings.Split(detail, "\n") {
		switch {
		case strings.HasPrefix(line, "Based on your past choice: "):
			basedOn = append(basedOn, strings.TrimPrefix(line, "Based on your past choice: "))
		case strings.HasPrefix(line, "Confidence: low"):
			confidence = "low"
		case strings.HasPrefix(line, "Confidence: medium"):
			confidence = "medium"
		case strings.HasPrefix(line, "Confidence: high"):
			confidence = "high"
		default:
			rest = append(rest, line)
		}
	}
	return strings.Join(rest, "\n"), basedOn, confidence
}

func autopilotEventResponses(events []domain.AutopilotEvent) []AutopilotEventResponse {
	out := make([]AutopilotEventResponse, 0, len(events))
	for _, ev := range events {
		out = append(out, autopilotEventResponse(ev))
	}
	return out
}

func autopilotViewResponse(v autopilot.View) AutopilotViewResponse {
	out := AutopilotViewResponse{
		Events: autopilotEventResponses(v.Events), Parked: autopilotEventResponses(v.Parked), Warnings: nonNil(v.Warnings),
	}
	if v.Run != nil {
		run := autopilotRunResponse(*v.Run, len(v.Parked))
		out.Run = &run
	}
	if v.Report != nil {
		rep := v.Report
		merged := make([]AutopilotMergedPRResponse, 0, len(rep.MergedPRs))
		for _, pr := range rep.MergedPRs {
			merged = append(merged, AutopilotMergedPRResponse{URL: pr.URL, Title: pr.Title, At: pr.At})
		}
		out.Report = &AutopilotReportResponse{
			RunID: rep.RunID, Task: rep.Task, State: string(rep.State), EndReason: string(rep.EndReason),
			Iterations: rep.Iterations, StartedAt: rep.StartedAt, EndedAt: timePtr(rep.EndedAt),
			MergedPRs: merged, Decisions: autopilotEventResponses(rep.Decisions), Parked: autopilotEventResponses(rep.Parked),
			Gaps: nonNil(rep.Gaps), PermissionsAllowed: rep.PermissionsAllowed, PermissionsDenied: rep.PermissionsDenied,
		}
	}
	return out
}

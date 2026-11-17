package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/jira/jiratest"
	jirasvc "github.com/aoagents/agent-orchestrator/backend/internal/service/jira"
)

type jiraProjects struct{}

func (jiraProjects) ProjectExists(_ context.Context, id string) (bool, error) {
	return id == "yoobi-app", nil
}

type jiraOrchestrators struct {
	active string
	sent   []string
}

func (o *jiraOrchestrators) ActiveOrchestrator(context.Context, string) (string, error) {
	return o.active, nil
}
func (o *jiraOrchestrators) SpawnOrchestrator(context.Context, string) (string, error) {
	o.active = "orch-new"
	return o.active, nil
}
func (o *jiraOrchestrators) SendTask(_ context.Context, _ string, text string) error {
	o.sent = append(o.sent, text)
	return nil
}

// jiraRig runs the real Jira service behind the controller against a fake site,
// and keeps every response body so a test can check none carries the token.
type jiraRig struct {
	t      *testing.T
	router chi.Router
	site   *jiratest.Server
	orch   *jiraOrchestrators
	bodies []string
}

func newJiraRig(t *testing.T) *jiraRig {
	t.Helper()
	site := jiratest.New()
	t.Cleanup(site.Close)
	site.Columns = []jiratest.Column{{Name: "To Do", StatusIDs: []string{"1"}}, {Name: "Done", StatusIDs: []string{"5"}}}
	site.Issues = []jiratest.Issue{
		{Key: "YIA-10", Summary: "Add CSV export", Description: "Users want CSV export.", StatusID: "1", StatusName: "To Do", StatusCategory: "new", Type: "Story", Priority: "High", Updated: "2026-11-10T09:30:00.000+0100"},
	}
	orch := &jiraOrchestrators{}
	svc := jirasvc.New(jirasvc.Deps{DataDir: t.TempDir(), Projects: jiraProjects{}, Orchestrators: orch})
	router := chi.NewRouter()
	(&JiraController{Svc: svc}).Register(router)
	return &jiraRig{t: t, router: router, site: site, orch: orch}
}

func (r *jiraRig) do(method, path string, body any) *httptest.ResponseRecorder {
	r.t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	r.bodies = append(r.bodies, rec.Body.String())
	return rec
}

func (r *jiraRig) connectBody() map[string]any {
	return map[string]any{"siteUrl": r.site.URL, "email": r.site.Email, "apiToken": r.site.Token, "projectKey": "YIA"}
}

func decodeCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Code
}

func TestJiraRoutesConnectBoardStartDisconnect(t *testing.T) {
	rig := newJiraRig(t)

	if rec := rig.do(http.MethodGet, "/projects/yoobi-app/jira", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"connected":false}` {
		t.Fatalf("status before connect: %d %s", rec.Code, rec.Body)
	}
	if rec := rig.do(http.MethodPost, "/projects/yoobi-app/jira/test", rig.connectBody()); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ticketCount":1`) {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if rec := rig.do(http.MethodPut, "/projects/yoobi-app/jira", rig.connectBody()); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"connected":true`) {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}

	rec := rig.do(http.MethodGet, "/projects/yoobi-app/jira/board", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("board: %d %s", rec.Code, rec.Body)
	}
	var board jirasvc.Board
	if err := json.Unmarshal(rec.Body.Bytes(), &board); err != nil || len(board.Columns) != 2 || board.Columns[0].Tickets[0].Key != "YIA-10" {
		t.Fatalf("board = %+v, %v", board, err)
	}

	rec = rig.do(http.MethodPost, "/projects/yoobi-app/jira/tickets/YIA-10/start", nil)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"orchestratorId":"orch-new"`) {
		t.Fatalf("start: %d %s", rec.Code, rec.Body)
	}
	if len(rig.orch.sent) != 1 || !strings.Contains(rig.orch.sent[0], "Jira ticket YIA-10: Add CSV export") {
		t.Fatalf("orchestrator received %q", rig.orch.sent)
	}

	if rec := rig.do(http.MethodDelete, "/projects/yoobi-app/jira", nil); rec.Code != http.StatusOK {
		t.Fatalf("disconnect: %d %s", rec.Code, rec.Body)
	}
	if rec := rig.do(http.MethodGet, "/projects/yoobi-app/jira/board", nil); rec.Code != http.StatusConflict || decodeCode(t, rec) != jirasvc.CodeNotConnected {
		t.Fatalf("board after disconnect: %d %s", rec.Code, rec.Body)
	}

	for _, body := range rig.bodies {
		if strings.Contains(body, rig.site.Token) || strings.Contains(body, "apiToken") {
			t.Fatalf("a response carries the token or an apiToken field: %s", body)
		}
	}
}

func TestJiraRejectedLoginIs422NotA401(t *testing.T) {
	rig := newJiraRig(t)
	body := rig.connectBody()
	body["apiToken"] = "wrong-token-value"
	rec := rig.do(http.MethodPut, "/projects/yoobi-app/jira", body)
	if rec.Code != http.StatusUnprocessableEntity || decodeCode(t, rec) != jirasvc.CodeAuthFailed {
		t.Fatalf("got %d %s; a Jira login failure must not look like a daemon 401", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "wrong-token-value") {
		t.Fatalf("error leaks the token: %s", rec.Body)
	}
}

func TestJiraOfflineIs503WithAReason(t *testing.T) {
	rig := newJiraRig(t)
	body := rig.connectBody()
	rig.site.Close()
	rec := rig.do(http.MethodPost, "/projects/yoobi-app/jira/test", body)
	if rec.Code != http.StatusServiceUnavailable || decodeCode(t, rec) != jirasvc.CodeUnreachable {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestJiraBadQueryAndBadInputAndBadJSON(t *testing.T) {
	rig := newJiraRig(t)
	body := rig.connectBody()
	body["jql"] = "BROKEN ==="
	if rec := rig.do(http.MethodPost, "/projects/yoobi-app/jira/test", body); rec.Code != http.StatusBadRequest || decodeCode(t, rec) != jirasvc.CodeBadQuery || !strings.Contains(rec.Body.String(), "unknown field") {
		t.Fatalf("bad query: %d %s", rec.Code, rec.Body)
	}
	body = rig.connectBody()
	body["siteUrl"] = "http://yoobi.atlassian.net"
	if rec := rig.do(http.MethodPut, "/projects/yoobi-app/jira", body); rec.Code != http.StatusBadRequest || decodeCode(t, rec) != jirasvc.CodeInvalidInput {
		t.Fatalf("plain http site: %d %s", rec.Code, rec.Body)
	}
	rec := httptest.NewRecorder()
	rig.router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/projects/yoobi-app/jira", strings.NewReader("{nope")))
	if rec.Code != http.StatusBadRequest || decodeCode(t, rec) != "INVALID_JSON" {
		t.Fatalf("bad json: %d %s", rec.Code, rec.Body)
	}
	if rec := rig.do(http.MethodGet, "/projects/other/jira", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown project: %d %s", rec.Code, rec.Body)
	}
	rig.do(http.MethodPut, "/projects/yoobi-app/jira", rig.connectBody())
	if rec := rig.do(http.MethodPost, "/projects/yoobi-app/jira/tickets/not-a-key/start", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad ticket key: %d %s", rec.Code, rec.Body)
	}
	if rec := rig.do(http.MethodPost, "/projects/yoobi-app/jira/tickets/YIA-999/start", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing ticket: %d %s", rec.Code, rec.Body)
	}
}

func TestJiraRoutesAnswer501WithoutAService(t *testing.T) {
	router := chi.NewRouter()
	(&JiraController{}).Register(router)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/projects/p/jira"},
		{http.MethodPut, "/projects/p/jira"},
		{http.MethodDelete, "/projects/p/jira"},
		{http.MethodPost, "/projects/p/jira/test"},
		{http.MethodGet, "/projects/p/jira/board"},
		{http.MethodPost, "/projects/p/jira/tickets/YIA-1/start"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, strings.NewReader("{}")))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", route.method, route.path, rec.Code)
		}
	}
}

// The saved token only ever goes back to the site and account it was saved for.
func TestJiraBlankTokenIsNotSentToAnotherSite(t *testing.T) {
	rig := newJiraRig(t)
	if rec := rig.do(http.MethodPut, "/projects/yoobi-app/jira", rig.connectBody()); rec.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
	other := jiratest.New()
	defer other.Close()

	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/projects/yoobi-app/jira"},
		{http.MethodPost, "/projects/yoobi-app/jira/test"},
	} {
		rec := rig.do(route.method, route.path, map[string]any{"siteUrl": other.URL, "email": rig.site.Email, "projectKey": "YIA"})
		if rec.Code != http.StatusBadRequest || decodeCode(t, rec) != jirasvc.CodeInvalidInput {
			t.Fatalf("%s %s: %d %s", route.method, route.path, rec.Code, rec.Body)
		}
	}
	if reqs := other.Requests(); len(reqs) != 0 {
		t.Fatalf("the saved token reached another host: %+v", reqs)
	}
}

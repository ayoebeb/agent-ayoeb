package controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	jirasvc "github.com/aoagents/agent-orchestrator/backend/internal/service/jira"
)

// maxJiraBodyBytes bounds a connection request; a JQL query is the longest field.
const maxJiraBodyBytes = 32 << 10

// JiraService is the controller-facing contract for a project's Jira connection.
type JiraService interface {
	Status(ctx context.Context, projectID string) (jirasvc.Connection, error)
	Test(ctx context.Context, projectID string, in jirasvc.ConnectInput) (jirasvc.TestResult, error)
	Connect(ctx context.Context, projectID string, in jirasvc.ConnectInput) (jirasvc.Connection, error)
	Disconnect(ctx context.Context, projectID string) error
	Board(ctx context.Context, projectID string) (jirasvc.Board, error)
	Start(ctx context.Context, projectID, issueKey string) (jirasvc.StartResult, error)
}

// JiraController owns the per-project Jira routes. Jira credentials never
// travel back out: no response carries the API token.
type JiraController struct {
	Svc JiraService
}

// JiraIssueKeyParam is the {issueKey} path segment.
type JiraIssueKeyParam struct {
	IssueKey string `path:"issueKey" description:"Jira ticket key, for example YIA-123."`
}

// JiraConnectRequest is the body of PUT /projects/{id}/jira and
// POST /projects/{id}/jira/test.
type JiraConnectRequest struct {
	SiteURL string `json:"siteUrl" maxLength:"512" description:"Jira Cloud site, for example yourcompany.atlassian.net."`
	Email   string `json:"email" maxLength:"320" description:"Email address of the Atlassian account that owns the API token."`
	// APIToken is write-only. Leave it empty to keep the token already stored.
	APIToken   string `json:"apiToken,omitempty" maxLength:"2048" description:"Atlassian API token. Write-only: it is stored locally and never returned. Empty keeps the stored token."`
	ProjectKey string `json:"projectKey,omitempty" maxLength:"32" description:"Jira project key, for example YIA. Required unless jql is set."`
	BoardID    int    `json:"boardId,omitempty" description:"Jira board id. Omit to use the project's first board."`
	JQL        string `json:"jql,omitempty" maxLength:"4096" description:"Optional JQL that chooses the tickets. Overrides the project key's default query."`
}

// Register mounts the Jira routes on the supplied router.
func (c *JiraController) Register(r chi.Router) {
	r.Get("/projects/{id}/jira", c.status)
	r.Put("/projects/{id}/jira", c.connect)
	r.Delete("/projects/{id}/jira", c.disconnect)
	r.Post("/projects/{id}/jira/test", c.test)
	r.Get("/projects/{id}/jira/board", c.board)
	r.Post("/projects/{id}/jira/tickets/{issueKey}/start", c.start)
}

func (c *JiraController) status(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/projects/{id}/jira")
		return
	}
	out, err := c.Svc.Status(r.Context(), string(projectID(r)))
	if err != nil {
		writeJiraError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *JiraController) connect(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPut, "/api/v1/projects/{id}/jira")
		return
	}
	in, ok := decodeJiraRequest(w, r)
	if !ok {
		return
	}
	out, err := c.Svc.Connect(r.Context(), string(projectID(r)), in)
	if err != nil {
		writeJiraError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *JiraController) test(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/projects/{id}/jira/test")
		return
	}
	in, ok := decodeJiraRequest(w, r)
	if !ok {
		return
	}
	out, err := c.Svc.Test(r.Context(), string(projectID(r)), in)
	if err != nil {
		writeJiraError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *JiraController) disconnect(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodDelete, "/api/v1/projects/{id}/jira")
		return
	}
	if err := c.Svc.Disconnect(r.Context(), string(projectID(r))); err != nil {
		writeJiraError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (c *JiraController) board(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodGet, "/api/v1/projects/{id}/jira/board")
		return
	}
	out, err := c.Svc.Board(r.Context(), string(projectID(r)))
	if err != nil {
		writeJiraError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, out)
}

func (c *JiraController) start(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, http.MethodPost, "/api/v1/projects/{id}/jira/tickets/{issueKey}/start")
		return
	}
	out, err := c.Svc.Start(r.Context(), string(projectID(r)), chi.URLParam(r, "issueKey"))
	if err != nil {
		writeJiraError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, out)
}

func decodeJiraRequest(w http.ResponseWriter, r *http.Request) (jirasvc.ConnectInput, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJiraBodyBytes)
	var body JiraConnectRequest
	if err := decodeJSON(r, &body); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return jirasvc.ConnectInput{}, false
	}
	return jirasvc.ConnectInput{
		SiteURL: body.SiteURL, Email: body.Email, APIToken: body.APIToken,
		ProjectKey: body.ProjectKey, BoardID: body.BoardID, JQL: body.JQL,
	}, true
}

// writeJiraError renders a Jira failure. A rejected Jira login is a 422, never
// a 401: a 401 from the daemon means "your AO pairing is wrong" to the phone
// client, and this one is about the Jira account.
func writeJiraError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, jirasvc.ErrAuthFailed):
		envelope.WriteAPIError(w, r, http.StatusUnprocessableEntity, "unprocessable", jirasvc.CodeAuthFailed,
			"Jira rejected the email or API token. Check both and try again.", nil)
	case errors.Is(err, jirasvc.ErrUnreachable):
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", jirasvc.CodeUnreachable,
			"Could not reach Jira. Check the site address and your connection.", jiraErrorDetail(err, jirasvc.ErrUnreachable))
	case errors.Is(err, jirasvc.ErrBadQuery):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", jirasvc.CodeBadQuery,
			"Jira did not accept the query: "+jiraDetailText(err, jirasvc.ErrBadQuery), nil)
	case errors.Is(err, jirasvc.ErrUpstream):
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "bad_gateway", jirasvc.CodeUpstream,
			"Jira returned an unexpected error. Try again in a moment.", nil)
	default:
		envelope.WriteError(w, r, err)
	}
}

// jiraDetailText is the part of err after the sentinel's own text.
func jiraDetailText(err, sentinel error) string {
	text := strings.TrimPrefix(err.Error(), sentinel.Error())
	return strings.TrimSpace(strings.TrimPrefix(text, ":"))
}

func jiraErrorDetail(err, sentinel error) map[string]any {
	if reason := jiraDetailText(err, sentinel); reason != "" {
		return map[string]any{"reason": reason}
	}
	return nil
}

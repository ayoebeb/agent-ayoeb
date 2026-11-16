// Package jiratest is a fake Jira Cloud site for tests. It speaks just enough
// of the REST API for the jira client: sign-in, projects, boards, search and
// single-ticket reads. It needs no real credentials and no network beyond the
// loopback listener httptest opens.
package jiratest

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

// Issue is a ticket the fake serves.
type Issue struct {
	Key            string
	Summary        string
	Description    string // plain text; served as an ADF document
	AcceptanceText string // plain text served in the acceptance-criteria custom field
	StatusID       string
	StatusName     string
	StatusCategory string // new, indeterminate or done
	AssigneeID     string
	AssigneeName   string
	Type           string
	Priority       string
	Labels         []string
	Updated        string
}

// Column is a board column in the fake's board configuration.
type Column struct {
	Name      string
	StatusIDs []string
}

// Request is one request the fake received.
type Request struct {
	Method string
	Path   string
	Query  string
	Body   string
	Auth   string
}

// Server is the fake site. Configure the exported fields before the first
// request; they are read without locking afterwards.
type Server struct {
	*httptest.Server

	Email       string
	Token       string
	AccountID   string
	DisplayName string
	ProjectKey  string
	ProjectName string
	BoardID     int
	BoardName   string
	// Columns, when set, is served as the project's board. Empty serves a 404
	// for the board APIs, like a site without Jira Software.
	Columns []Column
	Issues  []Issue
	// ACField is the custom-field id for acceptance criteria; empty means the
	// site has no such field.
	ACField string
	// PageSize forces pagination: a search returns at most this many tickets
	// per page. Zero returns everything in one page.
	PageSize int
	// Status overrides every response with this HTTP status when non-zero, to
	// simulate outages and rate limits.
	Status int

	mu       sync.Mutex
	requests []Request
}

// New starts a fake site with an example account and project.
func New() *Server {
	s := &Server{
		Email: "dev@example.com", Token: "fake-api-token", AccountID: "acc-1", DisplayName: "Ada Example",
		ProjectKey: "YIA", ProjectName: "Yoobi", BoardID: 7, BoardName: "YIA board",
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// Requests returns a copy of every request received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Reset forgets the recorded requests.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = nil
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	bodyBytes, _ := io.ReadAll(r.Body)
	body := string(bodyBytes)
	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: body, Auth: r.Header.Get("Authorization")})
	s.mu.Unlock()

	if s.Status != 0 {
		writeJSON(w, s.Status, map[string]any{"errorMessages": []string{"forced status"}})
		return
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte(s.Email+":"+s.Token))
	if r.Header.Get("Authorization") != want {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Basic auth with API token required"})
		return
	}
	path := r.URL.Path
	switch {
	case path == "/rest/api/3/myself":
		writeJSON(w, 200, map[string]any{"accountId": s.AccountID, "displayName": s.DisplayName})
	case strings.HasPrefix(path, "/rest/api/3/project/"):
		if strings.TrimPrefix(path, "/rest/api/3/project/") != s.ProjectKey {
			writeJSON(w, 404, map[string]any{"errorMessages": []string{"No project could be found with key"}})
			return
		}
		writeJSON(w, 200, map[string]any{"key": s.ProjectKey, "name": s.ProjectName})
	case path == "/rest/agile/1.0/board":
		if len(s.Columns) == 0 {
			writeJSON(w, 404, map[string]any{"errorMessages": []string{"no software"}})
			return
		}
		writeJSON(w, 200, map[string]any{"values": []map[string]any{{"id": s.BoardID, "name": s.BoardName, "type": "kanban"}}})
	case strings.HasPrefix(path, "/rest/agile/1.0/board/") && strings.HasSuffix(path, "/configuration"):
		if len(s.Columns) == 0 || path != "/rest/agile/1.0/board/"+strconv.Itoa(s.BoardID)+"/configuration" {
			writeJSON(w, 404, map[string]any{"errorMessages": []string{"board not found"}})
			return
		}
		cols := make([]map[string]any, 0, len(s.Columns))
		for _, c := range s.Columns {
			statuses := make([]map[string]any, 0, len(c.StatusIDs))
			for _, id := range c.StatusIDs {
				statuses = append(statuses, map[string]any{"id": id})
			}
			cols = append(cols, map[string]any{"name": c.Name, "statuses": statuses})
		}
		writeJSON(w, 200, map[string]any{"name": s.BoardName, "columnConfig": map[string]any{"columns": cols}})
	case path == "/rest/api/3/field":
		fields := []map[string]any{{"id": "summary", "name": "Summary", "custom": false}}
		if s.ACField != "" {
			fields = append(fields, map[string]any{"id": s.ACField, "name": "Acceptance Criteria", "custom": true})
		}
		writeJSON(w, 200, fields)
	case path == "/rest/api/3/search/jql" && r.Method == http.MethodPost:
		s.search(w, body)
	case strings.HasPrefix(path, "/rest/api/3/issue/"):
		key := strings.TrimPrefix(path, "/rest/api/3/issue/")
		for _, issue := range s.Issues {
			if issue.Key == key {
				writeJSON(w, 200, s.wire(issue))
				return
			}
		}
		writeJSON(w, 404, map[string]any{"errorMessages": []string{"Issue does not exist"}})
	default:
		writeJSON(w, 404, map[string]any{"errorMessages": []string{"unknown route " + path}})
	}
}

func (s *Server) search(w http.ResponseWriter, raw string) {
	var in struct {
		JQL           string `json:"jql"`
		MaxResults    int    `json:"maxResults"`
		NextPageToken string `json:"nextPageToken"`
	}
	_ = json.Unmarshal([]byte(raw), &in)
	if strings.Contains(in.JQL, "BROKEN") {
		writeJSON(w, 400, map[string]any{"errorMessages": []string{"Error in the JQL Query: unknown field"}})
		return
	}
	start := 0
	if in.NextPageToken != "" {
		start, _ = strconv.Atoi(in.NextPageToken)
	}
	size := len(s.Issues)
	if s.PageSize > 0 && s.PageSize < size {
		size = s.PageSize
	}
	if in.MaxResults > 0 && in.MaxResults < size {
		size = in.MaxResults
	}
	end := start + size
	if end > len(s.Issues) {
		end = len(s.Issues)
	}
	issues := make([]map[string]any, 0, end-start)
	for _, issue := range s.Issues[start:end] {
		issues = append(issues, s.wire(issue))
	}
	out := map[string]any{"issues": issues, "isLast": end >= len(s.Issues)}
	if end < len(s.Issues) {
		out["nextPageToken"] = strconv.Itoa(end)
	}
	writeJSON(w, 200, out)
}

// wire renders an Issue the way Jira Cloud does.
func (s *Server) wire(issue Issue) map[string]any {
	fields := map[string]any{
		"summary":     issue.Summary,
		"description": adf(issue.Description),
		"status": map[string]any{
			"id": issue.StatusID, "name": issue.StatusName,
			"statusCategory": map[string]any{"key": issue.StatusCategory},
		},
		"issuetype": map[string]any{"name": issue.Type},
		"priority":  map[string]any{"name": issue.Priority},
		"labels":    issue.Labels,
		"updated":   issue.Updated,
	}
	if issue.AssigneeID != "" {
		fields["assignee"] = map[string]any{"accountId": issue.AssigneeID, "displayName": issue.AssigneeName}
	} else {
		fields["assignee"] = nil
	}
	if s.ACField != "" && issue.AcceptanceText != "" {
		fields[s.ACField] = adf(issue.AcceptanceText)
	}
	return map[string]any{"key": issue.Key, "fields": fields}
}

// adf wraps plain text as an Atlassian Document Format document, one paragraph
// per line; an empty string is a null description.
func adf(text string) any {
	if text == "" {
		return nil
	}
	var content []map[string]any
	for _, line := range strings.Split(text, "\n") {
		para := map[string]any{"type": "paragraph"}
		if line != "" {
			para["content"] = []map[string]any{{"type": "text", "text": line}}
		}
		content = append(content, para)
	}
	return map[string]any{"type": "doc", "version": 1, "content": content}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

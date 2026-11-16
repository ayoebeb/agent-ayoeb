// Package jira is a small read-only client for the Jira Cloud REST API. It
// signs in with an account email and API token (HTTP Basic), lists the tickets
// a JQL query matches, reads one ticket, and discovers the board columns a
// project uses. It never writes to Jira: no transitions, no comments.
package jira

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Sentinel errors the service and controller translate into stable API codes.
var (
	// ErrAuth means Jira rejected the email/API token (HTTP 401 or 403).
	ErrAuth = errors.New("jira rejected the credentials")
	// ErrNotFound means the project, board or ticket does not exist for this account.
	ErrNotFound = errors.New("jira resource not found")
	// ErrUnreachable means Jira could not be reached (DNS, TLS, timeout, offline).
	ErrUnreachable = errors.New("jira is unreachable")
	// ErrBadQuery means Jira refused the JQL (HTTP 400).
	ErrBadQuery = errors.New("jira rejected the query")
	// ErrUpstream is any other non-success response from Jira.
	ErrUpstream = errors.New("jira returned an error")
)

const (
	defaultTimeout = 20 * time.Second
	// maxResponseBytes bounds one response so a hostile or broken server cannot
	// exhaust memory. Real search pages are far smaller.
	maxResponseBytes = 16 << 20
	// PageSize is how many tickets one search request asks for.
	PageSize = 100
)

// User is a Jira account as shown on a ticket.
type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

// Status is a ticket's workflow status. Category is Jira's coarse bucket:
// "new" (to do), "indeterminate" (in progress) or "done".
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// Issue is one ticket, flattened to plain text.
type Issue struct {
	Key                string
	Summary            string
	Description        string
	AcceptanceCriteria string
	Status             Status
	Assignee           *User
	IssueType          string
	Priority           string
	Labels             []string
	Updated            time.Time
}

// Column is one board column and the status ids that map to it.
type Column struct {
	Name      string
	StatusIDs []string
}

// Board is a Jira board's name and ordered columns.
type Board struct {
	ID      int
	Name    string
	Columns []Column
}

// Config selects the Jira site and the account the client signs in as.
type Config struct {
	SiteURL string
	Email   string
	Token   string
}

// Client talks to one Jira Cloud site.
type Client struct {
	base  string
	email string
	token string
	http  *http.Client
	// acField is the cached custom-field id that holds acceptance criteria, ""
	// when the site has none. acLoaded says the lookup ran; acMu guards both so
	// one client can serve concurrent requests.
	acMu     sync.Mutex
	acField  string
	acLoaded bool
}

// NormalizeSiteURL turns what a person types ("yoobi.atlassian.net",
// "https://yoobi.atlassian.net/jira/software/projects/YIA/boards/1") into the
// site root. Jira Cloud is https only; plain http is accepted for a loopback
// host so a local fake server can stand in during tests and demos.
func NormalizeSiteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("site URL is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("site URL is not a valid URL")
	}
	if u.User != nil {
		return "", errors.New("site URL must not contain credentials")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", errors.New("site URL must use https")
		}
	default:
		return "", errors.New("site URL must use https")
	}
	return u.Scheme + "://" + u.Host, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// New builds a client. cfg.SiteURL must already be normalized.
func New(cfg Config) *Client {
	return &Client{
		base:  strings.TrimRight(cfg.SiteURL, "/"),
		email: cfg.Email,
		token: cfg.Token,
		http: &http.Client{
			Timeout: defaultTimeout,
			// A redirect would carry the Authorization header somewhere the user
			// did not enter; Jira Cloud's API does not redirect.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// SiteURL is the normalized site root the client talks to.
func (c *Client) SiteURL() string { return c.base }

// BrowseURL is the link that opens a ticket in the browser.
func (c *Client) BrowseURL(key string) string { return c.base + "/browse/" + url.PathEscape(key) }

// Myself returns the account the credentials belong to. It is the cheapest
// call that proves the email and token are valid.
func (c *Client) Myself(ctx context.Context) (User, error) {
	var out struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/myself", nil, &out); err != nil {
		return User{}, err
	}
	return User{AccountID: out.AccountID, DisplayName: out.DisplayName}, nil
}

// ProjectName returns the display name of a project, proving the key exists
// and the account can see it.
func (c *Client) ProjectName(ctx context.Context, key string) (string, error) {
	var out struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/project/"+url.PathEscape(key), nil, &out); err != nil {
		return "", err
	}
	return out.Name, nil
}

// FindBoard returns the first board of a project, or ErrNotFound when the
// project has none or the site has no Jira Software.
func (c *Client) FindBoard(ctx context.Context, projectKey string) (id int, name string, err error) {
	var out struct {
		Values []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"values"`
	}
	q := url.Values{"projectKeyOrId": {projectKey}, "maxResults": {"10"}}
	if err := c.do(ctx, http.MethodGet, "/rest/agile/1.0/board?"+q.Encode(), nil, &out); err != nil {
		return 0, "", err
	}
	if len(out.Values) == 0 {
		return 0, "", ErrNotFound
	}
	return out.Values[0].ID, out.Values[0].Name, nil
}

// BoardColumns reads a board's columns in the order Jira shows them.
func (c *Client) BoardColumns(ctx context.Context, boardID int, name string) (Board, error) {
	var out struct {
		Name         string `json:"name"`
		ColumnConfig struct {
			Columns []struct {
				Name     string `json:"name"`
				Statuses []struct {
					ID string `json:"id"`
				} `json:"statuses"`
			} `json:"columns"`
		} `json:"columnConfig"`
	}
	if err := c.do(ctx, http.MethodGet, "/rest/agile/1.0/board/"+strconv.Itoa(boardID)+"/configuration", nil, &out); err != nil {
		return Board{}, err
	}
	board := Board{ID: boardID, Name: firstNonEmpty(out.Name, name)}
	for _, col := range out.ColumnConfig.Columns {
		column := Column{Name: col.Name}
		for _, s := range col.Statuses {
			column.StatusIDs = append(column.StatusIDs, s.ID)
		}
		board.Columns = append(board.Columns, column)
	}
	return board, nil
}

// Search runs a JQL query and returns up to limit tickets. truncated reports
// that Jira had more than limit matches.
func (c *Client) Search(ctx context.Context, jql string, limit int) (issues []Issue, truncated bool, err error) {
	if limit <= 0 {
		limit = PageSize
	}
	fields, acField := c.issueFields(ctx)
	token := ""
	for len(issues) < limit {
		page := PageSize
		if remaining := limit - len(issues); remaining < page {
			page = remaining
		}
		body := map[string]any{"jql": jql, "fields": fields, "maxResults": page}
		if token != "" {
			body["nextPageToken"] = token
		}
		var out struct {
			Issues        []rawIssue `json:"issues"`
			NextPageToken string     `json:"nextPageToken"`
			IsLast        bool       `json:"isLast"`
		}
		if err := c.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &out); err != nil {
			return nil, false, err
		}
		for _, raw := range out.Issues {
			issues = append(issues, raw.flatten(acField))
		}
		if out.NextPageToken == "" || out.IsLast || len(out.Issues) == 0 {
			return issues, false, nil
		}
		token = out.NextPageToken
	}
	return issues, true, nil
}

// Issue reads one ticket by key.
func (c *Client) Issue(ctx context.Context, key string) (Issue, error) {
	fields, acField := c.issueFields(ctx)
	q := url.Values{"fields": {strings.Join(fields, ",")}}
	var raw rawIssue
	if err := c.do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"?"+q.Encode(), nil, &raw); err != nil {
		return Issue{}, err
	}
	return raw.flatten(acField), nil
}

var baseFields = []string{"summary", "description", "status", "assignee", "issuetype", "priority", "labels", "updated"}

// issueFields is the field list a search asks for, plus the site's
// "Acceptance criteria" custom field when it has one. Discovery failing is not
// an error: the ticket's description is still sent.
func (c *Client) issueFields(ctx context.Context) (fields []string, acField string) {
	c.acMu.Lock()
	defer c.acMu.Unlock()
	if !c.acLoaded {
		var all []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Custom bool   `json:"custom"`
		}
		// A canceled lookup is retried by the next call; any other outcome is final.
		if err := c.do(ctx, http.MethodGet, "/rest/api/3/field", nil, &all); err == nil {
			for _, f := range all {
				if f.Custom && strings.EqualFold(strings.TrimSpace(f.Name), "acceptance criteria") {
					c.acField = f.ID
					break
				}
			}
			c.acLoaded = true
		} else if ctx.Err() == nil {
			c.acLoaded = true
		}
	}
	fields = append([]string(nil), baseFields...)
	if c.acField != "" {
		fields = append(fields, c.acField)
	}
	return fields, c.acField
}

// do sends one request and decodes a JSON response into out (which may be nil).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader = http.NoBody
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(c.email+":"+c.token)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// The transport error can echo the request URL; it never holds the token
		// (the token is only in a header), but keep the cause out of the message.
		return fmt.Errorf("%w: %s", ErrUnreachable, transportReason(err))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: reading response", ErrUnreachable)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ErrAuth
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode == http.StatusBadRequest:
		return fmt.Errorf("%w: %s", ErrBadQuery, errorMessages(data))
	case resp.StatusCode >= 300:
		return fmt.Errorf("%w: HTTP %d", ErrUpstream, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: unexpected response", ErrUpstream)
	}
	return nil
}

func transportReason(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "timed out"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "host not found"
	}
	return "connection failed"
}

// errorMessages pulls Jira's human message out of an error body.
func errorMessages(data []byte) string {
	var out struct {
		ErrorMessages []string          `json:"errorMessages"`
		Errors        map[string]string `json:"errors"`
	}
	if json.Unmarshal(data, &out) != nil {
		return "the query was not accepted"
	}
	parts := append([]string(nil), out.ErrorMessages...)
	for _, v := range out.Errors {
		parts = append(parts, v)
	}
	if len(parts) == 0 {
		return "the query was not accepted"
	}
	return strings.Join(parts, "; ")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type rawIssue struct {
	Key    string                     `json:"key"`
	Fields map[string]json.RawMessage `json:"fields"`
}

func (r rawIssue) flatten(acField string) Issue {
	issue := Issue{Key: r.Key}
	var summary string
	_ = json.Unmarshal(r.Fields["summary"], &summary)
	issue.Summary = strings.TrimSpace(summary)
	issue.Description = adfToText(r.Fields["description"])
	if acField != "" {
		issue.AcceptanceCriteria = adfToText(r.Fields[acField])
	}
	if issue.AcceptanceCriteria == "" {
		issue.Description, issue.AcceptanceCriteria = splitAcceptanceCriteria(issue.Description)
	}
	var status struct {
		ID             string `json:"id"`
		Name           string `json:"name"`
		StatusCategory struct {
			Key string `json:"key"`
		} `json:"statusCategory"`
	}
	_ = json.Unmarshal(r.Fields["status"], &status)
	issue.Status = Status{ID: status.ID, Name: status.Name, Category: status.StatusCategory.Key}
	var assignee *User
	if json.Unmarshal(r.Fields["assignee"], &assignee) == nil && assignee != nil {
		issue.Assignee = assignee
	}
	var named struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(r.Fields["issuetype"], &named)
	issue.IssueType = named.Name
	named.Name = ""
	_ = json.Unmarshal(r.Fields["priority"], &named)
	issue.Priority = named.Name
	_ = json.Unmarshal(r.Fields["labels"], &issue.Labels)
	var updated string
	if json.Unmarshal(r.Fields["updated"], &updated) == nil {
		// Jira writes offsets without a colon ("+0200").
		if t, err := time.Parse("2006-01-02T15:04:05.000-0700", updated); err == nil {
			issue.Updated = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, updated); err == nil {
			issue.Updated = t.UTC()
		}
	}
	return issue
}

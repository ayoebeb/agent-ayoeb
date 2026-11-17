// Package jira connects a project to a Jira Cloud site: it keeps the
// connection (and its API token) in the daemon's data dir, reads the tickets a
// project's JQL matches, lays them out in the columns of the project's Jira
// board, and hands a ticket to the project's orchestrator as a new task.
//
// The service is read-only toward Jira. It never changes a ticket's status or
// posts a comment.
package jira

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	jiraclient "github.com/aoagents/agent-orchestrator/backend/internal/adapters/jira"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// Stable API error codes.
const (
	CodeNotConnected = "JIRA_NOT_CONNECTED"
	CodeAuthFailed   = "JIRA_AUTH_FAILED"
	CodeUnreachable  = "JIRA_UNREACHABLE"
	CodeBadQuery     = "JIRA_BAD_QUERY"
	CodeNotFound     = "JIRA_NOT_FOUND"
	CodeUpstream     = "JIRA_UPSTREAM_ERROR"
	CodeInvalidInput = "JIRA_INVALID_INPUT"
	CodeOrchestrator = "JIRA_ORCHESTRATOR_UNAVAILABLE"
)

const (
	maxTickets        = 200
	clientTTL         = 5 * time.Minute
	duplicateStartTTL = 15 * time.Second
)

// Jira failures the controller maps to a status and a stable code; everything
// else is an *apierr.Error.
var (
	// ErrAuthFailed means Jira rejected the email or API token.
	ErrAuthFailed = errors.New("jira rejected the email or API token")
	// ErrUnreachable means Jira could not be reached (offline, DNS, timeout).
	ErrUnreachable = errors.New("jira could not be reached")
	// ErrBadQuery means Jira refused the JQL.
	ErrBadQuery = errors.New("jira rejected the query")
	// ErrUpstream is any other failure response from Jira.
	ErrUpstream = errors.New("jira returned an error")
)

// Orchestrators is the slice of the session service this package needs. The
// daemon adapts *session.Service to it.
type Orchestrators interface {
	// ActiveOrchestrator returns the project's running orchestrator session id,
	// or "" when it has none.
	ActiveOrchestrator(ctx context.Context, projectID string) (string, error)
	// SpawnOrchestrator starts the project's orchestrator (returning the running
	// one when it already exists) and returns its session id.
	SpawnOrchestrator(ctx context.Context, projectID string) (string, error)
	// SendTask delivers text to the orchestrator the way a person typing in its
	// chat would.
	SendTask(ctx context.Context, sessionID, text string) error
}

// Projects answers whether a project id is registered.
type Projects interface {
	ProjectExists(ctx context.Context, projectID string) (bool, error)
}

// Deps are the collaborators a Service needs beyond its data dir.
type Deps struct {
	DataDir       string
	Projects      Projects
	Orchestrators Orchestrators
	Now           func() time.Time
}

// Service owns the per-project Jira connections.
type Service struct {
	dataDir       string
	projects      Projects
	orchestrators Orchestrators
	now           func() time.Time

	mu      sync.Mutex // guards the files and the caches below
	clients map[string]*cachedClient
	// starts holds one entry per ticket being sent, or sent a moment ago, so
	// concurrent requests for the same ticket cannot each send the brief.
	starts map[string]*startFlight
}

// startFlight is one ticket on its way to an orchestrator. A second request for
// the same ticket waits for it instead of sending the brief again. Its fields
// are read and written under Service.mu; done is closed once complete is set.
type startFlight struct {
	done           chan struct{}
	complete       bool
	ok             bool
	orchestratorID string
	finished       time.Time
}

type cachedClient struct {
	fingerprint string
	client      *jiraclient.Client
	board       *jiraclient.Board
	boardLoaded bool
	created     time.Time
}

// New returns a Service rooted at deps.DataDir.
func New(deps Deps) *Service {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		dataDir: deps.DataDir, projects: deps.Projects, orchestrators: deps.Orchestrators,
		now: now, clients: map[string]*cachedClient{}, starts: map[string]*startFlight{},
	}
}

// Connection is a project's Jira connection as the API shows it. It has no
// field for the token: whether one is stored is just Connected.
type Connection struct {
	Connected   bool       `json:"connected"`
	SiteURL     string     `json:"siteUrl,omitempty"`
	Email       string     `json:"email,omitempty"`
	ProjectKey  string     `json:"projectKey,omitempty"`
	BoardID     int        `json:"boardId,omitempty"`
	JQL         string     `json:"jql,omitempty"`
	AccountName string     `json:"accountName,omitempty"`
	ConnectedAt *time.Time `json:"connectedAt,omitempty"`
}

// ConnectInput is what a person enters to connect a project. An empty APIToken
// keeps the token already stored for the project.
type ConnectInput struct {
	SiteURL    string
	Email      string
	APIToken   string
	ProjectKey string
	BoardID    int
	JQL        string
}

// TestResult reports what a connection check found.
type TestResult struct {
	AccountName string `json:"accountName"`
	ProjectName string `json:"projectName,omitempty"`
	// JQL is the query the board will use.
	JQL string `json:"jql"`
	// TicketCount is how many tickets the query returned (up to the board limit).
	TicketCount int `json:"ticketCount"`
}

// TicketUser is a ticket's assignee.
type TicketUser struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

// TicketStatus is a ticket's workflow status. Category is "new",
// "indeterminate" or "done".
type TicketStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
}

// TicketSession is a session that was started for a ticket.
type TicketSession struct {
	SessionID string    `json:"sessionId"`
	StartedAt time.Time `json:"startedAt"`
}

// Ticket is one Jira ticket on the board.
type Ticket struct {
	Key                string          `json:"key"`
	Summary            string          `json:"summary"`
	Description        string          `json:"description,omitempty"`
	AcceptanceCriteria string          `json:"acceptanceCriteria,omitempty"`
	URL                string          `json:"url"`
	Status             TicketStatus    `json:"status"`
	Assignee           *TicketUser     `json:"assignee,omitempty"`
	IssueType          string          `json:"issueType,omitempty"`
	Priority           string          `json:"priority,omitempty"`
	Labels             []string        `json:"labels,omitempty"`
	UpdatedAt          *time.Time      `json:"updatedAt,omitempty"`
	Sessions           []TicketSession `json:"sessions,omitempty"`
}

// BoardColumn is one column of the Jira board. Other marks the catch-all
// column for tickets whose status no board column shows; the client names it.
type BoardColumn struct {
	Name    string   `json:"name"`
	Other   bool     `json:"other,omitempty"`
	Tickets []Ticket `json:"tickets"`
}

// Board is the Jira view: tickets grouped into columns.
type Board struct {
	SiteURL    string `json:"siteUrl"`
	ProjectKey string `json:"projectKey,omitempty"`
	// BoardName is the Jira board the columns came from; empty when columns are
	// the tickets' statuses.
	BoardName string `json:"boardName,omitempty"`
	// AccountID is the connected Jira account, so the client can mark "mine".
	AccountID string        `json:"accountId,omitempty"`
	JQL       string        `json:"jql"`
	Columns   []BoardColumn `json:"columns"`
	Total     int           `json:"total"`
	// Truncated says Jira had more tickets than the board shows.
	Truncated bool      `json:"truncated,omitempty"`
	FetchedAt time.Time `json:"fetchedAt"`
}

// StartResult reports where a ticket went.
type StartResult struct {
	IssueKey       string `json:"issueKey"`
	OrchestratorID string `json:"orchestratorId"`
	// Spawned is true when the project had no orchestrator and one was started
	// for the ticket.
	Spawned bool `json:"spawned,omitempty"`
	// Duplicate is true when the same ticket was sent a moment ago and this
	// request was ignored.
	Duplicate bool `json:"duplicate,omitempty"`
}

var (
	projectKeyRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{1,19}$`)
	issueKeyRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{1,19}-\d{1,9}$`)
)

// Status returns the project's connection, or a zero Connection when none.
func (s *Service) Status(ctx context.Context, projectID string) (Connection, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return Connection{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadConnectionsLocked()
	if err != nil {
		return Connection{}, err
	}
	stored, ok := doc.Connections[projectID]
	if !ok || stored.APIToken == "" {
		return Connection{}, nil
	}
	return publicConnection(stored), nil
}

func publicConnection(c storedConnection) Connection {
	connectedAt := c.ConnectedAt
	return Connection{
		Connected: true, SiteURL: c.SiteURL, Email: c.Email, ProjectKey: c.ProjectKey,
		BoardID: c.BoardID, JQL: c.JQL, AccountName: c.AccountName, ConnectedAt: &connectedAt,
	}
}

// Test checks a connection without saving it: the credentials sign in, the
// project exists, and the query runs.
func (s *Service) Test(ctx context.Context, projectID string, in ConnectInput) (TestResult, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return TestResult{}, err
	}
	cand, err := s.candidate(projectID, in)
	if err != nil {
		return TestResult{}, err
	}
	res, _, err := s.verify(ctx, cand)
	return res, err
}

// Connect verifies a connection and, only when it works, stores it. The token
// never leaves the data dir again.
func (s *Service) Connect(ctx context.Context, projectID string, in ConnectInput) (Connection, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return Connection{}, err
	}
	cand, err := s.candidate(projectID, in)
	if err != nil {
		return Connection{}, err
	}
	_, me, err := s.verify(ctx, cand)
	if err != nil {
		return Connection{}, err
	}
	cand.AccountID, cand.AccountName = me.AccountID, me.DisplayName
	cand.ConnectedAt = s.now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadConnectionsLocked()
	if err != nil {
		return Connection{}, err
	}
	doc.Version = fileVersion
	doc.Connections[projectID] = cand
	if err := writeJSON(s.connectionsPath(), doc); err != nil {
		return Connection{}, fmt.Errorf("store jira connection: %w", err)
	}
	delete(s.clients, projectID)
	return publicConnection(cand), nil
}

// Disconnect removes the project's connection, its token and its ticket links.
// It is not an error when nothing was connected.
func (s *Service) Disconnect(ctx context.Context, projectID string) error {
	if err := s.requireProject(ctx, projectID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadConnectionsLocked()
	if err != nil {
		return err
	}
	delete(s.clients, projectID)
	if _, ok := doc.Connections[projectID]; ok {
		delete(doc.Connections, projectID)
		if err := writeJSON(s.connectionsPath(), doc); err != nil {
			return fmt.Errorf("remove jira connection: %w", err)
		}
	}
	links, err := s.loadLinksLocked()
	if err != nil {
		return err
	}
	kept := links.Links[:0]
	for _, l := range links.Links {
		if l.ProjectID != projectID {
			kept = append(kept, l)
		}
	}
	if len(kept) != len(links.Links) {
		links.Links = kept
		if err := writeJSON(s.linksPath(), links); err != nil {
			return fmt.Errorf("remove jira links: %w", err)
		}
	}
	return nil
}

// Board reads the project's tickets and lays them out in board columns.
func (s *Service) Board(ctx context.Context, projectID string) (Board, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return Board{}, err
	}
	stored, err := s.connection(projectID)
	if err != nil {
		return Board{}, err
	}
	client, jiraBoard := s.clientFor(ctx, projectID, stored)
	jql := EffectiveJQL(stored.ProjectKey, stored.JQL)
	issues, truncated, err := client.Search(ctx, jql, maxTickets)
	if err != nil {
		return Board{}, translate(err)
	}
	links, err := s.linksFor(projectID)
	if err != nil {
		return Board{}, err
	}
	columns := layoutColumns(issues, jiraBoard, client, links)
	board := Board{
		SiteURL: stored.SiteURL, ProjectKey: stored.ProjectKey, AccountID: stored.AccountID, JQL: jql,
		Columns: columns, Total: len(issues), Truncated: truncated, FetchedAt: s.now().UTC(),
	}
	if jiraBoard != nil {
		board.BoardName = jiraBoard.Name
	}
	return board, nil
}

// Start reads the ticket fresh from Jira, sends it to the project's
// orchestrator as a new task, and remembers which session got it. The ticket
// text comes from Jira, not from the caller.
func (s *Service) Start(ctx context.Context, projectID, issueKey string) (StartResult, error) {
	if err := s.requireProject(ctx, projectID); err != nil {
		return StartResult{}, err
	}
	issueKey = strings.ToUpper(strings.TrimSpace(issueKey))
	if !issueKeyRe.MatchString(issueKey) {
		return StartResult{}, apierr.Invalid(CodeInvalidInput, "That is not a Jira ticket key (for example YIA-123).", nil)
	}
	if s.orchestrators == nil {
		return StartResult{}, apierr.NotImplemented(CodeOrchestrator, "Starting work from a ticket is not available here.")
	}
	stored, err := s.connection(projectID)
	if err != nil {
		return StartResult{}, err
	}
	// A project that shows its Jira project's own tickets only starts tickets
	// from it. A custom JQL is the person's own scope, so it is not second-guessed.
	if stored.JQL == "" && stored.ProjectKey != "" && !strings.HasPrefix(issueKey, stored.ProjectKey+"-") {
		return StartResult{}, apierr.Invalid(CodeInvalidInput, fmt.Sprintf("%s is not a ticket of the Jira project %s this board shows.", issueKey, stored.ProjectKey), nil)
	}

	// Reserve the ticket before anything is sent. A concurrent request for the
	// same ticket waits here and gets this one's answer instead of sending again.
	flight, earlier, err := s.beginStart(ctx, projectID+"\x00"+issueKey)
	if err != nil {
		return StartResult{}, err
	}
	if earlier != nil {
		earlier.IssueKey = issueKey
		return *earlier, nil
	}
	result, err := s.deliver(ctx, projectID, issueKey, stored)
	s.finishStart(projectID+"\x00"+issueKey, flight, result.OrchestratorID, err == nil)
	return result, err
}

// deliver reads the ticket from Jira and hands it to the orchestrator.
func (s *Service) deliver(ctx context.Context, projectID, issueKey string, stored storedConnection) (StartResult, error) {
	client, _ := s.clientFor(ctx, projectID, stored)
	issue, err := client.Issue(ctx, issueKey)
	if err != nil {
		return StartResult{}, translate(err)
	}
	orchestratorID, err := s.orchestrators.ActiveOrchestrator(ctx, projectID)
	if err != nil {
		return StartResult{}, err
	}
	spawned := false
	if orchestratorID == "" {
		if orchestratorID, err = s.orchestrators.SpawnOrchestrator(ctx, projectID); err != nil {
			return StartResult{}, err
		}
		spawned = true
	}
	if err := s.orchestrators.SendTask(ctx, orchestratorID, BuildBrief(issue, client.BrowseURL(issue.Key))); err != nil {
		return StartResult{}, err
	}
	if err := s.recordLink(projectID, issueKey, orchestratorID); err != nil {
		return StartResult{}, fmt.Errorf("remember ticket link: %w", err)
	}
	return StartResult{IssueKey: issueKey, OrchestratorID: orchestratorID, Spawned: spawned}, nil
}

// beginStart reserves a ticket for the caller. It returns the reservation to
// finish, or, when the ticket is already being sent or was sent moments ago, the
// earlier result with Duplicate set. A caller that finds a send in flight waits
// for it; if that send failed, the caller takes over and sends it itself.
func (s *Service) beginStart(ctx context.Context, key string) (*startFlight, *StartResult, error) {
	for {
		s.mu.Lock()
		now := s.now()
		for k, f := range s.starts {
			if f.complete && now.Sub(f.finished) >= duplicateStartTTL {
				delete(s.starts, k)
			}
		}
		f := s.starts[key]
		if f == nil {
			f = &startFlight{done: make(chan struct{})}
			s.starts[key] = f
			s.mu.Unlock()
			return f, nil, nil
		}
		if f.complete {
			// A failed send removes its entry, so a complete one here succeeded.
			earlier := StartResult{OrchestratorID: f.orchestratorID, Duplicate: true}
			s.mu.Unlock()
			return nil, &earlier, nil
		}
		done := f.done
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

// finishStart settles a reservation. A send that failed is forgotten so the
// next request tries again; one that succeeded blocks repeats for a few seconds.
func (s *Service) finishStart(key string, f *startFlight, orchestratorID string, ok bool) {
	s.mu.Lock()
	f.complete, f.ok = true, ok
	if ok {
		f.orchestratorID, f.finished = orchestratorID, s.now()
	} else if s.starts[key] == f {
		delete(s.starts, key)
	}
	s.mu.Unlock()
	close(f.done)
}
func (s *Service) requireProject(ctx context.Context, projectID string) error {
	if strings.TrimSpace(projectID) == "" {
		return apierr.Invalid(CodeInvalidInput, "A project is required.", nil)
	}
	if s.projects == nil {
		return nil
	}
	ok, err := s.projects.ProjectExists(ctx, projectID)
	if err != nil {
		return err
	}
	if !ok {
		return apierr.NotFound("PROJECT_NOT_FOUND", "Unknown project")
	}
	return nil
}

// connection returns the stored connection or a "not connected" error.
func (s *Service) connection(projectID string) (storedConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadConnectionsLocked()
	if err != nil {
		return storedConnection{}, err
	}
	stored, ok := doc.Connections[projectID]
	if !ok || stored.APIToken == "" {
		return storedConnection{}, apierr.Conflict(CodeNotConnected, "This project is not connected to Jira.", nil)
	}
	return stored, nil
}

// candidate validates input and fills in what is missing: the stored token, when
// the caller leaves it blank and the site and email are unchanged. Nothing is
// written.
func (s *Service) candidate(projectID string, in ConnectInput) (storedConnection, error) {
	invalid := func(msg string) error { return apierr.Invalid(CodeInvalidInput, msg, nil) }
	site, err := jiraclient.NormalizeSiteURL(in.SiteURL)
	if err != nil {
		return storedConnection{}, invalid("Enter your Jira site, for example yourcompany.atlassian.net.")
	}
	email := strings.TrimSpace(in.Email)
	if email == "" || !strings.Contains(email, "@") {
		return storedConnection{}, invalid("Enter the email address of your Atlassian account.")
	}
	token := strings.TrimSpace(in.APIToken)
	if token == "" {
		stored, err := s.connection(projectID)
		if err != nil {
			return storedConnection{}, invalid("Enter your Jira API token.")
		}
		// A saved token belongs to the site and account it was saved for. It is
		// reused only while both are unchanged, so a blank token can never send it
		// to a host or account the person typed in afterwards.
		if !strings.EqualFold(stored.SiteURL, site) || !strings.EqualFold(strings.TrimSpace(stored.Email), email) {
			return storedConnection{}, invalid("The site or email changed, so enter the API token for that account.")
		}
		token = stored.APIToken
	}
	key := strings.ToUpper(strings.TrimSpace(in.ProjectKey))
	if key != "" && !projectKeyRe.MatchString(key) {
		return storedConnection{}, invalid("A project key is letters and digits, for example YIA.")
	}
	jql := strings.TrimSpace(in.JQL)
	if key == "" && jql == "" {
		return storedConnection{}, invalid("Enter a Jira project key or a JQL query to choose which tickets to show.")
	}
	if in.BoardID < 0 {
		return storedConnection{}, invalid("The board id is a number.")
	}
	return storedConnection{SiteURL: site, Email: email, APIToken: token, ProjectKey: key, BoardID: in.BoardID, JQL: jql}, nil
}

// verify signs in, checks the project, and runs the query once.
func (s *Service) verify(ctx context.Context, c storedConnection) (TestResult, jiraclient.User, error) {
	client := newClient(c)
	me, err := client.Myself(ctx)
	if err != nil {
		return TestResult{}, jiraclient.User{}, redact(translate(err), c.APIToken)
	}
	res := TestResult{AccountName: me.DisplayName, JQL: EffectiveJQL(c.ProjectKey, c.JQL)}
	if c.ProjectKey != "" {
		name, err := client.ProjectName(ctx, c.ProjectKey)
		if err != nil {
			if errors.Is(err, jiraclient.ErrNotFound) {
				return TestResult{}, jiraclient.User{}, apierr.Invalid(CodeNotFound, fmt.Sprintf("Jira has no project %s that this account can see.", c.ProjectKey), nil)
			}
			return TestResult{}, jiraclient.User{}, redact(translate(err), c.APIToken)
		}
		res.ProjectName = name
	}
	issues, _, err := client.Search(ctx, res.JQL, maxTickets)
	if err != nil {
		return TestResult{}, jiraclient.User{}, redact(translate(err), c.APIToken)
	}
	res.TicketCount = len(issues)
	return res, me, nil
}

func newClient(c storedConnection) *jiraclient.Client {
	return jiraclient.New(jiraclient.Config{SiteURL: c.SiteURL, Email: c.Email, Token: c.APIToken})
}

// clientFor returns the project's cached client and its Jira board columns (nil
// when the project has no board or the site has no Jira Software). Both are
// cached for a few minutes: the field lookup and board layout rarely change.
func (s *Service) clientFor(ctx context.Context, projectID string, c storedConnection) (*jiraclient.Client, *jiraclient.Board) {
	fingerprint := strings.Join([]string{c.SiteURL, c.Email, c.APIToken, c.ProjectKey, fmt.Sprint(c.BoardID)}, "\x00")
	s.mu.Lock()
	entry := s.clients[projectID]
	if entry == nil || entry.fingerprint != fingerprint || s.now().Sub(entry.created) > clientTTL {
		entry = &cachedClient{fingerprint: fingerprint, client: newClient(c), created: s.now()}
		s.clients[projectID] = entry
	}
	client, loaded, board := entry.client, entry.boardLoaded, entry.board
	s.mu.Unlock()
	if loaded {
		return client, board
	}
	board = discoverBoard(ctx, client, c)
	s.mu.Lock()
	if s.clients[projectID] == entry {
		entry.board, entry.boardLoaded = board, true
	}
	s.mu.Unlock()
	return client, board
}

func discoverBoard(ctx context.Context, client *jiraclient.Client, c storedConnection) *jiraclient.Board {
	id, name := c.BoardID, ""
	if id == 0 {
		if c.ProjectKey == "" {
			return nil
		}
		var err error
		if id, name, err = client.FindBoard(ctx, c.ProjectKey); err != nil {
			return nil
		}
	}
	board, err := client.BoardColumns(ctx, id, name)
	if err != nil || len(board.Columns) == 0 {
		return nil
	}
	return &board
}

// EffectiveJQL is the query the board runs: the person's own JQL when they
// gave one, otherwise the project's open tickets plus what finished this week.
func EffectiveJQL(projectKey, custom string) string {
	if strings.TrimSpace(custom) != "" {
		return strings.TrimSpace(custom)
	}
	return fmt.Sprintf(`project = %q AND (statusCategory != Done OR resolutiondate >= -7d) ORDER BY updated DESC`, projectKey)
}

// layoutColumns groups tickets into the Jira board's columns when the board is
// known, otherwise into one column per status ordered by workflow category.
func layoutColumns(issues []jiraclient.Issue, board *jiraclient.Board, client *jiraclient.Client, links map[string][]TicketSession) []BoardColumn {
	tickets := make([]Ticket, len(issues))
	for i, issue := range issues {
		tickets[i] = toTicket(issue, client.BrowseURL(issue.Key), links[issue.Key])
	}
	if board != nil {
		return columnsFromBoard(tickets, *board)
	}
	return columnsFromStatuses(tickets)
}

func toTicket(issue jiraclient.Issue, url string, sessions []TicketSession) Ticket {
	t := Ticket{
		Key: issue.Key, Summary: issue.Summary, Description: issue.Description, AcceptanceCriteria: issue.AcceptanceCriteria,
		URL: url, Status: TicketStatus{ID: issue.Status.ID, Name: issue.Status.Name, Category: issue.Status.Category},
		IssueType: issue.IssueType, Priority: issue.Priority, Labels: issue.Labels, Sessions: sessions,
	}
	if !issue.Updated.IsZero() {
		updated := issue.Updated
		t.UpdatedAt = &updated
	}
	if issue.Assignee != nil {
		t.Assignee = &TicketUser{AccountID: issue.Assignee.AccountID, DisplayName: issue.Assignee.DisplayName}
	}
	return t
}

func columnsFromBoard(tickets []Ticket, board jiraclient.Board) []BoardColumn {
	columns := make([]BoardColumn, 0, len(board.Columns)+1)
	byStatus := map[string]int{}
	for i, col := range board.Columns {
		columns = append(columns, BoardColumn{Name: col.Name, Tickets: []Ticket{}})
		for _, id := range col.StatusIDs {
			byStatus[id] = i
		}
	}
	other := make([]Ticket, 0, len(tickets))
	for _, t := range tickets {
		if i, ok := byStatus[t.Status.ID]; ok {
			columns[i].Tickets = append(columns[i].Tickets, t)
		} else {
			other = append(other, t)
		}
	}
	if len(other) > 0 {
		columns = append(columns, BoardColumn{Name: "Other", Other: true, Tickets: other})
	}
	return columns
}

func columnsFromStatuses(tickets []Ticket) []BoardColumn {
	byName := map[string]*BoardColumn{}
	rank := map[string]int{}
	order := make([]string, 0, len(tickets))
	for _, t := range tickets {
		name := t.Status.Name
		if name == "" {
			name = "Other"
		}
		col, ok := byName[name]
		if !ok {
			col = &BoardColumn{Name: name, Tickets: []Ticket{}}
			byName[name] = col
			rank[name] = categoryRank(t.Status.Category)
			order = append(order, name)
		}
		col.Tickets = append(col.Tickets, t)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if rank[order[i]] != rank[order[j]] {
			return rank[order[i]] < rank[order[j]]
		}
		return order[i] < order[j]
	})
	columns := make([]BoardColumn, 0, len(order))
	for _, name := range order {
		columns = append(columns, *byName[name])
	}
	return columns
}

func categoryRank(category string) int {
	switch category {
	case "new":
		return 0
	case "done":
		return 2
	default:
		return 1
	}
}

// translate maps a Jira client error onto the API vocabulary.
func translate(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, jiraclient.ErrAuth):
		return ErrAuthFailed
	case errors.Is(err, jiraclient.ErrUnreachable):
		return fmt.Errorf("%w: %s", ErrUnreachable, strings.TrimPrefix(err.Error(), jiraclient.ErrUnreachable.Error()+": "))
	case errors.Is(err, jiraclient.ErrBadQuery):
		return fmt.Errorf("%w: %s", ErrBadQuery, strings.TrimPrefix(err.Error(), jiraclient.ErrBadQuery.Error()+": "))
	case errors.Is(err, jiraclient.ErrNotFound):
		return apierr.NotFound(CodeNotFound, "Jira could not find that ticket, project or board.")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	default:
		return ErrUpstream
	}
}

// redactedError shows a message with the token removed while still unwrapping
// to the original, so errors.Is keeps working.
type redactedError struct {
	msg string
	err error
}

func (e redactedError) Error() string { return e.msg }
func (e redactedError) Unwrap() error { return e.err }

// redact removes the token from an error's text. The client never puts the
// token in an error, but Jira can echo a query the person typed, and that
// query may be the token pasted into the wrong box.
func redact(err error, token string) error {
	if err == nil || token == "" || !strings.Contains(err.Error(), token) {
		return err
	}
	return redactedError{msg: strings.ReplaceAll(err.Error(), token, "[redacted]"), err: err}
}

func (s *Service) linksFor(projectID string) (map[string][]TicketSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadLinksLocked()
	if err != nil {
		return nil, err
	}
	out := map[string][]TicketSession{}
	for _, l := range doc.Links {
		if l.ProjectID == projectID {
			out[l.IssueKey] = append(out[l.IssueKey], TicketSession{SessionID: l.SessionID, StartedAt: l.StartedAt})
		}
	}
	return out, nil
}

const maxLinks = 500

func (s *Service) recordLink(projectID, key, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadLinksLocked()
	if err != nil {
		return err
	}
	doc.Version = fileVersion
	doc.Links = append(doc.Links, storedLink{ProjectID: projectID, IssueKey: key, SessionID: sessionID, StartedAt: s.now().UTC()})
	if len(doc.Links) > maxLinks {
		doc.Links = doc.Links[len(doc.Links)-maxLinks:]
	}
	return writeJSON(s.linksPath(), doc)
}

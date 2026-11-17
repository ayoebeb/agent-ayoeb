package jira_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/jira/jiratest"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	jirasvc "github.com/aoagents/agent-orchestrator/backend/internal/service/jira"
)

const projectID = "yoobi-app"

type fakeProjects map[string]bool

func (p fakeProjects) ProjectExists(_ context.Context, id string) (bool, error) { return p[id], nil }

// fakeOrchestrators records what the service asks of the session layer. It is
// safe for concurrent use, and a send can be held back with gate.
type fakeOrchestrators struct {
	mu       sync.Mutex
	active   string
	spawned  []string
	sent     []sentTask
	sendErr  error
	spawnErr error
	// gate, when set, holds every SendTask until it is closed; inSend counts the
	// sends waiting on it.
	gate   chan struct{}
	inSend int
}

func (f *fakeOrchestrators) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

type sentTask struct{ session, text string }

func (f *fakeOrchestrators) ActiveOrchestrator(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.active, nil
}

func (f *fakeOrchestrators) SpawnOrchestrator(_ context.Context, project string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.spawnErr != nil {
		return "", f.spawnErr
	}
	f.spawned = append(f.spawned, project)
	f.active = "orch-new"
	return f.active, nil
}

func (f *fakeOrchestrators) SendTask(_ context.Context, session, text string) error {
	f.mu.Lock()
	gate := f.gate
	f.inSend++
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inSend--
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, sentTask{session, text})
	return nil
}

type fixture struct {
	t     *testing.T
	dir   string
	jira  *jiratest.Server
	orch  *fakeOrchestrators
	svc   *jirasvc.Service
	clock time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	srv := jiratest.New()
	t.Cleanup(srv.Close)
	srv.ACField = "customfield_10042"
	srv.Columns = []jiratest.Column{
		{Name: "To Do", StatusIDs: []string{"1"}},
		{Name: "In Progress", StatusIDs: []string{"3"}},
		{Name: "Done", StatusIDs: []string{"5"}},
	}
	srv.Issues = []jiratest.Issue{
		{Key: "YIA-10", Summary: "Add CSV export", Description: "Users want CSV export.\nKeep it small.", AcceptanceText: "- exports all rows\n- has a test", StatusID: "1", StatusName: "To Do", StatusCategory: "new", AssigneeID: "acc-1", AssigneeName: "Ada Example", Type: "Story", Priority: "High", Labels: []string{"export"}, Updated: "2026-11-10T09:30:00.000+0100"},
		{Key: "YIA-11", Summary: "Fix login redirect", StatusID: "3", StatusName: "In Progress", StatusCategory: "indeterminate", Type: "Bug", Priority: "Medium", Updated: "2026-11-11T10:00:00.000+0000"},
		{Key: "YIA-12", Summary: "Old chore", StatusID: "99", StatusName: "Parked", StatusCategory: "new", Type: "Task", Updated: "2026-11-01T10:00:00.000+0000"},
	}
	dir := t.TempDir()
	f := &fixture{t: t, dir: dir, jira: srv, orch: &fakeOrchestrators{}, clock: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)}
	f.svc = jirasvc.New(jirasvc.Deps{
		DataDir: dir, Projects: fakeProjects{projectID: true}, Orchestrators: f.orch,
		Now: func() time.Time { return f.clock },
	})
	return f
}

func (f *fixture) input() jirasvc.ConnectInput {
	return jirasvc.ConnectInput{SiteURL: f.jira.URL, Email: f.jira.Email, APIToken: f.jira.Token, ProjectKey: "yia"}
}

func (f *fixture) connect() {
	f.t.Helper()
	if _, err := f.svc.Connect(context.Background(), projectID, f.input()); err != nil {
		f.t.Fatalf("Connect: %v", err)
	}
}

func TestConnectStoresTokenOnlyInTheDataDirAndNeverReturnsIt(t *testing.T) {
	f := newFixture(t)
	conn, err := f.svc.Connect(context.Background(), projectID, f.input())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !conn.Connected || conn.SiteURL != f.jira.URL || conn.Email != "dev@example.com" || conn.ProjectKey != "YIA" || conn.AccountName != "Ada Example" {
		t.Fatalf("connection = %+v", conn)
	}

	status, err := f.svc.Status(context.Background(), projectID)
	if err != nil || !status.Connected {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	for name, v := range map[string]any{"connect": conn, "status": status} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), f.jira.Token) {
			t.Fatalf("%s response leaks the token: %s", name, raw)
		}
	}

	file := filepath.Join(f.dir, "jira-connections.json")
	data, err := os.ReadFile(file)
	if err != nil || !strings.Contains(string(data), f.jira.Token) {
		t.Fatalf("token is not stored in %s: %v", file, err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(file)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("connections file mode = %v, want 0600", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(f.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file %s left behind", e.Name())
		}
	}
}

func TestStatusIsEmptyBeforeConnectAndAfterDisconnect(t *testing.T) {
	f := newFixture(t)
	if st, err := f.svc.Status(context.Background(), projectID); err != nil || st.Connected {
		t.Fatalf("before connect: %+v, %v", st, err)
	}
	f.connect()
	if err := f.svc.Disconnect(context.Background(), projectID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if st, _ := f.svc.Status(context.Background(), projectID); st.Connected {
		t.Fatalf("still connected after Disconnect: %+v", st)
	}
	data, _ := os.ReadFile(filepath.Join(f.dir, "jira-connections.json"))
	if strings.Contains(string(data), f.jira.Token) {
		t.Fatalf("token survives Disconnect: %s", data)
	}
	if err := f.svc.Disconnect(context.Background(), projectID); err != nil {
		t.Fatalf("second Disconnect should be a no-op: %v", err)
	}
}

func TestConnectRejectedCredentialsStoreNothing(t *testing.T) {
	f := newFixture(t)
	in := f.input()
	in.APIToken = "wrong-token"
	_, err := f.svc.Connect(context.Background(), projectID, in)
	if !errors.Is(err, jirasvc.ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("error leaks the token: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(f.dir, "jira-connections.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a failed connect wrote a file: %v", statErr)
	}
}

func TestConnectUnknownProjectKey(t *testing.T) {
	f := newFixture(t)
	in := f.input()
	in.ProjectKey = "NOPE"
	_, err := f.svc.Connect(context.Background(), projectID, in)
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeNotFound {
		t.Fatalf("err = %v, want a JIRA_NOT_FOUND api error", err)
	}
}

func TestConnectValidatesInput(t *testing.T) {
	f := newFixture(t)
	cases := map[string]func(*jirasvc.ConnectInput){
		"no site":         func(in *jirasvc.ConnectInput) { in.SiteURL = "" },
		"plain http":      func(in *jirasvc.ConnectInput) { in.SiteURL = "http://yoobi.atlassian.net" },
		"no email":        func(in *jirasvc.ConnectInput) { in.Email = "" },
		"no token":        func(in *jirasvc.ConnectInput) { in.APIToken = "" },
		"bad project key": func(in *jirasvc.ConnectInput) { in.ProjectKey = "no spaces!" },
	}
	for name, mutate := range cases {
		in := f.input()
		mutate(&in)
		_, err := f.svc.Connect(context.Background(), projectID, in)
		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeInvalidInput {
			t.Errorf("%s: err = %v, want JIRA_INVALID_INPUT", name, err)
		}
	}
	if len(f.jira.Requests()) != 0 {
		t.Fatalf("invalid input reached Jira: %+v", f.jira.Requests())
	}
}

func TestUnknownProjectIsNotFound(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Status(context.Background(), "nope")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "PROJECT_NOT_FOUND" {
		t.Fatalf("err = %v, want PROJECT_NOT_FOUND", err)
	}
}

func TestReconnectWithEmptyTokenKeepsTheStoredOne(t *testing.T) {
	f := newFixture(t)
	f.connect()
	in := f.input()
	in.APIToken = ""
	in.JQL = `project = YIA AND assignee = currentUser()`
	conn, err := f.svc.Connect(context.Background(), projectID, in)
	if err != nil || conn.JQL != in.JQL {
		t.Fatalf("Connect = %+v, %v", conn, err)
	}
	if _, err := f.svc.Board(context.Background(), projectID); err != nil {
		t.Fatalf("Board with the kept token: %v", err)
	}
}

func TestTestChecksWithoutSaving(t *testing.T) {
	f := newFixture(t)
	res, err := f.svc.Test(context.Background(), projectID, f.input())
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if res.AccountName != "Ada Example" || res.ProjectName != "Yoobi" || res.TicketCount != 3 || !strings.Contains(res.JQL, `project = "YIA"`) {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "jira-connections.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Test must not store anything: %v", err)
	}
}

func TestBadJQLIsReportedAndTheTokenPastedIntoItIsRedacted(t *testing.T) {
	f := newFixture(t)
	in := f.input()
	in.JQL = "BROKEN " + f.jira.Token
	_, err := f.svc.Test(context.Background(), projectID, in)
	if !errors.Is(err, jirasvc.ErrBadQuery) {
		t.Fatalf("err = %v, want ErrBadQuery", err)
	}
	if strings.Contains(err.Error(), f.jira.Token) {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestBoardGroupsTicketsIntoTheJiraBoardColumns(t *testing.T) {
	f := newFixture(t)
	f.connect()
	board, err := f.svc.Board(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if board.BoardName != "YIA board" || board.AccountID != "acc-1" || board.Total != 3 || board.Truncated {
		t.Fatalf("board = %+v", board)
	}
	names := make([]string, len(board.Columns))
	for i, c := range board.Columns {
		names[i] = c.Name
	}
	if got := strings.Join(names, "|"); got != "To Do|In Progress|Done|Other" {
		t.Fatalf("columns = %s", got)
	}
	todo := board.Columns[0].Tickets
	if len(todo) != 1 || todo[0].Key != "YIA-10" || todo[0].URL != f.jira.URL+"/browse/YIA-10" || todo[0].Assignee == nil || todo[0].Assignee.DisplayName != "Ada Example" {
		t.Fatalf("To Do = %+v", todo)
	}
	if todo[0].AcceptanceCriteria == "" || todo[0].Description == "" {
		t.Fatalf("ticket text missing: %+v", todo[0])
	}
	if len(board.Columns[2].Tickets) != 0 {
		t.Fatalf("Done should be an empty column, got %+v", board.Columns[2].Tickets)
	}
	if !board.Columns[3].Other || board.Columns[3].Tickets[0].Key != "YIA-12" {
		t.Fatalf("unmapped status should land in Other: %+v", board.Columns[3])
	}
	// The default query covers open work plus what finished this week.
	if !strings.Contains(board.JQL, `project = "YIA"`) || !strings.Contains(board.JQL, "statusCategory != Done") {
		t.Fatalf("jql = %q", board.JQL)
	}
}

func TestBoardWithoutJiraSoftwareGroupsByStatusCategory(t *testing.T) {
	f := newFixture(t)
	f.jira.Columns = nil // no boards on this site
	f.jira.Issues = append(f.jira.Issues, jiratest.Issue{Key: "YIA-13", Summary: "Shipped", StatusID: "5", StatusName: "Done", StatusCategory: "done"})
	f.connect()
	board, err := f.svc.Board(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if board.BoardName != "" {
		t.Fatalf("no board expected, got %q", board.BoardName)
	}
	names := make([]string, len(board.Columns))
	for i, c := range board.Columns {
		names[i] = c.Name
	}
	if got := strings.Join(names, "|"); got != "Parked|To Do|In Progress|Done" {
		t.Fatalf("columns = %s (want category order, then name)", got)
	}
}

func TestBoardNotConnectedAndOffline(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Board(context.Background(), projectID)
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeNotConnected {
		t.Fatalf("err = %v, want JIRA_NOT_CONNECTED", err)
	}

	f.connect()
	f.jira.Close()
	_, err = f.svc.Board(context.Background(), projectID)
	if !errors.Is(err, jirasvc.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestBoardTokenRevokedAfterConnect(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.jira.Token = "rotated-on-the-server"
	_, err := f.svc.Board(context.Background(), projectID)
	if !errors.Is(err, jirasvc.ErrAuthFailed) {
		t.Fatalf("err = %v, want ErrAuthFailed", err)
	}
}

func TestStartSendsTheTicketToTheExistingOrchestratorAndLinksIt(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"

	res, err := f.svc.Start(context.Background(), projectID, "yia-10")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.IssueKey != "YIA-10" || res.OrchestratorID != "orch-1" || res.Spawned || res.Duplicate {
		t.Fatalf("result = %+v", res)
	}
	if len(f.orch.spawned) != 0 || len(f.orch.sent) != 1 || f.orch.sent[0].session != "orch-1" {
		t.Fatalf("spawned %v, sent %+v", f.orch.spawned, f.orch.sent)
	}
	brief := f.orch.sent[0].text
	for _, want := range []string{
		"Jira ticket YIA-10: Add CSV export",
		f.jira.URL + "/browse/YIA-10",
		"Type: Story", "Priority: High", "Status: To Do", "Assignee: Ada Example",
		"## Description", "Users want CSV export.", "Keep it small.",
		"## Acceptance criteria", "- exports all rows", "- has a test",
		"ao spawn --issue jira:YIA-10",
		"do not comment on it in Jira",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief is missing %q:\n%s", want, brief)
		}
	}
	if strings.Contains(brief, f.jira.Token) {
		t.Fatalf("brief leaks the token")
	}

	board, err := f.svc.Board(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	linked := board.Columns[0].Tickets[0].Sessions
	if len(linked) != 1 || linked[0].SessionID != "orch-1" {
		t.Fatalf("ticket sessions = %+v", linked)
	}
}

func TestStartSpawnsAnOrchestratorWhenTheProjectHasNone(t *testing.T) {
	f := newFixture(t)
	f.connect()
	res, err := f.svc.Start(context.Background(), projectID, "YIA-11")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !res.Spawned || res.OrchestratorID != "orch-new" || len(f.orch.spawned) != 1 || len(f.orch.sent) != 1 {
		t.Fatalf("result = %+v, spawned %v, sent %+v", res, f.orch.spawned, f.orch.sent)
	}
	if !strings.Contains(f.orch.sent[0].text, "(The ticket has no description.)") {
		t.Fatalf("empty description is not called out:\n%s", f.orch.sent[0].text)
	}
}

func TestStartIgnoresADoubleClick(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	if _, err := f.svc.Start(context.Background(), projectID, "YIA-10"); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	f.clock = f.clock.Add(3 * time.Second)
	res, err := f.svc.Start(context.Background(), projectID, "YIA-10")
	if err != nil || !res.Duplicate || len(f.orch.sent) != 1 {
		t.Fatalf("second Start = %+v, %v; sent %d", res, err, len(f.orch.sent))
	}
	f.clock = f.clock.Add(time.Minute)
	if res, err = f.svc.Start(context.Background(), projectID, "YIA-10"); err != nil || res.Duplicate || len(f.orch.sent) != 2 {
		t.Fatalf("a later Start should send again: %+v, %v; sent %d", res, err, len(f.orch.sent))
	}
}

func TestStartRejectsBadKeysMissingTicketsAndStaysReadOnly(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	for _, key := range []string{"", "10", "yia", "YIA-10; DROP", "../../etc"} {
		_, err := f.svc.Start(context.Background(), projectID, key)
		var apiErr *apierr.Error
		if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeInvalidInput {
			t.Errorf("key %q: err = %v, want JIRA_INVALID_INPUT", key, err)
		}
	}
	_, err := f.svc.Start(context.Background(), projectID, "YIA-404")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeNotFound {
		t.Fatalf("missing ticket: err = %v", err)
	}
	if len(f.orch.sent) != 0 {
		t.Fatalf("nothing should reach the orchestrator, sent %+v", f.orch.sent)
	}
	// Read-only toward Jira: only GETs, plus the search POST.
	for _, r := range f.jira.Requests() {
		if r.Method != http.MethodGet && (r.Method != http.MethodPost || r.Path != "/rest/api/3/search/jql") {
			t.Fatalf("service wrote to Jira: %+v", r)
		}
	}
}

func TestStartFailureLeavesNoLink(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	f.orch.sendErr = errors.New("terminal closed")
	if _, err := f.svc.Start(context.Background(), projectID, "YIA-10"); err == nil {
		t.Fatal("expected the send failure")
	}
	f.orch.sendErr = nil
	board, _ := f.svc.Board(context.Background(), projectID)
	if len(board.Columns[0].Tickets[0].Sessions) != 0 {
		t.Fatalf("a failed send left a link: %+v", board.Columns[0].Tickets[0].Sessions)
	}
}

func TestDisconnectDropsTicketLinks(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	if _, err := f.svc.Start(context.Background(), projectID, "YIA-10"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.svc.Disconnect(context.Background(), projectID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	f.connect()
	board, _ := f.svc.Board(context.Background(), projectID)
	if len(board.Columns[0].Tickets[0].Sessions) != 0 {
		t.Fatalf("links survived Disconnect: %+v", board.Columns[0].Tickets[0].Sessions)
	}
}

func TestConnectionsAreScopedPerProject(t *testing.T) {
	f := newFixture(t)
	f.svc = jirasvc.New(jirasvc.Deps{DataDir: f.dir, Projects: fakeProjects{projectID: true, "other": true}, Orchestrators: f.orch})
	f.connect()
	if st, _ := f.svc.Status(context.Background(), "other"); st.Connected {
		t.Fatalf("another project inherited the connection: %+v", st)
	}
}

// A saved token belongs to the site and account it was saved for. Leaving the
// token blank must never send it anywhere else.
func TestBlankTokenIsNeverSentToAnotherSiteOrAccount(t *testing.T) {
	f := newFixture(t)
	f.connect()
	other := jiratest.New()
	defer other.Close()

	for name, mutate := range map[string]func(*jirasvc.ConnectInput){
		"another site":                func(in *jirasvc.ConnectInput) { in.SiteURL = other.URL },
		"another site and email":      func(in *jirasvc.ConnectInput) { in.SiteURL = other.URL; in.Email = other.Email },
		"another account, same site":  func(in *jirasvc.ConnectInput) { in.Email = "someone.else@example.com" },
		"another port on the same IP": func(in *jirasvc.ConnectInput) { in.SiteURL = strings.Replace(f.jira.URL, "127.0.0.1", "localhost", 1) },
	} {
		for _, action := range []string{"test", "connect"} {
			in := f.input()
			in.APIToken = ""
			mutate(&in)
			var err error
			if action == "test" {
				_, err = f.svc.Test(context.Background(), projectID, in)
			} else {
				_, err = f.svc.Connect(context.Background(), projectID, in)
			}
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeInvalidInput {
				t.Errorf("%s via %s: err = %v, want JIRA_INVALID_INPUT", name, action, err)
			}
		}
	}
	if reqs := other.Requests(); len(reqs) != 0 {
		t.Fatalf("the saved token reached another host: %+v", reqs)
	}
	for _, r := range f.jira.Requests() {
		if r.Path == "/rest/api/3/myself" && strings.Contains(r.Query, "localhost") {
			t.Fatalf("unexpected request %+v", r)
		}
	}

	// The stored connection is untouched by the refused attempts.
	if st, _ := f.svc.Status(context.Background(), projectID); st.SiteURL != f.jira.URL || st.Email != f.jira.Email {
		t.Fatalf("connection changed by a refused request: %+v", st)
	}
}

func TestBlankTokenIsKeptForTheSameSiteAndAccountHoweverTheyAreWritten(t *testing.T) {
	f := newFixture(t)
	f.connect()
	in := f.input()
	in.APIToken = ""
	in.SiteURL = f.jira.URL + "/"
	in.Email = strings.ToUpper(f.jira.Email)
	if _, err := f.svc.Connect(context.Background(), projectID, in); err != nil {
		t.Fatalf("same site and account written differently: %v", err)
	}
	// A new token for another site is fine: it comes from the person.
	other := jiratest.New()
	defer other.Close()
	in = jirasvc.ConnectInput{SiteURL: other.URL, Email: other.Email, APIToken: other.Token, ProjectKey: "YIA"}
	if _, err := f.svc.Connect(context.Background(), projectID, in); err != nil {
		t.Fatalf("new site with its own token: %v", err)
	}
	if st, _ := f.svc.Status(context.Background(), projectID); st.SiteURL != other.URL {
		t.Fatalf("status = %+v", st)
	}
}

// Two requests for one ticket (a double click, or the desktop and the phone)
// must send its brief once.
func TestConcurrentStartsSendTheBriefOnce(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	f.orch.gate = make(chan struct{})

	const callers = 8
	results := make([]jirasvc.StartResult, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = f.svc.Start(context.Background(), projectID, "YIA-10")
		}()
	}
	// Hold the first send until the others have had every chance to start theirs.
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.orch.mu.Lock()
		inSend := f.orch.inSend
		f.orch.mu.Unlock()
		if inSend >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(f.orch.gate)
	wg.Wait()

	if n := f.orch.sentCount(); n != 1 {
		t.Fatalf("the brief was sent %d times, want once", n)
	}
	fresh := 0
	for i, res := range results {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if res.OrchestratorID != "orch-1" || res.IssueKey != "YIA-10" {
			t.Fatalf("caller %d got %+v", i, res)
		}
		if !res.Duplicate {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("%d callers were told they sent it, want exactly one", fresh)
	}
	board, _ := f.svc.Board(context.Background(), projectID)
	if got := len(board.Columns[0].Tickets[0].Sessions); got != 1 {
		t.Fatalf("ticket has %d links, want 1", got)
	}
}

func TestDifferentTicketsAreNotHeldBackByEachOther(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	var wg sync.WaitGroup
	for _, key := range []string{"YIA-10", "YIA-11"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.svc.Start(context.Background(), projectID, key); err != nil {
				t.Errorf("%s: %v", key, err)
			}
		}()
	}
	wg.Wait()
	if n := f.orch.sentCount(); n != 2 {
		t.Fatalf("sent %d briefs, want 2", n)
	}
}

// A send that fails must not leave the ticket reserved.
func TestFailedSendIsRetriedAtOnce(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	f.orch.sendErr = errors.New("terminal closed")
	if _, err := f.svc.Start(context.Background(), projectID, "YIA-10"); err == nil {
		t.Fatal("expected the send failure")
	}
	f.orch.mu.Lock()
	f.orch.sendErr = nil
	f.orch.mu.Unlock()
	res, err := f.svc.Start(context.Background(), projectID, "YIA-10")
	if err != nil || res.Duplicate || f.orch.sentCount() != 1 {
		t.Fatalf("retry = %+v, %v; sent %d", res, err, f.orch.sentCount())
	}
}

func TestStartRefusesATicketOutsideTheProjectUnlessTheJQLIsTheirOwn(t *testing.T) {
	f := newFixture(t)
	f.jira.Issues = append(f.jira.Issues, jiratest.Issue{Key: "ABC-5", Summary: "Another project", StatusID: "1", StatusName: "To Do", StatusCategory: "new", Type: "Task", Updated: "2026-11-01T10:00:00.000+0000"})
	f.connect()
	f.orch.active = "orch-1"
	_, err := f.svc.Start(context.Background(), projectID, "ABC-5")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Code != jirasvc.CodeInvalidInput {
		t.Fatalf("err = %v, want JIRA_INVALID_INPUT", err)
	}
	for _, r := range f.jira.Requests() {
		if strings.Contains(r.Path, "/issue/ABC-5") {
			t.Fatalf("the out-of-project ticket was read from Jira: %+v", r)
		}
	}
	if f.orch.sentCount() != 0 {
		t.Fatal("an out-of-project ticket was sent")
	}

	// With the person's own JQL the scope is theirs.
	in := f.input()
	in.JQL = "project in (YIA, ABC)"
	if _, err := f.svc.Connect(context.Background(), projectID, in); err != nil {
		t.Fatalf("Connect with JQL: %v", err)
	}
	if res, err := f.svc.Start(context.Background(), projectID, "ABC-5"); err != nil || res.Duplicate {
		t.Fatalf("Start under a custom JQL = %+v, %v", res, err)
	}
}

// If the link cannot be saved after the brief was sent, Start still succeeds and
// a quick retry is a duplicate, not a second send.
func TestStartKeepsTheDuplicateGuardWhenTheLinkCannotBeSaved(t *testing.T) {
	f := newFixture(t)
	f.connect()
	f.orch.active = "orch-1"
	// A directory where the links file belongs makes reading and writing it fail.
	if err := os.Mkdir(filepath.Join(f.dir, "jira-links.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.Start(context.Background(), projectID, "YIA-10")
	if err != nil || res.OrchestratorID != "orch-1" || len(f.orch.sent) != 1 {
		t.Fatalf("Start = %+v, %v; sent %d", res, err, len(f.orch.sent))
	}
	res, err = f.svc.Start(context.Background(), projectID, "YIA-10")
	if err != nil || !res.Duplicate || len(f.orch.sent) != 1 {
		t.Fatalf("retry = %+v, %v; sent %d", res, err, len(f.orch.sent))
	}
}

const defaultAssignedJQL = "assignee = currentUser() AND (statusCategory != Done OR updated >= -14d) ORDER BY updated DESC"

func TestEffectiveJQL(t *testing.T) {
	cases := []struct{ name, key, custom, want string }{
		{"no key and no jql is everything assigned to me", "", "", defaultAssignedJQL},
		{"blank key is the same", "  ", " ", defaultAssignedJQL},
		{"a key scopes to that project", "YIA", "", `project = "YIA" AND (statusCategory != Done OR resolutiondate >= -7d) ORDER BY updated DESC`},
		{"custom jql wins without a key", "", " project = ABC ORDER BY rank ", "project = ABC ORDER BY rank"},
		{"custom jql wins over a key", "YIA", "assignee = EMPTY", "assignee = EMPTY"},
	}
	for _, c := range cases {
		if got := jirasvc.EffectiveJQL(c.key, c.custom); got != c.want {
			t.Errorf("%s: EffectiveJQL(%q, %q) = %q, want %q", c.name, c.key, c.custom, got, c.want)
		}
	}
}

func lastSearchJQL(t *testing.T, f *fixture) string {
	t.Helper()
	reqs := f.jira.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		if strings.HasSuffix(reqs[i].Path, "/search/jql") {
			var body struct {
				JQL string `json:"jql"`
			}
			if err := json.Unmarshal([]byte(reqs[i].Body), &body); err != nil {
				t.Fatalf("search body: %v", err)
			}
			return body.JQL
		}
	}
	t.Fatal("no search request reached Jira")
	return ""
}

func TestBoardWithoutAProjectKeyShowsEverythingAssignedToMeGroupedByStatus(t *testing.T) {
	f := newFixture(t)
	f.jira.Columns = nil // no Jira board is looked up without a project key
	f.jira.Issues = []jiratest.Issue{
		{Key: "WEB-3", Summary: "Shipped", StatusID: "5", StatusName: "Done", StatusCategory: "done", AssigneeID: "acc-1", AssigneeName: "Ada Example"},
		{Key: "API-7", Summary: "Review", StatusID: "3", StatusName: "In Review", StatusCategory: "indeterminate", AssigneeID: "acc-1", AssigneeName: "Ada Example"},
		{Key: "YIA-1", Summary: "Start", StatusID: "1", StatusName: "To Do", StatusCategory: "new", AssigneeID: "acc-1", AssigneeName: "Ada Example"},
		{Key: "API-8", Summary: "Plan", StatusID: "2", StatusName: "Backlog", StatusCategory: "new", AssigneeID: "acc-1", AssigneeName: "Ada Example"},
		{Key: "WEB-4", Summary: "Build", StatusID: "3", StatusName: "In Progress", StatusCategory: "indeterminate", AssigneeID: "acc-1", AssigneeName: "Ada Example"},
	}
	in := f.input()
	in.ProjectKey = ""
	if _, err := f.svc.Connect(context.Background(), projectID, in); err != nil {
		t.Fatalf("Connect without a project key: %v", err)
	}
	board, err := f.svc.Board(context.Background(), projectID)
	if err != nil {
		t.Fatalf("Board: %v", err)
	}
	if got := lastSearchJQL(t, f); got != defaultAssignedJQL || board.JQL != defaultAssignedJQL {
		t.Fatalf("jql sent = %q, board = %q", got, board.JQL)
	}
	names := make([]string, len(board.Columns))
	for i, c := range board.Columns {
		names[i] = c.Name
	}
	// To Do category first, then In Progress, then Done; by name inside a category.
	if got := strings.Join(names, "|"); got != "Backlog|To Do|In Progress|In Review|Done" {
		t.Fatalf("columns = %s", got)
	}
	if board.Total != 5 || board.BoardName != "" {
		t.Fatalf("board = %+v", board)
	}
}

func TestBoardKeepsACustomJQLWithoutAProjectKey(t *testing.T) {
	f := newFixture(t)
	in := f.input()
	in.ProjectKey = ""
	in.JQL = "project = ABC ORDER BY rank"
	if _, err := f.svc.Connect(context.Background(), projectID, in); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := f.svc.Board(context.Background(), projectID); err != nil {
		t.Fatalf("Board: %v", err)
	}
	if got := lastSearchJQL(t, f); got != "project = ABC ORDER BY rank" {
		t.Fatalf("custom jql was replaced: %q", got)
	}
}

func TestStartWithoutAProjectKeyAcceptsATicketOfAnyJiraProject(t *testing.T) {
	f := newFixture(t)
	f.jira.Issues = append(f.jira.Issues, jiratest.Issue{Key: "WEB-3", Summary: "Other project", StatusID: "1", StatusName: "To Do", StatusCategory: "new"})
	in := f.input()
	in.ProjectKey = ""
	if _, err := f.svc.Connect(context.Background(), projectID, in); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	res, err := f.svc.Start(context.Background(), projectID, "WEB-3")
	if err != nil || res.IssueKey != "WEB-3" {
		t.Fatalf("Start = %+v, %v", res, err)
	}
}

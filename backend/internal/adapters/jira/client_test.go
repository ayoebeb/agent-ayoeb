package jira_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/jira"
	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/jira/jiratest"
)

func newClient(t *testing.T, srv *jiratest.Server) *jira.Client {
	t.Helper()
	return jira.New(jira.Config{SiteURL: srv.URL, Email: srv.Email, Token: srv.Token})
}

func sampleIssues() []jiratest.Issue {
	return []jiratest.Issue{
		{Key: "YIA-1", Summary: "Add export button", Description: "Users want CSV export.\nKeep it small.", StatusID: "1", StatusName: "To Do", StatusCategory: "new", AssigneeID: "acc-1", AssigneeName: "Ada Example", Type: "Story", Priority: "High", Labels: []string{"export"}, Updated: "2026-11-10T09:30:00.000+0100"},
		{Key: "YIA-2", Summary: "Fix login redirect", Description: "", StatusID: "3", StatusName: "In Progress", StatusCategory: "indeterminate", Type: "Bug", Priority: "Medium", Updated: "2026-11-11T10:00:00.000+0000"},
	}
}

func TestMyselfSendsBasicAuthAndReturnsAccount(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()

	user, err := newClient(t, srv).Myself(context.Background())
	if err != nil {
		t.Fatalf("Myself: %v", err)
	}
	if user.AccountID != "acc-1" || user.DisplayName != "Ada Example" {
		t.Fatalf("user = %+v", user)
	}
	reqs := srv.Requests()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("dev@example.com:fake-api-token"))
	if len(reqs) != 1 || reqs[0].Auth != want {
		t.Fatalf("requests = %+v, want one with Authorization %q", reqs, want)
	}
}

func TestWrongTokenIsAuthError(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	client := jira.New(jira.Config{SiteURL: srv.URL, Email: srv.Email, Token: "nope"})

	_, err := client.Myself(context.Background())
	if !errors.Is(err, jira.ErrAuth) {
		t.Fatalf("err = %v, want ErrAuth", err)
	}
	if strings.Contains(err.Error(), "nope") {
		t.Fatalf("error leaks the token: %v", err)
	}
}

func TestUnreachableSiteIsReported(t *testing.T) {
	srv := jiratest.New()
	url := srv.URL
	srv.Close() // nothing listens any more

	_, err := jira.New(jira.Config{SiteURL: url, Email: "a@b.c", Token: "tok"}).Myself(context.Background())
	if !errors.Is(err, jira.ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
}

func TestServerErrorIsUpstream(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	srv.Status = 503

	_, err := newClient(t, srv).Myself(context.Background())
	if !errors.Is(err, jira.ErrUpstream) {
		t.Fatalf("err = %v, want ErrUpstream", err)
	}
}

func TestProjectNameAndMissingProject(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	client := newClient(t, srv)

	name, err := client.ProjectName(context.Background(), "YIA")
	if err != nil || name != "Yoobi" {
		t.Fatalf("ProjectName = %q, %v", name, err)
	}
	if _, err := client.ProjectName(context.Background(), "NOPE"); !errors.Is(err, jira.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSearchFlattensTicketsAndSendsJQL(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	srv.Issues = sampleIssues()

	issues, truncated, err := newClient(t, srv).Search(context.Background(), `project = "YIA" ORDER BY updated DESC`, 50)
	if err != nil || truncated {
		t.Fatalf("Search = truncated %v, err %v", truncated, err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues", len(issues))
	}
	first := issues[0]
	if first.Key != "YIA-1" || first.Summary != "Add export button" || first.Status.Name != "To Do" || first.Status.Category != "new" {
		t.Fatalf("first = %+v", first)
	}
	if first.Description != "Users want CSV export.\n\nKeep it small." {
		t.Fatalf("description = %q", first.Description)
	}
	if first.Assignee == nil || first.Assignee.DisplayName != "Ada Example" || first.IssueType != "Story" || first.Priority != "High" {
		t.Fatalf("first = %+v", first)
	}
	if first.Updated.IsZero() || first.Updated.Hour() != 8 { // 09:30+01:00 is 08:30 UTC
		t.Fatalf("updated = %v", first.Updated)
	}
	if issues[1].Assignee != nil {
		t.Fatalf("unassigned ticket has assignee %+v", issues[1].Assignee)
	}

	var search *jiratest.Request
	for _, r := range srv.Requests() {
		if r.Path == "/rest/api/3/search/jql" {
			r := r
			search = &r
		}
	}
	if search == nil || search.Method != "POST" || !strings.Contains(search.Body, `project = \"YIA\" ORDER BY updated DESC`) {
		t.Fatalf("search request = %+v", search)
	}
}

func TestSearchFollowsPagesAndReportsTruncation(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	srv.PageSize = 2
	for i := 1; i <= 5; i++ {
		srv.Issues = append(srv.Issues, jiratest.Issue{Key: "YIA-" + string(rune('0'+i)), Summary: "t", StatusID: "1", StatusName: "To Do", StatusCategory: "new"})
	}

	all, truncated, err := newClient(t, srv).Search(context.Background(), "project = YIA", 100)
	if err != nil || truncated || len(all) != 5 {
		t.Fatalf("all: len %d truncated %v err %v", len(all), truncated, err)
	}
	few, truncated, err := newClient(t, srv).Search(context.Background(), "project = YIA", 3)
	if err != nil || !truncated || len(few) != 3 {
		t.Fatalf("few: len %d truncated %v err %v", len(few), truncated, err)
	}
}

func TestSearchRejectedJQLIsBadQuery(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()

	_, _, err := newClient(t, srv).Search(context.Background(), "BROKEN ===", 10)
	if !errors.Is(err, jira.ErrBadQuery) || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v, want ErrBadQuery with Jira's message", err)
	}
}

func TestAcceptanceCriteriaFromCustomFieldAndDescriptionSection(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	srv.ACField = "customfield_10042"
	srv.Issues = []jiratest.Issue{
		{Key: "YIA-1", Summary: "A", Description: "Body", AcceptanceText: "- it exports\n- it is tested", StatusID: "1", StatusName: "To Do", StatusCategory: "new"},
	}
	issue, err := newClient(t, srv).Issue(context.Background(), "YIA-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if issue.AcceptanceCriteria != "- it exports\n\n- it is tested" || issue.Description != "Body" {
		t.Fatalf("issue = %+v", issue)
	}

	// No custom field: the section is cut out of the description.
	srv2 := jiratest.New()
	defer srv2.Close()
	srv2.Issues = []jiratest.Issue{
		{Key: "YIA-2", Summary: "B", Description: "Intro text\n## Acceptance criteria\n- one\n- two\n## Notes\nlater", StatusID: "1", StatusName: "To Do", StatusCategory: "new"},
	}
	issue, err = newClient(t, srv2).Issue(context.Background(), "YIA-2")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !strings.Contains(issue.AcceptanceCriteria, "- one") || strings.Contains(issue.Description, "- one") || !strings.Contains(issue.Description, "later") {
		t.Fatalf("issue = %+v", issue)
	}
}

func TestIssueNotFound(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	if _, err := newClient(t, srv).Issue(context.Background(), "YIA-404"); !errors.Is(err, jira.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestBoardDiscoveryAndColumns(t *testing.T) {
	srv := jiratest.New()
	defer srv.Close()
	srv.Columns = []jiratest.Column{{Name: "To Do", StatusIDs: []string{"1"}}, {Name: "Doing", StatusIDs: []string{"3", "4"}}, {Name: "Done", StatusIDs: []string{"5"}}}
	client := newClient(t, srv)

	id, name, err := client.FindBoard(context.Background(), "YIA")
	if err != nil || id != 7 || name != "YIA board" {
		t.Fatalf("FindBoard = %d %q %v", id, name, err)
	}
	board, err := client.BoardColumns(context.Background(), id, name)
	if err != nil {
		t.Fatalf("BoardColumns: %v", err)
	}
	if len(board.Columns) != 3 || board.Columns[1].Name != "Doing" || strings.Join(board.Columns[1].StatusIDs, ",") != "3,4" {
		t.Fatalf("board = %+v", board)
	}

	noBoards := jiratest.New()
	defer noBoards.Close()
	if _, _, err := newClient(t, noBoards).FindBoard(context.Background(), "YIA"); !errors.Is(err, jira.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for a site without boards", err)
	}
}

func TestNormalizeSiteURL(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{"yoobi.atlassian.net", "https://yoobi.atlassian.net", false},
		{"https://yoobi.atlassian.net/", "https://yoobi.atlassian.net", false},
		{" https://yoobi.atlassian.net/jira/software/projects/YIA/boards/1 ", "https://yoobi.atlassian.net", false},
		{"http://127.0.0.1:5555", "http://127.0.0.1:5555", false},
		{"http://localhost:8080/x", "http://localhost:8080", false},
		{"http://yoobi.atlassian.net", "", true},
		{"ftp://yoobi.atlassian.net", "", true},
		{"https://user:pass@yoobi.atlassian.net", "", true},
		{"", "", true},
		{"https://", "", true},
	}
	for _, c := range cases {
		got, err := jira.NormalizeSiteURL(c.in)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("NormalizeSiteURL(%q) = %q, %v; want %q (err %v)", c.in, got, err, c.want, c.wantErr)
		}
	}
}

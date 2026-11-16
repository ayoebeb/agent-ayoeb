package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// approvalsCall is one request the stub daemon saw.
type approvalsCall struct {
	method string
	uri    string // the raw request URI, so path encoding can be asserted
	path   string // the decoded path
	body   string
}

// stubReply is what the stub daemon answers. A zero status is 200, and an empty
// body sends none (a 204 carries no body).
type stubReply struct {
	status int
	body   string
}

// startApprovalsDaemon serves reply(r) for every daemon call except telemetry,
// and records the calls. The CLI is pointed at it through the run file.
func startApprovalsDaemon(t *testing.T, reply func(c approvalsCall) stubReply) *[]approvalsCall {
	t.Helper()
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "")
	calls := &[]approvalsCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal/telemetry/cli-invoked" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		body, _ := io.ReadAll(r.Body)
		call := approvalsCall{method: r.Method, uri: r.RequestURI, path: r.URL.Path, body: string(body)}
		*calls = append(*calls, call)
		got := reply(call)
		if got.status == 0 {
			got.status = http.StatusOK
		}
		if got.body != "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(got.status)
		_, _ = io.WriteString(w, got.body)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	return calls
}

func runApprovals(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, append([]string{"session", "approvals"}, args...)...)
	return out, err
}

func conversationReply(snapshot string) func(c approvalsCall) stubReply {
	return func(c approvalsCall) stubReply {
		if strings.HasSuffix(c.path, "/conversation") {
			return stubReply{body: snapshot}
		}
		return stubReply{status: http.StatusNotFound, body: `{"code":"NOT_FOUND","message":"no route"}`}
	}
}

const claudeDecisions = `[{"id":"allow","label":"Allow","kind":"allow_once"},{"id":"allow_always","label":"Always allow","kind":"allow_always"},{"id":"reject","label":"Reject","kind":"reject_once"}]`

// pendingSnapshot holds, out of sequence order, a pending approval, a pending
// form input, a resolved approval, and a plain command the list must ignore.
var pendingSnapshot = `{"conversationId":"cnv","controller":"busy","latestSequence":9,"turns":[],"messages":[],"activities":[
 {"kind":"activity","id":"a4","sequence":8,"turnId":"t1","activityKind":"user_input","status":"pending","requestId":"acp-request:h:3","summary":"Which database?","createdAt":"2026-11-16T10:01:00Z",
  "detail":{"inputMode":"form","message":"Which database?","schema":{"type":"object","properties":{"db":{"type":"string","enum":["pg","sqlite"]}},"required":["db"]}}},
 {"kind":"activity","id":"a3","sequence":7,"turnId":"t1","activityKind":"approval","status":"pending","requestId":"acp-request:h:2","summary":"Run ls","createdAt":"2026-11-16T10:00:00Z",
  "detail":{"protocol":"acp","toolKind":"execute","toolName":"Bash","input":{"command":"ls -la && echo done","description":"List files"},"decisions":` + claudeDecisions + `}},
 {"kind":"activity","id":"a2","sequence":5,"activityKind":"approval","status":"resolved","requestId":"acp-request:h:1","summary":"Old","detail":{"decisions":` + claudeDecisions + `}},
 {"kind":"activity","id":"a1","sequence":3,"activityKind":"command","status":"completed","summary":"echo hi","detail":{"command":"echo hi"}}
]}`

func TestSessionApprovalsList_HumanOutput(t *testing.T) {
	calls := startApprovalsDaemon(t, conversationReply(pendingSnapshot))

	out, err := runApprovals(t, "ls", "s-1")
	if err != nil {
		t.Fatalf("ls failed: %v", err)
	}
	if got := (*calls)[0]; got.method != "GET" || got.uri != "/api/v1/sessions/s-1/conversation?limit=500" {
		t.Fatalf("call = %+v", got)
	}
	for _, want := range []string{
		"session s-1: 2 pending requests",
		"request: acp-request:h:2",
		"kind:       approval",
		"tool:       Bash",
		"summary:    Run ls",
		`input:      {"command":"ls -la && echo done","description":"List files"}`,
		"decisions:  allow (Allow), allow_always (Always allow), reject (Reject)",
		"request: acp-request:h:3",
		"kind:       input (form)",
		"summary:    Which database?",
		`schema:     {"properties":{"db":{"enum":["pg","sqlite"],"type":"string"}},"required":["db"],"type":"object"}`,
		"actions:    accept, decline, cancel",
		"Answer with: ao session approvals resolve s-1 <request-id> --decision <id> (approval) or --action <action> (input)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "acp-request:h:1") || strings.Contains(out, "echo hi") {
		t.Fatalf("a resolved approval or an unrelated activity was listed:\n%s", out)
	}
	if strings.Index(out, "acp-request:h:2") > strings.Index(out, "acp-request:h:3") {
		t.Fatalf("requests are not oldest first:\n%s", out)
	}
}

func TestSessionApprovalsList_JSON(t *testing.T) {
	startApprovalsDaemon(t, conversationReply(pendingSnapshot))

	out, err := runApprovals(t, "ls", "s-1", "--json")
	if err != nil {
		t.Fatalf("ls --json failed: %v", err)
	}
	var got approvalsListOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	want := approvalsListOutput{SessionID: "s-1", Pending: []pendingRequest{
		{
			RequestID: "acp-request:h:2", Kind: "approval", Sequence: 7, TurnID: "t1", CreatedAt: "2026-11-16T10:00:00Z",
			Tool: "Bash", Summary: "Run ls", Input: `{"command":"ls -la && echo done","description":"List files"}`,
			Decisions: []pendingDecision{
				{ID: "allow", Label: "Allow", Kind: "allow_once"},
				{ID: "allow_always", Label: "Always allow", Kind: "allow_always"},
				{ID: "reject", Label: "Reject", Kind: "reject_once"},
			},
		},
		{
			RequestID: "acp-request:h:3", Kind: "input", Sequence: 8, TurnID: "t1", CreatedAt: "2026-11-16T10:01:00Z",
			Summary: "Which database?", InputMode: "form", Actions: []string{"accept", "decline", "cancel"},
			Schema: map[string]any{
				"type": "object", "required": []any{"db"},
				"properties": map[string]any{"db": map[string]any{"type": "string", "enum": []any{"pg", "sqlite"}}},
			},
		},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ls --json =\n%+v\nwant\n%+v", got, want)
	}
}

func TestSessionApprovalsList_NothingPending(t *testing.T) {
	startApprovalsDaemon(t, conversationReply(`{"conversationId":"cnv","turns":[],"messages":[],"activities":[
		{"kind":"activity","id":"a1","sequence":1,"activityKind":"approval","status":"resolved","requestId":"r1","summary":"done"}]}`))

	out, err := runApprovals(t, "ls", "s-1")
	if err != nil || !strings.Contains(out, "session s-1: no pending approvals or input requests") {
		t.Fatalf("human: out=%q err=%v", out, err)
	}
	out, err = runApprovals(t, "ls", "s-1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	// An empty list stays [] so a script can range over it.
	if !strings.Contains(out, `"pending": []`) {
		t.Fatalf("json: %s", out)
	}
}

// TestPendingRequestsFrom_Summaries covers how a request's tool and input are
// read from the shapes the Claude and Codex drivers store, and that nothing the
// worker wrote can break the layout.
func TestPendingRequestsFrom_Summaries(t *testing.T) {
	long := strings.Repeat("x", approvalInputMaxChars+60)
	for _, tt := range []struct {
		name        string
		activity    string
		wantTool    string
		wantInput   string
		wantSummary string
		wantFullCmd string
	}{
		{
			name:        "claude tool name and structured input",
			activity:    `"summary":"Run it","detail":{"toolName":"Bash","toolKind":"execute","input":{"command":"a > b && c"}}`,
			wantTool:    "Bash",
			wantInput:   `{"command":"a > b && c"}`,
			wantSummary: "Run it",
		},
		{
			name:        "provider tool name wins over tool kind",
			activity:    `"summary":"s","detail":{"providerToolName":"mcp__x__y","toolKind":"other","input":"raw text"}`,
			wantTool:    "mcp__x__y",
			wantInput:   "raw text",
			wantSummary: "s",
		},
		{
			name:        "codex command approval",
			activity:    `"summary":"Run npm test","detail":{"method":"item/commandExecution/requestApproval","command":"npm test","cwd":"/w"}`,
			wantTool:    "item/commandExecution/requestApproval",
			wantInput:   "npm test",
			wantSummary: "Run npm test",
		},
		{
			name:        "tool kind when no name",
			activity:    `"summary":"Edit file","detail":{"toolKind":"edit"}`,
			wantTool:    "edit",
			wantSummary: "Edit file",
		},
		{
			name:        "no detail at all",
			activity:    `"summary":"Permission required"`,
			wantSummary: "Permission required",
		},
		{
			name:        "terminal control sequences and newlines are removed",
			activity:    "\"summary\":\"line one\\n\\u001b[31mred\\u001b[0m\\u0007 two\",\"detail\":{\"toolName\":\"Ba\\u001b[2Jsh\",\"input\":\"echo a\\n\\n  echo b\"}",
			wantTool:    "Bash",
			wantInput:   "echo a echo b",
			wantSummary: "line one red two",
		},
		{
			name:        "long input is cut and points at the full text",
			activity:    `"summary":"s","detail":{"toolName":"Bash","input":"` + long + `"}`,
			wantTool:    "Bash",
			wantInput:   strings.Repeat("x", approvalInputMaxChars) + "…",
			wantSummary: "s",
			wantFullCmd: "ao session get s-1 --transcript 1 --before 5 --cap 0 -p demo",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var snap conversationSnapshotWire
			raw := `{"activities":[{"kind":"activity","sequence":4,"activityKind":"approval","status":"pending","requestId":"r1",` + tt.activity + `}]}`
			if err := json.Unmarshal([]byte(raw), &snap); err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			got := pendingRequestsFrom("s-1", "demo", snap)
			if len(got) != 1 {
				t.Fatalf("got %d requests: %+v", len(got), got)
			}
			if got[0].Tool != tt.wantTool || got[0].Input != tt.wantInput || got[0].Summary != tt.wantSummary || got[0].FullInputCommand != tt.wantFullCmd {
				t.Fatalf("got tool=%q input=%q summary=%q full=%q\nwant tool=%q input=%q summary=%q full=%q",
					got[0].Tool, got[0].Input, got[0].Summary, got[0].FullInputCommand,
					tt.wantTool, tt.wantInput, tt.wantSummary, tt.wantFullCmd)
			}
		})
	}
}

func TestSessionApprovalsList_Errors(t *testing.T) {
	for _, tt := range []struct {
		name     string
		args     []string
		reply    func(c approvalsCall) stubReply
		want     []string
		wantNot  []string
		wantExit int
	}{
		{
			name: "terminal ui session refused by the daemon",
			args: []string{"ls", "s-1"},
			reply: func(approvalsCall) stubReply {
				return stubReply{status: 409, body: `{"error":"conflict","code":"SESSION_MODE_MISMATCH","message":"this session was created in Terminal UI mode and has no chat conversation","requestId":"req-9"}`}
			},
			want: []string{"session s-1 runs in Terminal UI mode", "Ask the user to answer in the session's terminal", "SESSION_MODE_MISMATCH", "[request req-9]"},
		},
		{
			name: "unknown session",
			args: []string{"ls", "nope"},
			reply: func(approvalsCall) stubReply {
				return stubReply{status: 404, body: `{"error":"not_found","code":"SESSION_NOT_FOUND","message":"session not found"}`}
			},
			want: []string{"session not found", "SESSION_NOT_FOUND"},
		},
		{
			name: "session in another project",
			args: []string{"ls", "s-1", "-p", "other"},
			reply: func(c approvalsCall) stubReply {
				return stubReply{body: `{"session":` + sessionJSONWithMode("s-1", "demo", "chat") + `}`}
			},
			want:     []string{"session s-1 is not in project other"},
			wantExit: 2,
		},
		{
			name: "terminal ui session caught before the conversation read",
			args: []string{"ls", "s-1", "-p", "demo"},
			reply: func(c approvalsCall) stubReply {
				if strings.HasSuffix(c.path, "/conversation") {
					return stubReply{status: 500, body: `{"code":"SHOULD_NOT_BE_CALLED","message":"x"}`}
				}
				return stubReply{body: `{"session":` + sessionJSONWithMode("s-1", "demo", "tui") + `}`}
			},
			want:    []string{"session s-1 runs in Terminal UI mode"},
			wantNot: []string{"SHOULD_NOT_BE_CALLED"},
		},
		{
			name: "daemon failure passes through",
			args: []string{"ls", "s-1"},
			reply: func(approvalsCall) stubReply {
				return stubReply{status: 500, body: `{"error":"internal","code":"CONVERSATION_READ_FAILED","message":"Conversation read failed"}`}
			},
			want:    []string{"Conversation read failed", "CONVERSATION_READ_FAILED"},
			wantNot: []string{"Terminal UI"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startApprovalsDaemon(t, tt.reply)
			_, err := runApprovals(t, tt.args...)
			if err == nil {
				t.Fatal("want an error")
			}
			wantExit := tt.wantExit
			if wantExit == 0 {
				wantExit = 1
			}
			if ExitCode(err) != wantExit {
				t.Fatalf("exit code = %d, want %d", ExitCode(err), wantExit)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q is missing %q", err.Error(), want)
				}
			}
			for _, not := range tt.wantNot {
				if strings.Contains(err.Error(), not) {
					t.Fatalf("error %q should not contain %q", err.Error(), not)
				}
			}
		})
	}
}

func TestSessionApprovals_UsageErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"ls without a session", []string{"ls"}, "accepts 1 arg(s)"},
		{"ls with a blank session", []string{"ls", " "}, "session id is required"},
		{"ls with two sessions", []string{"ls", "a", "b"}, "accepts 1 arg(s)"},
		{"resolve without a request", []string{"resolve", "s-1", "--decision", "allow"}, "accepts 2 arg(s)"},
		{"resolve with a blank request", []string{"resolve", "s-1", " ", "--decision", "allow"}, "request id is required"},
		{"neither flag", []string{"resolve", "s-1", "r1"}, "one of --decision (approval) or --action (input request) is required"},
		{"both flags", []string{"resolve", "s-1", "r1", "--decision", "allow", "--action", "accept"}, "mutually exclusive"},
		{"empty decision", []string{"resolve", "s-1", "r1", "--decision", " "}, "--decision must not be empty"},
		{"unknown action", []string{"resolve", "s-1", "r1", "--action", "allow"}, "--action must be accept, decline, or cancel"},
		{"empty action", []string{"resolve", "s-1", "r1", "--action", ""}, "--action must be accept, decline, or cancel"},
		{"content with a decision", []string{"resolve", "s-1", "r1", "--decision", "allow", "--content", `{"a":1}`}, "--content is only valid with --action accept"},
		{"content with decline", []string{"resolve", "s-1", "r1", "--action", "decline", "--content", `{"a":1}`}, "--content is only valid with --action accept"},
		{"content that is not JSON", []string{"resolve", "s-1", "r1", "--action", "accept", "--content", `nope`}, "--content must be a JSON object"},
		{"content that is not an object", []string{"resolve", "s-1", "r1", "--action", "accept", "--content", `["a"]`}, "--content must be a JSON object"},
		{"content that is null", []string{"resolve", "s-1", "r1", "--action", "accept", "--content", `null`}, "--content must be a JSON object"},
		{"content with trailing data", []string{"resolve", "s-1", "r1", "--action", "accept", "--content", `{"a":1} {}`}, "--content must be a JSON object"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := startApprovalsDaemon(t, func(approvalsCall) stubReply { return stubReply{status: 204} })
			_, err := runApprovals(t, tt.args...)
			var usage usageError
			if !errors.As(err, &usage) || ExitCode(err) != 2 {
				t.Fatalf("err = %v (exit %d), want a usage error", err, ExitCode(err))
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q is missing %q", err.Error(), tt.want)
			}
			if len(*calls) != 0 {
				t.Fatalf("a usage error still reached the daemon: %+v", *calls)
			}
		})
	}
}

func TestSessionApprovalsResolve_SendsTheAnswer(t *testing.T) {
	for _, tt := range []struct {
		name     string
		args     []string
		wantPath string
		wantBody string
		wantOut  string
	}{
		{
			name:     "allow",
			args:     []string{"--decision", "allow"},
			wantPath: "/api/v1/sessions/s-1/conversation/approvals/acp-request:h:2/resolve",
			wantBody: `{"decisionId":"allow"}`,
			wantOut:  "resolved approval acp-request:h:2 on session s-1: allow\n",
		},
		{
			name:     "allow_always",
			args:     []string{"--decision", "allow_always"},
			wantPath: "/api/v1/sessions/s-1/conversation/approvals/acp-request:h:2/resolve",
			wantBody: `{"decisionId":"allow_always"}`,
			wantOut:  "resolved approval acp-request:h:2 on session s-1: allow_always\n",
		},
		{
			name:     "reject",
			args:     []string{"--decision", "reject"},
			wantPath: "/api/v1/sessions/s-1/conversation/approvals/acp-request:h:2/resolve",
			wantBody: `{"decisionId":"reject"}`,
			wantOut:  "resolved approval acp-request:h:2 on session s-1: reject\n",
		},
		{
			name:     "a provider's own decision id is passed through",
			args:     []string{"--decision", "acceptForSession"},
			wantPath: "/api/v1/sessions/s-1/conversation/approvals/acp-request:h:2/resolve",
			wantBody: `{"decisionId":"acceptForSession"}`,
			wantOut:  "resolved approval acp-request:h:2 on session s-1: acceptForSession\n",
		},
		{
			name:     "input decline carries no content",
			args:     []string{"--action", "decline"},
			wantPath: "/api/v1/sessions/s-1/conversation/inputs/acp-request:h:2/resolve",
			wantBody: `{"action":"decline"}`,
			wantOut:  "resolved input acp-request:h:2 on session s-1: decline\n",
		},
		{
			name:     "input cancel",
			args:     []string{"--action", "cancel"},
			wantPath: "/api/v1/sessions/s-1/conversation/inputs/acp-request:h:2/resolve",
			wantBody: `{"action":"cancel"}`,
			wantOut:  "resolved input acp-request:h:2 on session s-1: cancel\n",
		},
		{
			name:     "input accept without content (url consent)",
			args:     []string{"--action", "accept"},
			wantPath: "/api/v1/sessions/s-1/conversation/inputs/acp-request:h:2/resolve",
			wantBody: `{"action":"accept"}`,
			wantOut:  "resolved input acp-request:h:2 on session s-1: accept\n",
		},
		{
			name:     "input accept with form content",
			args:     []string{"--action", "accept", "--content", `{"db":"pg","n":3}`},
			wantPath: "/api/v1/sessions/s-1/conversation/inputs/acp-request:h:2/resolve",
			wantBody: `{"action":"accept","content":{"db":"pg","n":3}}`,
			wantOut:  "resolved input acp-request:h:2 on session s-1: accept\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := startApprovalsDaemon(t, func(approvalsCall) stubReply { return stubReply{status: 204} })
			out, err := runApprovals(t, append([]string{"resolve", "s-1", "acp-request:h:2"}, tt.args...)...)
			if err != nil {
				t.Fatalf("resolve failed: %v", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("calls = %+v, want exactly the resolve", *calls)
			}
			got := (*calls)[0]
			if got.method != "POST" || got.path != tt.wantPath || got.body != tt.wantBody {
				t.Fatalf("call = %+v\nwant POST %s %s", got, tt.wantPath, tt.wantBody)
			}
			if out != tt.wantOut {
				t.Fatalf("output = %q, want %q", out, tt.wantOut)
			}
		})
	}
}

func TestSessionApprovalsResolve_JSONOutput(t *testing.T) {
	startApprovalsDaemon(t, func(approvalsCall) stubReply { return stubReply{status: 204} })

	out, err := runApprovals(t, "resolve", "s-1", "r1", "--decision", "reject", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got approvalsResolveOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if want := (approvalsResolveOutput{SessionID: "s-1", RequestID: "r1", Kind: "approval", Decision: "reject", Resolved: true}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}

	out, err = runApprovals(t, "resolve", "s-1", "r2", "--action", "cancel", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var input approvalsResolveOutput
	if err := json.Unmarshal([]byte(out), &input); err != nil {
		t.Fatal(err)
	}
	if want := (approvalsResolveOutput{SessionID: "s-1", RequestID: "r2", Kind: "input", Action: "cancel", Resolved: true}); input != want {
		t.Fatalf("got %+v, want %+v", input, want)
	}
}

// TestSessionApprovalsResolve_EncodesIDs: request ids carry colons and, in
// principle, anything else. Each must stay one path segment on the wire, and the
// daemon must be able to decode the original back.
func TestSessionApprovalsResolve_EncodesIDs(t *testing.T) {
	for _, tt := range []struct {
		name      string
		sessionID string
		requestID string
		wantURI   string
	}{
		{"acp colons are valid in a segment", "s-1", "acp-request:3f9c:12", "/api/v1/sessions/s-1/conversation/approvals/acp-request:3f9c:12/resolve"},
		{"slash space and percent", "s-1", "acp-request:a/b c%d:1", "/api/v1/sessions/s-1/conversation/approvals/acp-request:a%2Fb%20c%25d:1/resolve"},
		{"question mark and hash", "s-1", "r?x#y", "/api/v1/sessions/s-1/conversation/approvals/r%3Fx%23y/resolve"},
		{"session id is encoded too", "mer 3/x", "r1", "/api/v1/sessions/mer%203%2Fx/conversation/approvals/r1/resolve"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := startApprovalsDaemon(t, func(approvalsCall) stubReply { return stubReply{status: 204} })
			if _, err := runApprovals(t, "resolve", tt.sessionID, tt.requestID, "--decision", "allow"); err != nil {
				t.Fatal(err)
			}
			got := (*calls)[0]
			if got.uri != tt.wantURI {
				t.Fatalf("request URI = %q, want %q", got.uri, tt.wantURI)
			}
			// What chi hands the controller, after its own PathUnescape of the segment.
			decoded := strings.TrimSuffix(strings.SplitN(got.path, "/approvals/", 2)[1], "/resolve")
			if decoded != tt.requestID {
				t.Fatalf("decoded request id = %q, want %q", decoded, tt.requestID)
			}
		})
	}
}

func TestSessionApprovalsResolve_DaemonRefusals(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		status  int
		body    string
		want    []string
		wantNot []string
	}{
		{
			name:   "a stale request id says to list again",
			args:   []string{"--decision", "allow"},
			status: 409,
			body:   `{"error":"conflict","code":"CHAT_REQUEST_NOT_PENDING","message":"this request has already been answered or is no longer waiting","requestId":"req-4"}`,
			want: []string{
				"request acp-request:h:2 is stale",
				"re-raised under a new id",
				"Run `ao session approvals ls s-1` for the current request id",
				"CHAT_REQUEST_NOT_PENDING", "[request req-4]",
			},
		},
		{
			name:   "a stale input request says the same",
			args:   []string{"--action", "decline"},
			status: 409,
			body:   `{"error":"conflict","code":"CHAT_REQUEST_NOT_PENDING","message":"this request has already been answered or is no longer waiting"}`,
			want:   []string{"request acp-request:h:2 is stale", "ao session approvals ls s-1"},
		},
		{
			name:    "another conflict keeps the daemon's own message",
			args:    []string{"--decision", "allow"},
			status:  409,
			body:    `{"error":"conflict","code":"CHAT_CONTROLLER_NOT_READY","message":"the agent controller for this session is not running"}`,
			want:    []string{"the agent controller for this session is not running", "CHAT_CONTROLLER_NOT_READY"},
			wantNot: []string{"stale", "ao session approvals ls"},
		},
		{
			name:   "a terminal ui session",
			args:   []string{"--decision", "allow"},
			status: 409,
			body:   `{"error":"conflict","code":"SESSION_MODE_MISMATCH","message":"this session was created in Terminal UI mode and has no chat conversation"}`,
			want:   []string{"session s-1 runs in Terminal UI mode", "SESSION_MODE_MISMATCH"},
		},
		{
			name:   "an answer the request did not offer",
			args:   []string{"--decision", "approve"},
			status: 400,
			body:   `{"error":"validation","code":"CHAT_DECISION_NOT_OFFERED","message":"chat decision was not offered for this request"}`,
			want:   []string{"did not offer that answer", "ao session approvals ls s-1", "CHAT_DECISION_NOT_OFFERED"},
		},
		{
			name:    "unknown session",
			args:    []string{"--decision", "allow"},
			status:  404,
			body:    `{"error":"not_found","code":"SESSION_NOT_FOUND","message":"session not found"}`,
			want:    []string{"session not found", "SESSION_NOT_FOUND"},
			wantNot: []string{"stale"},
		},
		{
			name:    "server error",
			args:    []string{"--decision", "allow"},
			status:  500,
			body:    `{"error":"internal","code":"INTERNAL","message":"server failed"}`,
			want:    []string{"server failed"},
			wantNot: []string{"stale", "Terminal UI"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startApprovalsDaemon(t, func(approvalsCall) stubReply { return stubReply{status: tt.status, body: tt.body} })
			out, err := runApprovals(t, append([]string{"resolve", "s-1", "acp-request:h:2"}, tt.args...)...)
			if err == nil {
				t.Fatalf("want an error, got output %q", out)
			}
			if ExitCode(err) != 1 {
				t.Fatalf("exit code = %d, want 1", ExitCode(err))
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q is missing %q", err.Error(), want)
				}
			}
			for _, not := range tt.wantNot {
				if strings.Contains(err.Error(), not) {
					t.Fatalf("error %q should not contain %q", err.Error(), not)
				}
			}
			if out != "" {
				t.Fatalf("a failed resolve printed %q", out)
			}
		})
	}
}

func TestSessionApprovalsResolve_ProjectScope(t *testing.T) {
	calls := startApprovalsDaemon(t, func(c approvalsCall) stubReply {
		if c.method == "GET" {
			return stubReply{body: `{"session":` + sessionJSONWithMode("s-1", "demo", "chat") + `}`}
		}
		return stubReply{status: 204}
	})

	_, err := runApprovals(t, "resolve", "s-1", "r1", "--decision", "allow", "-p", "other")
	var usage usageError
	if !errors.As(err, &usage) || !strings.Contains(err.Error(), "session s-1 is not in project other") {
		t.Fatalf("err = %v, want the project mismatch", err)
	}
	for _, c := range *calls {
		if c.method == "POST" {
			t.Fatalf("resolved a session outside the requested project: %+v", *calls)
		}
	}

	*calls = nil
	if _, err := runApprovals(t, "resolve", "s-1", "r1", "--decision", "allow", "-p", "demo"); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 2 || (*calls)[0].method != "GET" || (*calls)[1].method != "POST" {
		t.Fatalf("calls = %+v, want the project check then the resolve", *calls)
	}
}

func TestSessionApprovals_HelpListsBothSubcommands(t *testing.T) {
	out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "approvals", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ls ", "resolve "} {
		if !strings.Contains(out, want) {
			t.Fatalf("help is missing %q:\n%s", want, out)
		}
	}
}

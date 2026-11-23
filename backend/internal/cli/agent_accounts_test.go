package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const accountsJSON = `{"accounts":[
 {"id":"device-claude","provider":"claude","email":"owner@example.com","default":true,"signedIn":true,"device":true},
 {"id":"acct-b","provider":"claude","email":"second.person.with.a.very.long.address@example.com","default":false,"signedIn":true},
 {"id":"acct-c","provider":"claude","email":"owner-two@example.com","default":false,"signedIn":false},
 {"id":"device-codex","provider":"codex","email":"owner@example.com","default":true,"signedIn":true,"device":true}
]}`

func TestAgentAccountsListsIdProviderShortEmailSignedInAndDefault(t *testing.T) {
	cfg := setConfigEnv(t)
	var requests []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&requests, r)
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents/accounts" {
			_, _ = io.WriteString(w, accountsJSON)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "agent", "accounts")
	if err != nil {
		t.Fatalf("agent accounts failed: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"ID", "PROVIDER", "EMAIL", "SIGNED IN", "DEFAULT", "device-claude", "acct-b", "codex", "second.person.with.a.very.l…"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "second.person.with.a.very.long.address@example.com") {
		t.Fatalf("email was not shortened:\n%s", out)
	}
	if want := []string{"GET /api/v1/agents/accounts"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%#v want %#v", requests, want)
	}
}

func TestResolveAgentAccountByIdOrUniqueEmailPrefix(t *testing.T) {
	accounts := []agentAccountDTO{
		{ID: "device-claude", Provider: "claude", Email: "owner@example.com"},
		{ID: "acct-b", Provider: "claude", Email: "second@example.com"},
		{ID: "acct-c", Provider: "claude", Email: "owner-two@example.com"},
		{ID: "device-codex", Provider: "codex", Email: "owner@example.com"},
	}
	cases := []struct {
		query, provider, want, wantErr string
	}{
		{"acct-b", "claude", "acct-b", ""},
		{"sec", "claude", "acct-b", ""},
		{"SECOND@", "claude", "acct-b", ""},
		{"owner@", "claude", "device-claude", ""},
		{"owner@", "codex", "device-codex", ""},
		{"owner", "claude", "", "matches several accounts"},
		{"nobody", "claude", "", "no account matches"},
		{"", "claude", "", "--account is required"},
		{"acct-b", "codex", "", "no account matches"},
	}
	for _, tc := range cases {
		got, err := resolveAgentAccount(accounts, tc.query, tc.provider)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%q/%s: err = %v, want %q", tc.query, tc.provider, err, tc.wantErr)
			}
			continue
		}
		if err != nil || got.ID != tc.want {
			t.Fatalf("%q/%s: got %+v, err = %v, want %s", tc.query, tc.provider, got, err, tc.want)
		}
	}
}

type switchServer struct {
	requests []string
	body     map[string]any
}

func newSwitchServer(t *testing.T, harness string, response string) (*switchServer, *httptest.Server) {
	t.Helper()
	s := &switchServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appendPrimaryRequest(&s.requests, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/sessions/orch-1":
			_, _ = io.WriteString(w, `{"session":{"id":"orch-1","projectId":"proj","kind":"orchestrator","harness":"`+harness+`","activity":{"state":"idle"}}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents/accounts":
			_, _ = io.WriteString(w, accountsJSON)
		case r.Method == http.MethodPut && r.URL.Path == "/api/v1/agents/accounts/sessions/orch-1":
			_ = json.NewDecoder(r.Body).Decode(&s.body)
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, response)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

const switchResponse = `{"ok":true,"sessionId":"orch-1","transition":{"id":"t1","phase":"requested","sourceAccountId":"device-claude","targetAccountId":"acct-b"},
 "workers":[{"sessionId":"w-1","status":"moving"},{"sessionId":"w-2","status":"moving"},{"sessionId":"w-3","status":"skipped","reason":"already_on_account"}],"movedWorkers":2}`

func TestSessionSwitchAccountMovesTheSessionAndReportsMovedWorkers(t *testing.T) {
	cfg := setConfigEnv(t)
	s, srv := newSwitchServer(t, "claude-code", switchResponse)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
		"session", "switch-account", "orch-1", "--account", "second.person")
	if err != nil {
		t.Fatalf("switch-account failed: %v stderr=%s", err, errOut)
	}
	if s.body["accountId"] != "acct-b" {
		t.Fatalf("body = %v, want accountId acct-b resolved from the email prefix", s.body)
	}
	if _, set := s.body["includeWorkers"]; set {
		t.Fatalf("body = %v, includeWorkers must be left to the daemon's default", s.body)
	}
	for _, want := range []string{"moving session orch-1 to account acct-b", "also moved 2 workers", "w-3: skipped (already on account)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestSessionSwitchAccountWorkersFlags(t *testing.T) {
	for _, tc := range []struct {
		flag string
		want bool
	}{{"--workers", true}, {"--no-workers", false}} {
		cfg := setConfigEnv(t)
		s, srv := newSwitchServer(t, "claude-code", switchResponse)
		writeRunFileFor(t, cfg, srv)
		if _, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
			"session", "switch-account", "orch-1", "--account", "acct-b", tc.flag); err != nil {
			t.Fatalf("%s: %v stderr=%s", tc.flag, err, errOut)
		}
		if got, ok := s.body["includeWorkers"].(bool); !ok || got != tc.want {
			t.Fatalf("%s: body = %v, want includeWorkers=%v", tc.flag, s.body, tc.want)
		}
	}
}

func TestSessionSwitchAccountRefusesBadInput(t *testing.T) {
	cfg := setConfigEnv(t)
	s, srv := newSwitchServer(t, "claude-code", switchResponse)
	writeRunFileFor(t, cfg, srv)
	deps := Deps{ProcessAlive: func(int) bool { return true }}

	if _, _, err := executeCLI(t, deps, "session", "switch-account", "orch-1"); err == nil || !strings.Contains(err.Error(), "--account is required") {
		t.Fatalf("missing account: err = %v", err)
	}
	if _, _, err := executeCLI(t, deps, "session", "switch-account", "orch-1", "--account", "owner"); err == nil || !strings.Contains(err.Error(), "matches several accounts") {
		t.Fatalf("ambiguous prefix: err = %v", err)
	}
	if _, _, err := executeCLI(t, deps, "session", "switch-account", "orch-1", "--account", "acct-b", "--workers", "--no-workers"); err == nil {
		t.Fatal("--workers with --no-workers was accepted")
	}
	for _, req := range s.requests {
		if strings.HasPrefix(req, "PUT ") {
			t.Fatalf("a refused command still moved a session: %v", s.requests)
		}
	}
}

// ao spawn --account starts the agent on the named account.
func TestSpawnAccountFlagSendsTheResolvedAccountId(t *testing.T) {
	req := spawnRequest{AgentAccountID: "acct-b"}
	raw, err := json.Marshal(req)
	if err != nil || !strings.Contains(string(raw), `"agentAccountId":"acct-b"`) {
		t.Fatalf("spawn body = %s, err = %v, want agentAccountId", raw, err)
	}
	empty, _ := json.Marshal(spawnRequest{})
	if strings.Contains(string(empty), "agentAccountId") {
		t.Fatalf("spawn body without --account = %s, must not carry agentAccountId", empty)
	}
}

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotifyUserValidation(t *testing.T) {
	tests := []struct {
		name string
		env  string
		args []string
	}{
		{"nothing to do", "mer-orchestrator-1", []string{"notify-user"}},
		{"blank question", "mer-orchestrator-1", []string{"notify-user", "--waiting", "   "}},
		{"both flags", "mer-orchestrator-1", []string{"notify-user", "--waiting", "Ship it?", "--clear"}},
		{"too long", "mer-orchestrator-1", []string{"notify-user", "--waiting", strings.Repeat("x", 241)}},
		{"no session", "", []string{"notify-user", "--waiting", "Ship it?"}},
		{"positional text", "mer-orchestrator-1", []string{"notify-user", "Ship it?"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", tc.env)
			_, _, err := executeCLI(t, Deps{}, tc.args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("err=%v exit=%d, want a usage error", err, ExitCode(err))
			}
		})
	}
}

func TestNotifyUserDeclaresTheQuestionForThisSession(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	for _, tc := range []struct {
		outcome string
		want    string
	}{
		{"notified", "The user has been notified."},
		{"unchanged", "already open"},
		{"disabled", "turned this notification off"},
	} {
		t.Run(tc.outcome, func(t *testing.T) {
			cfg := setConfigEnv(t)
			var gotPath, gotMethod string
			var got notifyUserDeclareRequest
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath, gotMethod = r.URL.Path, r.Method
				_ = json.NewDecoder(r.Body).Decode(&got)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"outcome":"`+tc.outcome+`"}`)
			}))
			defer srv.Close()
			writeRunFileFor(t, cfg, srv)

			stdout, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }},
				"notify-user", "--waiting", "  Merge PR 42 now, or wait?  ")
			if err != nil {
				t.Fatal(err)
			}
			if gotMethod != http.MethodPost || gotPath != "/api/v1/sessions/mer-orchestrator-1/waiting" {
				t.Fatalf("request = %s %s", gotMethod, gotPath)
			}
			if got.Question != "Merge PR 42 now, or wait?" {
				t.Fatalf("question = %q", got.Question)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Fatalf("stdout = %q, want it to mention %q", stdout, tc.want)
			}
		})
	}
}

func TestNotifyUserClear(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	for _, tc := range []struct {
		cleared bool
		want    string
	}{
		{true, "Withdrew the question"},
		{false, "Nothing was open"},
	} {
		cfg := setConfigEnv(t)
		var gotPath, gotMethod string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotMethod = r.URL.Path, r.Method
			w.Header().Set("Content-Type", "application/json")
			if tc.cleared {
				_, _ = io.WriteString(w, `{"cleared":true}`)
			} else {
				_, _ = io.WriteString(w, `{"cleared":false}`)
			}
		}))
		writeRunFileFor(t, cfg, srv)
		stdout, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "notify-user", "--clear")
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if gotMethod != http.MethodDelete || gotPath != "/api/v1/sessions/mer-orchestrator-1/waiting" || !strings.Contains(stdout, tc.want) {
			t.Fatalf("request = %s %s, stdout = %q", gotMethod, gotPath, stdout)
		}
	}
}

// A worker that tries it gets the daemon's refusal, not a silent success.
func TestNotifyUserSurfacesTheDaemonRefusal(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-1")
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"validation","code":"NOT_AN_ORCHESTRATOR","message":"Only a project orchestrator can tell the user it is waiting"}`)
	}))
	defer srv.Close()
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "notify-user", "--waiting", "Ship it?")
	if err == nil || !strings.Contains(err.Error(), "Only a project orchestrator") {
		t.Fatalf("err = %v", err)
	}
}

package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// usageLimitedSessionJSON is a worker whose latest turn stopped on the 5-hour
// limit: idle by activity, but its status says why it is not working.
func usageLimitedSessionJSON(resume, offReason string) string {
	reset := time.Now().Add(90 * time.Minute).UTC().Format(time.RFC3339)
	return `{"id":"lim-1","projectId":"demo","kind":"worker","harness":"claude-code","displayName":"Dashboard",` +
		`"activity":{"state":"idle","lastActivityAt":"2026-11-16T14:20:00Z"},"isTerminated":false,` +
		`"createdAt":"2026-11-16T14:18:17Z","updatedAt":"2026-11-16T14:20:00Z","status":"usage_limit","prs":[],` +
		`"usageLimit":{"turnId":"t1","window":"5-hour","message":"You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)",` +
		`"stoppedAt":"2026-11-16T14:20:00Z","resetsAt":"` + reset + `","resume":"` + resume + `","resumeOffReason":"` + offReason + `"}}`
}

func usageLimitServer(t *testing.T, resume, offReason string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/sessions":
			_, _ = io.WriteString(w, `{"sessions":[`+usageLimitedSessionJSON(resume, offReason)+`]}`)
		case "/api/v1/sessions/lim-1":
			_, _ = io.WriteString(w, `{"session":`+usageLimitedSessionJSON(resume, offReason)+`}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A limit-stopped worker reads as that in the list, not as a bare "idle".
func TestSessionList_ShowsUsageLimitInsteadOfIdle(t *testing.T) {
	cfg := setConfigEnv(t)
	writeRunFileFor(t, cfg, usageLimitServer(t, "scheduled", ""))

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "ls", "--project", "demo")
	if err != nil {
		t.Fatalf("session ls failed: %v\nstderr=%s", err, errOut)
	}
	if !strings.Contains(out, "usage limit, resets ") || strings.Contains(out, " idle ") {
		t.Fatalf("ls output must show the limit and not idle:\n%s", out)
	}

	out, errOut, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "ls", "--project", "demo", "--json")
	if err != nil {
		t.Fatalf("session ls --json failed: %v\nstderr=%s", err, errOut)
	}
	var got sessionListOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Data) != 1 {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	entry := got.Data[0]
	if entry.Status != "usage_limit" || entry.UsageLimit == nil || entry.UsageLimit.Window != "5-hour" || entry.UsageLimit.Resume != "scheduled" {
		t.Fatalf("entry = %+v", entry)
	}
}

// `ao session get` without a transcript says what stopped the session and what
// AO will do about it, and --json carries the same fact.
func TestSessionGet_ShowsUsageLimitWithoutTranscript(t *testing.T) {
	for _, tc := range []struct {
		resume, off string
		want        string
	}{
		{"scheduled", "", "AO continues it automatically after the reset"},
		{"waiting", "", "AO continues it when a resume slot is free"},
		{"off", "disabled", "AO will not continue it automatically (automatic resume is off)"},
		{"off", "cancelled", "(you cancelled it)"},
	} {
		t.Run(tc.resume+tc.off, func(t *testing.T) {
			cfg := setConfigEnv(t)
			writeRunFileFor(t, cfg, usageLimitServer(t, tc.resume, tc.off))

			out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "get", "lim-1")
			if err != nil {
				t.Fatalf("session get failed: %v\nstderr=%s", err, errOut)
			}
			for _, want := range []string{"status: usage_limit", "activity: idle", "usage limit: stopped on the 5-hour limit, resets ", tc.want} {
				if !strings.Contains(out, want) {
					t.Fatalf("output is missing %q:\n%s", want, out)
				}
			}

			out, errOut, err = executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "get", "lim-1", "--json")
			if err != nil {
				t.Fatalf("session get --json failed: %v\nstderr=%s", err, errOut)
			}
			var got sessionResponse
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("decode: %v\n%s", err, out)
			}
			if got.Session.UsageLimit == nil || got.Session.UsageLimit.Resume != tc.resume || got.Session.UsageLimit.ResumeOffReason != tc.off {
				t.Fatalf("usageLimit = %+v", got.Session.UsageLimit)
			}
		})
	}
}

func TestUsageLimitActivityLabelWithoutAResetTime(t *testing.T) {
	limit := &sessionUsageLimitDTO{Resume: "off", ResumeOffReason: "unknown_reset"}
	if got := limit.activityLabel(); got != "usage limit, reset unknown" {
		t.Fatalf("label = %q", got)
	}
	if got := limit.detailLine(); !strings.Contains(got, "reset time unknown") || !strings.Contains(got, "the provider gave no reset time") {
		t.Fatalf("detail = %q", got)
	}
}

// A failed turn shows in the history with how it ended, so a session whose
// first turn died on the limit does not read as "no turns".
func TestSessionHistory_ShowsAFailedTurnAndItsError(t *testing.T) {
	cfg := setConfigEnv(t)
	t.Setenv("AO_SESSION_ID", "lim-1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"turns":[{"userRequest":"build the dashboard","agentResponse":"","at":"2026-11-16T14:18:17Z",` +
			`"state":"failed","error":"You've hit your 5-hour limit · resets 6pm (Europe/Amsterdam)"}],"total":1,"offset":0}`))
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	out, errOut, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, "session", "history")
	if err != nil {
		t.Fatalf("session history failed: %v\nstderr=%s", err, errOut)
	}
	for _, want := range []string{"## Turn 1 of 1", "[failed]", "build the dashboard", "Error: You've hit your 5-hour limit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
}

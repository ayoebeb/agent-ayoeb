package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// daemonStub answers one request and remembers it.
type daemonStub struct {
	method, path, caller string
	body                 map[string]any
}

func serveAutopilot(t *testing.T, reply string) *daemonStub {
	t.Helper()
	cfg := setConfigEnv(t)
	stub := &daemonStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.method, stub.path = r.Method, r.URL.Path
		stub.caller = r.Header.Get("X-AO-Caller-Session")
		stub.body = nil
		_ = json.NewDecoder(r.Body).Decode(&stub.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)
	return stub
}

var autopilotAlive = Deps{ProcessAlive: func(int) bool { return true }}

const runningView = `{"run":{"id":"ap_1","sessionId":"mer-orchestrator-1","task":"Make the importer faster","criteria":"","state":"running","iteration":2,"maxIterations":30,"stagnantIterations":0,"lastGap":"","evidence":"","ciGap":"","permissionsAllowed":12,"permissionsDenied":1,"startedAt":"2026-11-17T09:00:00Z","openParkedItemCount":1},"events":[],"parked":[{"id":"ape_2","kind":"parked","summary":"Upgrade the CI plan","detail":"money: ran out","createdAt":"2026-11-17T09:30:00Z"}],"warnings":["This is a terminal session: a permission prompt inside it cannot be answered."]}`

func TestAutopilotUsageErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		args []string
	}{
		{"start without a task", "mer-orchestrator-1", []string{"autopilot", "start"}},
		{"start with a blank task", "mer-orchestrator-1", []string{"autopilot", "start", "   "}},
		{"start without a session", "", []string{"autopilot", "start", "task"}},
		{"start with a negative cap", "mer-orchestrator-1", []string{"autopilot", "start", "task", "--max-iterations", "-1"}},
		{"status without a session", "", []string{"autopilot", "status"}},
		{"stop with both --all and --session", "", []string{"autopilot", "stop", "--all", "--session", "x"}},
		{"stop without a session", "", []string{"autopilot", "stop"}},
		{"criteria outside a session", "", []string{"autopilot", "criteria", "done when green"}},
		{"criteria blank", "mer-orchestrator-1", []string{"autopilot", "criteria", "  "}},
		{"decide missing a field", "mer-orchestrator-1", []string{"autopilot", "decide", "--question", "q", "--choice", "c"}},
		{"decide outside a session", "", []string{"autopilot", "decide", "--question", "q", "--choice", "c", "--why", "w"}},
		{"park with an unknown limit", "mer-orchestrator-1", []string{"autopilot", "park", "--limit", "everything", "--item", "i", "--why", "w"}},
		{"park missing the item", "mer-orchestrator-1", []string{"autopilot", "park", "--limit", "money", "--why", "w"}},
		{"done without evidence", "mer-orchestrator-1", []string{"autopilot", "done"}},
		{"done outside a session", "", []string{"autopilot", "done", "--evidence", "e"}},
		{"log with a zero limit", "x", []string{"autopilot", "log", "--limit", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AO_SESSION_ID", tc.env)
			_, _, err := executeCLI(t, Deps{}, tc.args...)
			if err == nil || ExitCode(err) != 2 {
				t.Fatalf("err=%v exit=%d, want a usage error (exit 2)", err, ExitCode(err))
			}
		})
	}
}

func TestAutopilotStartPutsTheTaskOnTheNamedSession(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	stub := serveAutopilot(t, runningView)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "start", "  Make the importer faster  ", "--session", "mer-orchestrator-1", "--max-iterations", "12")
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot" {
		t.Fatalf("request = %s %s", stub.method, stub.path)
	}
	if stub.body["task"] != "Make the importer faster" || stub.body["maxIterations"] != float64(12) {
		t.Fatalf("body = %v", stub.body)
	}
	if !strings.Contains(stdout, "Autopilot is on for mer-orchestrator-1") || !strings.Contains(stdout, "ao autopilot stop --session mer-orchestrator-1") ||
		!strings.Contains(stdout, "Note: This is a terminal session") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestAutopilotStartOmitsTheCapUnlessAsked(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	stub := serveAutopilot(t, runningView)
	if _, _, err := executeCLI(t, autopilotAlive, "autopilot", "start", "task", "--session", "mer-orchestrator-1"); err != nil {
		t.Fatal(err)
	}
	if _, present := stub.body["maxIterations"]; present {
		t.Fatalf("body = %v: the cap must be left to the Settings default", stub.body)
	}
}

func TestAutopilotStatusReadsOwnSessionAndRendersPlainWords(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	stub := serveAutopilot(t, runningView)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "status")
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodGet || stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot" {
		t.Fatalf("request = %s %s", stub.method, stub.path)
	}
	for _, want := range []string{
		"Autopilot on mer-orchestrator-1: running, iteration 3 of 30", "Task: Make the importer faster",
		"Criteria: not recorded yet", "12 allowed, 1 denied", "1 item(s) parked for the user", "Note: This is a terminal session",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
}

func TestAutopilotStatusForASessionThatNeverRan(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	serveAutopilot(t, `{"events":[],"parked":[],"warnings":[]}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "status", "--session", "mer-1")
	if err != nil || !strings.Contains(stdout, "Autopilot has not run on mer-1.") {
		t.Fatalf("stdout = %q err = %v", stdout, err)
	}
}

func TestAutopilotStatusJSONPrintsTheView(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	serveAutopilot(t, runningView)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var view autopilotView
	if err := json.Unmarshal([]byte(stdout), &view); err != nil || view.Run == nil || view.Run.State != "running" || len(view.Parked) != 1 {
		t.Fatalf("stdout = %q err = %v", stdout, err)
	}
}

func TestAutopilotStatusNamesWhyAParkedRunWaits(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	serveAutopilot(t, `{"run":{"state":"parked","endReason":"stuck","task":"t","iteration":4,"maxIterations":30},"events":[],"parked":[],"warnings":[]}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "status")
	if err != nil || !strings.Contains(stdout, "parked, waiting for the user (nothing changed for three iterations)") {
		t.Fatalf("stdout = %q err = %v", stdout, err)
	}
}

func TestAutopilotLogShowsTheReturnReportThenTheLogOldestFirst(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	serveAutopilot(t, `{
	  "run":{"state":"done","endReason":"goal_done","task":"Ship the importer","iteration":4,"maxIterations":30,"ciGap":"billing limit"},
	  "events":[
	    {"kind":"done","summary":"Done: go test ok","createdAt":"2026-11-17T10:00:00Z"},
	    {"kind":"decision","summary":"Patch or refactor? → Patch","detail":"smaller risk","createdAt":"2026-11-17T09:10:00Z"},
	    {"kind":"started","summary":"Autopilot started","createdAt":"2026-11-17T09:00:00Z"}],
	  "parked":[],"warnings":[],
	  "report":{"task":"Ship the importer","state":"done",
	    "mergedPrs":[{"url":"https://github.com/o/r/pull/4","title":"PR #4 merged"}],
	    "decisions":[{"summary":"Patch or refactor? → Patch","detail":"smaller risk"}],
	    "parked":[{"summary":"Buy CI minutes","detail":"money: ran out"},{"summary":"Email the customer","detail":"messaging: asked","resolvedAt":"2026-11-17T11:00:00Z"}],
	    "gaps":["CI could not run: billing limit"]}}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "log")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"What Autopilot did:", "Merged: PR #4 merged (https://github.com/o/r/pull/4)", "Decided: Patch or refactor? → Patch", "because smaller risk",
		"Parked (needs you): Buy CI minutes", "Parked (handled): Email the customer", "Gap: CI could not run: billing limit", "Log (oldest first, 3 line(s))",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout)
		}
	}
	if started, done := strings.Index(stdout, "Autopilot started"), strings.Index(stdout, "Done: go test ok"); started < 0 || done < 0 || started > done {
		t.Fatalf("the log must read oldest first:\n%s", stdout)
	}
}

func TestAutopilotStopOneAndAll(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	stub := serveAutopilot(t, `{"stopped":true}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "stop", "--session", "mer-orchestrator-1")
	if err != nil || stub.method != http.MethodPost || stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/stop" ||
		!strings.Contains(stdout, "Autopilot stopped on mer-orchestrator-1.") {
		t.Fatalf("one: %s %s %q %v", stub.method, stub.path, stdout, err)
	}

	stub = serveAutopilot(t, `{"stopped":false}`)
	stdout, _, err = executeCLI(t, autopilotAlive, "autopilot", "stop", "--session", "mer-1")
	if err != nil || !strings.Contains(stdout, "was not running on mer-1") {
		t.Fatalf("none: %q %v", stdout, err)
	}

	stub = serveAutopilot(t, `{"stopped":3}`)
	stdout, _, err = executeCLI(t, autopilotAlive, "autopilot", "stop", "--all")
	if err != nil || stub.path != "/api/v1/autopilot/stop" || !strings.Contains(stdout, "Stopped 3 Autopilot run(s).") {
		t.Fatalf("all: %s %q %v", stub.path, stdout, err)
	}
}

func TestAutopilotResume(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	stub := serveAutopilot(t, runningView)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "resume", "--session", "mer-orchestrator-1")
	if err != nil || stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/resume" || !strings.Contains(stdout, "Autopilot continues on mer-orchestrator-1.") {
		t.Fatalf("%s %q %v", stub.path, stdout, err)
	}
}

func TestAutopilotAgentCommandsActAsTheCallingSession(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")

	stub := serveAutopilot(t, `{"id":"ap_1"}`)
	if stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "criteria", "Done when go test passes"); err != nil ||
		stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/criteria" || stub.body["criteria"] != "Done when go test passes" ||
		!strings.Contains(stdout, "criteria recorded") {
		t.Fatalf("criteria: %s %v %q %v", stub.path, stub.body, stdout, err)
	}
	if stub.caller != "mer-orchestrator-1" {
		t.Fatalf("caller header = %q: the daemon tells the agent from the user by it", stub.caller)
	}

	stub = serveAutopilot(t, `{"event":{"id":"ape_1"}}`)
	if stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "decide", "--question", "Patch or refactor?", "--choice", "Patch", "--why", "Smaller risk"); err != nil ||
		stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/decisions" || stub.body["choice"] != "Patch" || !strings.Contains(stdout, "Decision recorded") {
		t.Fatalf("decide: %s %v %q %v", stub.path, stub.body, stdout, err)
	}

	stub = serveAutopilot(t, `{"event":{"id":"ape_2"},"alreadyParked":false}`)
	if stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "park", "--limit", "money", "--item", "Upgrade the CI plan", "--why", "Ran out"); err != nil ||
		stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/parked" || stub.body["limit"] != "money" || !strings.Contains(stdout, "the user has been told once") {
		t.Fatalf("park: %s %v %q %v", stub.path, stub.body, stdout, err)
	}
	stub = serveAutopilot(t, `{"event":{"id":"ape_2"},"alreadyParked":true}`)
	if stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "park", "--limit", "money", "--item", "Upgrade the CI plan", "--why", "Ran out"); err != nil ||
		!strings.Contains(stdout, "already parked; the user was not told again") {
		t.Fatalf("repeat park: %q %v", stdout, err)
	}
}

func TestAutopilotDoneAcceptedAndRefused(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")

	stub := serveAutopilot(t, `{"accepted":true,"failures":[],"gaps":["CI for PR #7 is failing; the agent says it cannot run: billing"],"run":{"state":"done"}}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "done", "--evidence", "go test ./... ok", "--ci-cannot-run", "billing")
	if err != nil || stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/done" ||
		stub.body["evidence"] != "go test ./... ok" || stub.body["ciCannotRun"] != "billing" ||
		!strings.Contains(stdout, "Done. Autopilot has finished this task.") || !strings.Contains(stdout, "Recorded gap: CI for PR #7") {
		t.Fatalf("accepted: %s %v %q %v", stub.path, stub.body, stdout, err)
	}

	serveAutopilot(t, `{"accepted":false,"failures":["PR #7 has no completed AO review of its current head."],"gaps":[],"run":{"state":"running"}}`)
	stdout, _, err = executeCLI(t, autopilotAlive, "autopilot", "done", "--evidence", "go test ./... ok")
	if err == nil || ExitCode(err) != 1 {
		t.Fatalf("a refused claim must exit 1 so the agent sees it failed: err=%v exit=%d", err, ExitCode(err))
	}
	if !strings.Contains(stdout, "Not done yet. These checks failed:") || !strings.Contains(stdout, "- PR #7 has no completed AO review") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestAutopilotSurfacesTheDaemonsRefusals(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"code":"AUTOPILOT_USER_ONLY","message":"Only the user can switch Autopilot on, off or back on"}`)
	}))
	defer srv.Close()
	writeRunFileFor(t, cfg, srv)
	_, _, err := executeCLI(t, autopilotAlive, "autopilot", "start", "task", "--session", "mer-1")
	if err == nil || ExitCode(err) != 1 || !strings.Contains(err.Error(), "Only the user can switch Autopilot on") {
		t.Fatalf("err=%v exit=%d, want the daemon's refusal with exit 1", err, ExitCode(err))
	}
}

func TestNotifyUserTellsTheAgentWhenAutopilotTookTheQuestion(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	serveAutopilot(t, `{"outcome":"autopilot"}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "notify-user", "--waiting", "Merge PR 42 now?")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Nothing was sent to the user", "do not wait for the user", "ao autopilot decide", "ao autopilot park"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout lacks %q: %s", want, stdout)
		}
	}
}

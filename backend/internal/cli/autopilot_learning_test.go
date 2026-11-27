package cli

import (
	"net/http"
	"strings"
	"testing"
)

func TestAutopilotAdviseAsksTheDaemonAsTheAgentAndPrintsTheAdvice(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	stub := serveAutopilot(t, `{"advice":"Their profile:\n- (all projects) Quality over cost."}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "advise", "--question", "Which model?", "--option", "Sonnet", "--option", "Opus")
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/api/v1/sessions/mer-orchestrator-1/autopilot/advice" || stub.caller != "mer-orchestrator-1" {
		t.Fatalf("request = %s %s as %q", stub.method, stub.path, stub.caller)
	}
	if stub.body["question"] != "Which model?" || len(stub.body["options"].([]any)) != 2 {
		t.Fatalf("body = %v", stub.body)
	}
	if !strings.Contains(stdout, "Quality over cost") {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestAutopilotDecideSendsTheBasisAndConfidence(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "mer-orchestrator-1")
	stub := serveAutopilot(t, `{"event":{"id":"ape_1","kind":"decision","summary":"Q → C","detail":"w","createdAt":"2026-11-17T09:00:00Z"}}`)
	if _, _, err := executeCLI(t, autopilotAlive, "autopilot", "decide", "--question", "Q", "--choice", "C", "--why", "w", "--based-on", "p1,p2", "--confidence", "low"); err != nil {
		t.Fatal(err)
	}
	if stub.body["confidence"] != "low" || len(stub.body["basedOn"].([]any)) != 2 {
		t.Fatalf("body = %v", stub.body)
	}
	if _, _, err := executeCLI(t, autopilotAlive, "autopilot", "decide", "--question", "Q", "--choice", "C", "--why", "w", "--confidence", "certain"); err == nil || ExitCode(err) != 2 {
		t.Fatalf("an unknown confidence must be a usage error: %v", err)
	}
}

func TestAutopilotLearnImportAndProfile(t *testing.T) {
	t.Setenv("AO_SESSION_ID", "")
	stub := serveAutopilot(t, `{"files":3,"pairs":12,"added":9,"skipped":0}`)
	stdout, _, err := executeCLI(t, autopilotAlive, "autopilot", "learn", "--import")
	if err != nil {
		t.Fatal(err)
	}
	if stub.method != http.MethodPost || stub.path != "/api/v1/autopilot/learning/import" || !strings.Contains(stdout, "12 question and answer pairs found, 9 new") {
		t.Fatalf("request = %s %s stdout = %q", stub.method, stub.path, stdout)
	}
	stub = serveAutopilot(t, `{"rules":[{"id":"r1","scope":"global","text":"Workers never on Opus.","origin":"learned"}],"pairs":[],"totalPairs":12,"unfoldedPairs":2,"lastError":"","canLearn":true,"learning":false}`)
	stdout, _, err = executeCLI(t, autopilotAlive, "autopilot", "learn")
	if err != nil || stub.method != http.MethodGet || !strings.Contains(stdout, "- [all projects] Workers never on Opus. (learned)") || !strings.Contains(stdout, "12 decisions stored, 2 not yet") {
		t.Fatalf("err=%v stdout=%q", err, stdout)
	}
}

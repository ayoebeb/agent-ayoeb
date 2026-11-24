package autopilot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// `link/..` is not the workspace when link points outside it: the shell goes up
// from where the link leads. A target written with a ".." is parked as written, by
// the text rules and again by the disk check, never cleaned into a path that
// looks inside.
func TestADeleteWithAParentSegmentThroughALinkIsParked(t *testing.T) {
	l := newLayout(t)
	makeLink(t, l.in("shortcut"), filepath.Join(l.outside, "precious"))
	ws := filepath.ToSlash(l.workspace)

	// Policy: never allowed, with or without a link, and nothing left to check on disk.
	for _, cmd := range []string{
		"rm -rf shortcut/../notes.txt",
		"rm -rf shortcut/../x",
		"rm -rf dist/../notes.txt",
		"rm -rf ./dist/..",
		"rm -rf dist/deep/../../notes.txt",
		"Remove-Item -Path shortcut/../x",
		"Remove-Item -LiteralPath dist/../notes.txt",
		`rm -rf shortcut\..\x`,
		`Remove-Item -Path dist\..\notes.txt`,
	} {
		a := Action{Tool: "Bash", Kind: "execute", Command: cmd, Workspace: ws}
		if v := Evaluate(a); v.Allow || v.Limit != domain.HardLimitDataDelete || len(v.DeleteTargets) != 0 {
			t.Errorf("Evaluate(%q) = %+v, want it parked as data_delete", cmd, v)
		}
	}
	// A provider cwd with a ".." is parked too.
	in := Action{Tool: "Bash", Kind: "execute", Command: "rm -rf dist", Workspace: ws, Cwd: filepath.ToSlash(l.in("shortcut")) + "/../dist"}
	if v := Evaluate(in); v.Allow {
		t.Errorf("a cwd with .. was allowed: %+v", v)
	}
	// The file-tool delete is held to the same rule.
	file := Action{Tool: "Delete", Kind: "delete", Paths: []string{"shortcut/../x"}, Workspace: ws}
	if v := Evaluate(file); v.Allow {
		t.Errorf("a file delete with .. was allowed: %+v", v)
	}

	// Disk: the check never cleans a path, so a ".." that reaches it is parked even
	// when the target would not exist.
	for _, target := range []string{
		ws + "/shortcut/../x",
		ws + "/dist/../notes.txt",
		ws + "/shortcut/../precious",
	} {
		ok, reason := checkDeleteTargets(l.workspace, []string{target})
		if ok {
			t.Errorf("%s accepted by the disk check", target)
		} else if !strings.Contains(reason, "..") {
			t.Errorf("%s: reason %q should name the \"..\"", target, reason)
		}
	}

	// The permission flow rejects and parks it, once each, and leaves ordinary
	// deletes alone.
	e := newEnv(t)
	orch := e.sessionAt(domain.KindOrchestrator, e.base, domain.SessionModeChat, l.workspace)
	e.start(orch.ID, "task")
	for _, cmd := range []string{"rm -rf shortcut/../x", "rm -rf dist/../notes.txt"} {
		before, notes := len(e.approvals.decisions), e.notes.count()
		e.svc.HandleApproval(e.ctx, orch.ID, approval("p", "Bash", "execute", jsonCommand(cmd)))
		if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "reject" {
			t.Errorf("%q decisions = %v, want reject", cmd, got)
		}
		if e.notes.count() != notes+1 {
			t.Errorf("%q: notifications %d -> %d, want it parked once", cmd, notes, e.notes.count())
		}
	}
	before := len(e.approvals.decisions)
	e.svc.HandleApproval(e.ctx, orch.ID, approval("ok", "Bash", "execute", jsonCommand("rm -rf dist")))
	if got := e.approvals.decisions[before:]; len(got) != 1 || got[0] != "allow" {
		t.Errorf("rm -rf dist decisions = %v, want allow", got)
	}
}

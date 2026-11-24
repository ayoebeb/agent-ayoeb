package autopilot

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// The file tool's delete is held to the same rule as the shell's: a ".." in the
// directory the request runs in, or in a target, as written, is parked before any
// path is normalized, because `link/..` does not lead where its cleaned form does.
func TestFileToolDeleteWithAParentSegmentInItsCwdOrTargetIsParked(t *testing.T) {
	l := newLayout(t)
	makeLink(t, l.in("shortcut"), filepath.Join(l.outside, "precious"))
	ws := filepath.ToSlash(l.workspace)
	deleteAction := func(cwd string, paths ...string) Action {
		return Action{Tool: "Delete", Kind: "delete", Paths: paths, Cwd: cwd, Workspace: ws}
	}

	parked := map[string]Action{
		"cwd goes up through a link":           deleteAction(ws+"/shortcut/..", "x"),
		"cwd with .. that cleans to inside":    deleteAction(ws+"/dist/..", "notes.txt"),
		"cwd with .. and an absolute target":   deleteAction(ws+"/shortcut/../dist", ws+"/notes.txt"),
		"cwd with a backslash ..":              deleteAction(ws+`\shortcut\..`, "x"),
		"target goes up through a link":        deleteAction("", "shortcut/../x"),
		"target with .. that cleans to inside": deleteAction("", "dist/../notes.txt"),
		"absolute target with ..":              deleteAction("", ws+"/shortcut/../x"),
		"one bad target among good ones":       deleteAction("", "notes.txt", "dist/../x"),
		"relative cwd with ..":                 deleteAction("shortcut/..", "x"),
	}
	for name, a := range parked {
		if v := Evaluate(a); v.Allow || v.Limit != domain.HardLimitDataDelete || len(v.DeleteTargets) != 0 {
			t.Errorf("%s: Evaluate = %+v, want it parked as data_delete with nothing left to delete", name, v)
		}
	}

	// Without a "..", a file delete is allowed by the text rules and handed to the
	// disk check with its absolute targets.
	ok := Evaluate(deleteAction(ws+"/dist", "app.js", "deep/more/a.txt"))
	if !ok.Allow || len(ok.DeleteTargets) != 2 || ok.DeleteTargets[0] != ws+"/dist/app.js" {
		t.Errorf("an ordinary file delete = %+v", ok)
	}

	// Through the permission flow, with the provider's own payload: the detail the
	// adapter stores carries the cwd beside the input.
	e := newEnv(t)
	orch := e.sessionAt(domain.KindOrchestrator, e.base, domain.SessionModeChat, l.workspace)
	e.start(orch.ID, "task")
	detail := func(cwd string, paths ...string) ApprovalNotice {
		doc := map[string]any{"providerToolName": "Delete", "toolKind": "delete", "cwd": cwd, "input": map[string]any{"paths": paths}}
		out, _ := json.Marshal(doc)
		n := approval("f", "Delete", "delete", "{}")
		n.Detail = out
		return n
	}
	decide := func(n ApprovalNotice) (string, int) {
		before, notes := len(e.approvals.decisions), e.notes.count()
		e.svc.HandleApproval(e.ctx, orch.ID, n)
		got := e.approvals.decisions[before:]
		if len(got) != 1 {
			t.Fatalf("decisions = %v", got)
		}
		return got[0], e.notes.count() - notes
	}
	for name, n := range map[string]ApprovalNotice{
		"cwd through the link":    detail(ws+"/shortcut/..", "x"),
		"target through the link": detail("", "shortcut/../x"),
	} {
		if got, parkedNotes := decide(n); got != "reject" || parkedNotes != 1 {
			t.Errorf("%s: decision %s with %d notification(s), want reject and one", name, got, parkedNotes)
		}
	}
	if got, _ := decide(detail(ws, "dist")); got != "allow" {
		t.Errorf("an ordinary file delete decided %s, want allow", got)
	}
	// And a link target is parked by the disk check, as for the shell.
	if got, _ := decide(detail(ws, "shortcut")); got != "reject" {
		t.Errorf("a file delete of a link decided %s, want reject", got)
	}
}

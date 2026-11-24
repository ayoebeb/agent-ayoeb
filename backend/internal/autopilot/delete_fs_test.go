package autopilot

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	aoprocess "github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// makeLink makes link point at target: a symbolic link where the platform allows
// one, else (Windows without the symlink privilege) a junction. A platform that
// allows neither skips the test.
func makeLink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		if out, err := aoprocess.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err == nil {
			return
		} else {
			t.Skipf("cannot create a symlink or a junction here: %v %s", err, out)
		}
	}
	t.Skip("cannot create a symlink here")
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// layout is a workspace with ordinary content and, next to it, a folder outside
// the workspace that must never be reached.
type layout struct {
	workspace string
	outside   string
}

func newLayout(t *testing.T) layout {
	t.Helper()
	base := t.TempDir()
	l := layout{workspace: filepath.Join(base, "workspace"), outside: filepath.Join(base, "outside")}
	write(t, filepath.Join(l.workspace, "dist", "app.js"), "x")
	write(t, filepath.Join(l.workspace, "dist", "deep", "more", "a.txt"), "x")
	write(t, filepath.Join(l.workspace, "notes.txt"), "x")
	write(t, filepath.Join(l.outside, "precious", "data.db"), "do not delete")
	return l
}

func (l layout) in(rel string) string { return filepath.Join(l.workspace, filepath.FromSlash(rel)) }

func TestCheckDeleteTargetsAllowsOrdinaryTargetsInsideTheWorkspace(t *testing.T) {
	l := newLayout(t)
	for _, targets := range [][]string{
		{l.in("dist")},
		{l.in("notes.txt")},
		{l.in("dist"), l.in("notes.txt")},
		{l.in("does-not-exist")},           // nothing to delete
		{l.in("new-dir/also-missing.txt")}, // its nearest existing parent is the workspace
	} {
		if ok, reason := checkDeleteTargets(l.workspace, toSlash(targets)); !ok {
			t.Errorf("targets %v parked: %s", targets, reason)
		}
	}
}

func TestCheckDeleteTargetsParksWhatCannotBeResolved(t *testing.T) {
	l := newLayout(t)
	// The workspace itself is not on disk.
	if ok, _ := checkDeleteTargets(filepath.Join(l.workspace, "missing"), toSlash([]string{l.in("dist")})); ok {
		t.Error("a workspace that does not exist was accepted")
	}
	// A target outside the workspace, even though it is real.
	if ok, _ := checkDeleteTargets(l.workspace, toSlash([]string{filepath.Join(l.outside, "precious")})); ok {
		t.Error("a target outside the workspace was accepted")
	}
	// The workspace root itself is never a target.
	if ok, _ := checkDeleteTargets(l.workspace, toSlash([]string{l.workspace})); ok {
		t.Error("the workspace root was accepted as a target")
	}
}

// A link inside the workspace whose target is outside it: as the delete target
// itself, as one of its parents, and for a target that does not exist yet.
func TestCheckDeleteTargetsParksALinkOutOfTheWorkspace(t *testing.T) {
	l := newLayout(t)
	makeLink(t, l.in("shortcut"), filepath.Join(l.outside, "precious"))

	cases := map[string][]string{
		"the target is the link":                 {l.in("shortcut")},
		"a parent of the target is the link":     {filepath.Join(l.in("shortcut"), "data.db")},
		"a parent of a target that is not there": {filepath.Join(l.in("shortcut"), "not-yet.txt")},
		"one bad target among good ones":         {l.in("dist"), l.in("shortcut"), l.in("notes.txt")},
	}
	for name, targets := range cases {
		if ok, reason := checkDeleteTargets(l.workspace, toSlash(targets)); ok {
			t.Errorf("%s: accepted, want parked", name)
		} else if reason == "" {
			t.Errorf("%s: parked without a reason", name)
		}
	}
}

// A recursive delete walks the tree: a link deep inside it is where the delete
// would escape, however ordinary the target looks.
func TestCheckDeleteTargetsParksALinkNestedInTheTree(t *testing.T) {
	l := newLayout(t)
	makeLink(t, filepath.Join(l.in("dist"), "deep", "more", "leak"), filepath.Join(l.outside, "precious"))

	ok, reason := checkDeleteTargets(l.workspace, toSlash([]string{l.in("dist")}))
	if ok {
		t.Fatal("a tree holding a link that leads outside the workspace was accepted")
	}
	if !strings.Contains(reason, "leak") {
		t.Errorf("reason %q should name the link", reason)
	}
	// A link inside the workspace that stays inside is parked too: AO cannot tell
	// where it leads without following it, and it need not guess.
	l2 := newLayout(t)
	makeLink(t, filepath.Join(l2.in("dist"), "inside"), l2.in("notes.txt"))
	if ok, _ := checkDeleteTargets(l2.workspace, toSlash([]string{l2.in("dist")})); ok {
		t.Error("a link inside the tree was accepted")
	}
	// The same tree without the link is fine.
	if ok, reason := checkDeleteTargets(newLayout(t).workspace, nil); !ok {
		t.Errorf("no targets parked: %s", reason)
	}
}

func TestCheckDeleteTargetsParksATreeTooBigToCheck(t *testing.T) {
	l := newLayout(t)
	old := maxDeleteTreeEntries
	maxDeleteTreeEntries = 5
	t.Cleanup(func() { maxDeleteTreeEntries = old })

	for i := 0; i < 10; i++ {
		write(t, l.in("dist/many/f"+string(rune('a'+i))+".txt"), "x")
	}
	ok, reason := checkDeleteTargets(l.workspace, toSlash([]string{l.in("dist")}))
	if ok || !strings.Contains(reason, "more than") {
		t.Fatalf("a tree past the bound = %v %q, want parked for its size", ok, reason)
	}
	// Within the bound it passes.
	maxDeleteTreeEntries = 1000
	if ok, reason := checkDeleteTargets(l.workspace, toSlash([]string{l.in("dist")})); !ok {
		t.Fatalf("a small tree parked: %s", reason)
	}
}

func toSlash(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.ToSlash(p)
	}
	return out
}

// The permission flow: a delete the text allows is still parked when the disk
// says the target, a parent or an entry inside the tree leads outside.
func TestHandleApprovalParksADeleteThatLeavesTheWorkspaceThroughALink(t *testing.T) {
	l := newLayout(t)
	e := newEnv(t)
	orch := e.sessionAt(domain.KindOrchestrator, e.base, domain.SessionModeChat, l.workspace)
	e.start(orch.ID, "task")

	decide := func(command string) string {
		before := len(e.approvals.decisions)
		e.svc.HandleApproval(e.ctx, orch.ID, approval("d", "Bash", "execute", jsonCommand(command)))
		got := e.approvals.decisions[before:]
		if len(got) != 1 {
			t.Fatalf("%s: decisions = %v", command, got)
		}
		return got[0]
	}
	// Ordinary deletes inside the workspace go through.
	for _, cmd := range []string{"rm -rf dist", "rm -f notes.txt", "rm -rf dist notes.txt"} {
		if got := decide(cmd); got != "allow" {
			t.Fatalf("%q decided %s, want allow", cmd, got)
		}
	}
	notes := e.notes.count()

	// The target is a link to a folder outside.
	makeLink(t, l.in("shortcut"), filepath.Join(l.outside, "precious"))
	// A link nested in the tree.
	makeLink(t, filepath.Join(l.in("dist"), "deep", "leak"), filepath.Join(l.outside, "precious"))

	for _, cmd := range []string{
		"rm -rf shortcut",
		"rm -rf shortcut/data.db",
		"rm -rf shortcut/not-yet",
		"rm -rf dist",
		"rm -rf notes.txt dist",
	} {
		if got := decide(cmd); got != "reject" {
			t.Errorf("%q decided %s, want reject: it reaches outside the workspace through a link", cmd, got)
		}
	}
	if e.notes.count() != notes+5 {
		t.Errorf("parked notifications = %d, want %d: each parked delete is told to the user once", e.notes.count(), notes+5)
	}
	// And the precious file is still there; nothing in this test deletes anything.
	if _, err := os.Stat(filepath.Join(l.outside, "precious", "data.db")); err != nil {
		t.Fatal(err)
	}
	// The text rules still apply first.
	if got := decide("rm -rf ../outside/precious"); got != "reject" {
		t.Errorf("a path leaving the workspace decided %s, want reject", got)
	}
}

// A Windows junction is not a symbolic link to Go's mode bits; it is a reparse
// point, and must be parked as a target, a parent and a nested entry.
func TestCheckDeleteTargetsParksAWindowsJunction(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions exist only on Windows")
	}
	l := newLayout(t)
	for _, link := range []string{l.in("shortcut"), filepath.Join(l.in("dist"), "deep", "leak")} {
		if out, err := aoprocess.Command("cmd", "/c", "mklink", "/J", link, filepath.Join(l.outside, "precious")).CombinedOutput(); err != nil {
			t.Skipf("cannot create a junction here: %v %s", err, out)
		}
	}
	for name, targets := range map[string][]string{
		"the target is the junction":          {l.in("shortcut")},
		"a parent is the junction":            {filepath.Join(l.in("shortcut"), "data.db")},
		"a junction nested in the tree":       {l.in("dist")},
		"a missing target under the junction": {filepath.Join(l.in("shortcut"), "new.txt")},
	} {
		if ok, _ := checkDeleteTargets(l.workspace, toSlash(targets)); ok {
			t.Errorf("%s: accepted, want parked", name)
		}
	}
	if ok, reason := checkDeleteTargets(l.workspace, toSlash([]string{l.in("notes.txt")})); !ok {
		t.Errorf("an ordinary file next to junctions parked: %s", reason)
	}
}

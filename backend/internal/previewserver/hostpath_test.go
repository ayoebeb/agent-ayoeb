package previewserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// fakeWindows is a Windows computer whose fnm default and system tools exist
// only in the two sets, so no test depends on what this machine has installed.
func fakeWindows(env map[string]string, persistent string, dirs, files []string) hostLookup {
	return hostLookup{
		goos:           "windows",
		getenv:         func(key string) string { return env[key] },
		homeDir:        `C:\Users\dev`,
		persistentPath: func() string { return persistent },
		isDir:          func(name string) bool { return slices.Contains(dirs, name) },
		isExecutable:   func(name string) bool { return slices.Contains(files, name) },
		glob:           func(string) []string { return nil },
		readFile:       func(string) string { return "" },
	}
}

func TestSearchPathAddsPersistentPathAndFnmDefault(t *testing.T) {
	fnm := `C:\Users\dev\AppData\Roaming\fnm\aliases\default`
	host := fakeWindows(
		map[string]string{"APPDATA": `C:\Users\dev\AppData\Roaming`},
		`C:\Windows\System32;c:\tools\;.;relative\bin;"C:\Program Files\Git\cmd"`,
		[]string{fnm}, nil,
	)
	got := host.searchPath(`C:\Tools`)
	want := []string{fnm, `C:\Tools`, `C:\Windows\System32`, `C:\Program Files\Git\cmd`}
	if !slices.Equal(got, want) {
		t.Fatalf("search path = %q, want %q", got, want)
	}
}

func TestSearchPathKeepsTheActiveFnmShell(t *testing.T) {
	multishell := `C:\Users\dev\AppData\Local\fnm_multishells\123`
	host := fakeWindows(
		map[string]string{"FNM_DIR": `D:\fnm`, "FNM_MULTISHELL_PATH": multishell},
		"", []string{multishell, `D:\fnm\aliases\default`}, nil,
	)
	if got := host.searchPath(multishell); !slices.Equal(got, []string{multishell}) {
		t.Fatalf("search path = %q, want only the active multishell", got)
	}
	// A daemon whose PATH lost the shell setup still gets that shell's Node.
	if got := host.searchPath(`C:\Windows`); !slices.Equal(got, []string{multishell, `C:\Windows`}) {
		t.Fatalf("search path = %q, want the multishell first", got)
	}
}

func TestSearchPathUsesNvmDefaultOnUnix(t *testing.T) {
	host := hostLookup{
		goos:           "linux",
		getenv:         func(string) string { return "" },
		homeDir:        "/home/dev",
		persistentPath: func() string { return "" },
		isDir:          func(name string) bool { return strings.HasPrefix(name, "/home/dev/.nvm/") },
		glob: func(string) []string {
			return []string{"/home/dev/.nvm/versions/node/v24.0.0", "/home/dev/.nvm/versions/node/v22.3.0"}
		},
		readFile: func(string) string { return "22\n" },
	}
	got := host.searchPath("/usr/bin")
	if !slices.Equal(got, []string{"/home/dev/.nvm/versions/node/v22.3.0/bin", "/usr/bin"}) {
		t.Fatalf("search path = %q", got)
	}
}

func TestResolveExecutableUsesPathextAndNeverTheWorkingDirectory(t *testing.T) {
	host := fakeWindows(nil, "", nil, []string{
		`C:\node\npm`, // the extensionless shell script npm ships
		`C:\node\npm.cmd`,
		`C:\node\node.exe`,
		`C:\work\npx.cmd`,
		`C:\work\tools\serve.cmd`,
	})
	searchPath := []string{`C:\node`}
	for name, want := range map[string]string{
		"npm":           `C:\node\npm.cmd`,
		"node":          `C:\node\node.exe`,
		"node.exe":      `C:\node\node.exe`,
		"./tools/serve": `C:\work\tools\serve.cmd`,
		`tools\serve`:   `C:\work\tools\serve.cmd`,
	} {
		got, err := host.resolveExecutable(name, `C:\work`, searchPath, ".COM;.EXE;.BAT;.CMD;.JS")
		if err != nil || got != want {
			t.Errorf("resolve %q = %q, %v; want %q", name, got, err, want)
		}
	}

	_, err := host.resolveExecutable("npx", `C:\work`, searchPath, "")
	var previewErr Error
	if !errors.As(err, &previewErr) || previewErr.Code != "PREVIEW_EXECUTABLE_NOT_FOUND" {
		t.Fatalf("resolve npx present only in the working directory: err = %v", err)
	}
	for _, part := range []string{`"npx"`, `PATH searched: C:\node`} {
		if !strings.Contains(previewErr.Message, part) {
			t.Errorf("error %q does not name %s", previewErr.Message, part)
		}
	}
}

func TestWithSearchPathEmitsOnePathAndKeepsAConfiguredOne(t *testing.T) {
	host := fakeWindows(nil, `C:\Windows`, nil, nil)
	env, searchPath := host.withSearchPath([]string{`Path=C:\daemon`, "LANG=nl"})
	if want := []string{"LANG=nl", `PATH=C:\daemon;C:\Windows`}; !slices.Equal(env, want) {
		t.Fatalf("env = %q, want %q", env, want)
	}
	if !slices.Equal(searchPath, []string{`C:\daemon`, `C:\Windows`}) {
		t.Fatalf("search path = %q", searchPath)
	}
	env, searchPath = host.withSearchPath(
		[]string{`Path=C:\daemon`, `Path=C:\mine`}, map[string]string{"Path": `C:\mine`},
	)
	if !slices.Equal(env, []string{`PATH=C:\mine`}) || !slices.Equal(searchPath, []string{`C:\mine`}) {
		t.Fatalf("configured PATH not kept: env = %q, search path = %q", env, searchPath)
	}
}

func writeProjectLaunch(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if body != "" {
		if err := os.MkdirAll(filepath.Join(root, ".ao"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".ao", "launch.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestLoadConfigurationInheritsTheProjectFile(t *testing.T) {
	project := writeProjectLaunch(t, `{"version":1,"configurations":[
		{"name":"dashboard","runtimeExecutable":"npm","port":5173},
		{"name":"webshop","runtimeExecutable":"npm","url":"http://127.0.0.1:${PORT}/shop"},
		{"name":"pinned","runtimeExecutable":"npm","port":4000,"url":"http://127.0.0.1:4000/"},
		{"name":"fixed","runtimeExecutable":"npm","port":4100,"autoPort":false}]}`)
	worktree := writeProjectLaunch(t, "")

	for name, wantAuto := range map[string]bool{"dashboard": true, "webshop": true, "pinned": false, "fixed": false} {
		cfg, err := loadConfiguration(worktree, project, name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		if cfg.AutoPort != wantAuto {
			t.Errorf("%s autoPort = %v, want %v", name, cfg.AutoPort, wantAuto)
		}
	}

	override := writeProjectLaunch(t, `{"version":1,"configurations":[
		{"name":"dashboard","runtimeExecutable":"pnpm","port":6000},
		{"name":"api","runtimeExecutable":"go","autoPort":true}]}`)
	cfg, err := loadConfiguration(override, project, "dashboard")
	if err != nil || cfg.RuntimeExecutable != "pnpm" || cfg.AutoPort {
		t.Fatalf("worktree override = %+v, %v", cfg, err)
	}
	if cfg, err = loadConfiguration(override, project, "webshop"); err != nil || cfg.RuntimeExecutable != "npm" {
		t.Fatalf("inherited next to an override = %+v, %v", cfg, err)
	}
	_, err = loadConfiguration(override, project, "")
	if err == nil || !strings.Contains(err.Error(), "dashboard, webshop, pinned, fixed, api") {
		t.Fatalf("ambiguous load error = %v, want the merged names", err)
	}
	// The main checkout itself is not read twice.
	if cfg, err = loadConfiguration(override, override, "api"); err != nil || cfg.RuntimeExecutable != "go" {
		t.Fatalf("project checkout as workspace = %+v, %v", cfg, err)
	}
}

func TestManagerSharedConfigurationGivesEveryWorktreeItsOwnPort(t *testing.T) {
	shared := helperConfiguration("web", TargetApp)
	shared.AutoPort = true
	_, reservation, err := reservePort(0, true)
	if err != nil {
		t.Fatal(err)
	}
	address, ok := reservation.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatal("reservation has no TCP address")
	}
	shared.Port = address.Port
	_ = reservation.Close()
	project := writeLaunchFile(t, []Configuration{shared})

	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Close)
	manager.SetProjectRoot(func(context.Context, domain.SessionID) string { return project })

	first, err := manager.Start(context.Background(), "ao-1", t.TempDir(), "web", nil)
	if err != nil {
		t.Fatalf("first start: %v\nstatus=%+v", err, first)
	}
	second, err := manager.Start(context.Background(), "ao-2", t.TempDir(), "web", nil)
	if err != nil {
		t.Fatalf("second start: %v\nstatus=%+v", err, second)
	}
	if first.Port != shared.Port || second.Port == first.Port {
		t.Fatalf("ports = %d and %d, want the preferred %d and another one", first.Port, second.Port, shared.Port)
	}
}

func TestManagerKeepsRunningServerWhenTheExecutableIsMissing(t *testing.T) {
	workspace := writeLaunchFile(t, []Configuration{helperConfiguration("web", TargetApp)})
	manager := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(manager.Close)
	if _, err := manager.Start(context.Background(), "ao-1", workspace, "", nil); err != nil {
		t.Fatal(err)
	}
	broken := writeLaunchFile(t, []Configuration{{Name: "web", RuntimeExecutable: "ao-no-such-tool", AutoPort: true}})
	_, err := manager.Start(context.Background(), "ao-1", broken, "", nil)
	assertPreviewErrorCode(t, err, "PREVIEW_EXECUTABLE_NOT_FOUND")
	if state := manager.Status("ao-1").State; state != StateReady {
		t.Fatalf("running server state = %q after a failed start", state)
	}
}

package liveserver

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

type fakeManaged struct {
	statuses []previewserver.Status
	started  []string
	stopped  []domain.SessionID
}

func (f *fakeManaged) List() []previewserver.Status { return f.statuses }
func (f *fakeManaged) Start(_ context.Context, id domain.SessionID, ws, cfg string, _ map[string]string) (previewserver.Status, error) {
	f.started = append(f.started, string(id)+"|"+ws+"|"+cfg)
	return previewserver.Status{SessionID: id, State: previewserver.StateReady, Configuration: cfg, TargetKind: previewserver.TargetApp, URL: "http://127.0.0.1:9000/", Port: 9000}, nil
}
func (f *fakeManaged) Stop(_ context.Context, id domain.SessionID) (previewserver.Status, error) {
	f.stopped = append(f.stopped, id)
	return previewserver.Status{SessionID: id, State: previewserver.StateStopped}, nil
}

// psTable parses ps-style rows: pid ppid rss command.
func psTable(t *testing.T, rows string) *procmem.Table {
	t.Helper()
	table, err := procmem.Parse(rows)
	if err != nil {
		t.Fatal(err)
	}
	return table
}

type harness struct {
	svc      *Service
	managed  *fakeManaged
	killed   []ProcessRef
	starts   map[int]string
	preview  map[domain.SessionID]string
	probes   map[string]error
	probeHit map[string]int
	mu       sync.Mutex
}

func newHarness(t *testing.T, listeners []Listener, ps string, sessions []Session) *harness {
	t.Helper()
	h := &harness{managed: &fakeManaged{}, preview: map[domain.SessionID]string{}, probes: map[string]error{}, probeHit: map[string]int{}, starts: map[int]string{}}
	h.svc = New(Deps{
		Managed:   h.managed,
		Sessions:  func(context.Context, domain.ProjectID) ([]Session, error) { return sessions, nil },
		Listeners: func(context.Context) ([]Listener, error) { return listeners, nil },
		Table:     func(context.Context) (*procmem.Table, error) { return psTable(t, ps), nil },
		Probe: func(_ context.Context, url string) (int, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.probeHit[url]++
			if err := h.probes[url]; err != nil {
				return 0, err
			}
			return 200, nil
		},
		StartTime: func(pid int) string {
			h.mu.Lock()
			defer h.mu.Unlock()
			if s, ok := h.starts[pid]; ok {
				return s
			}
			return "start-" + strconv.Itoa(pid)
		},
		Kill:       func(_ context.Context, procs []ProcessRef) error { h.killed = append(h.killed, procs...); return nil },
		SetPreview: func(_ context.Context, id domain.SessionID, url string) error { h.preview[id] = url; return nil },
		PreviewURL: func(_ context.Context, id domain.SessionID) string { return h.preview[id] },
		OwnPID:     1,
	})
	return h
}

const psRows = `
10 1 100 claude
11 10 100 node vite
12 11 100 node vite-worker
20 1 100 postgres
30 1 100 node unrelated
40 10 100 claude-helper
`

func TestDetectsOnlyListenersOwnedByProjectSessions(t *testing.T) {
	listeners := []Listener{
		{PID: 11, Port: 5173, Addr: "127.0.0.1"},
		{PID: 12, Port: 5173, Addr: "127.0.0.1"},
		{PID: 20, Port: 5432, Addr: "0.0.0.0"},
		{PID: 30, Port: 8081, Addr: "::"},
		{PID: 11, Port: 9229, Addr: "192.168.1.5"},
	}
	h := newHarness(t, listeners, psRows, []Session{{ID: "s1", Name: "worker one", WorkspacePath: "/w/s1", RootPIDs: []int{10}}})
	got, err := h.svc.List(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("servers = %+v, want exactly the vite server", got)
	}
	s := got[0]
	if s.Key != "detected:5173" || s.Source != SourceDetected || s.Name != "vite" || s.SessionID != "s1" || s.PID != 11 {
		t.Fatalf("server = %+v", s)
	}
	if s.OutputAvailable || s.CanRestart || s.WorkingDir != "/w/s1" || s.URL != "http://127.0.0.1:5173/" {
		t.Fatalf("detected flags/url = %+v", s)
	}
	if s.Health != HealthUp {
		t.Fatalf("health = %q", s.Health)
	}
}

func TestIgnoresTerminatedSessionsAndAgentProcesses(t *testing.T) {
	listeners := []Listener{{PID: 10, Port: 4000, Addr: "127.0.0.1"}, {PID: 11, Port: 5173, Addr: "127.0.0.1"}}
	h := newHarness(t, listeners, psRows, []Session{{ID: "s1", RootPIDs: []int{10}, Terminated: true}})
	got, _ := h.svc.List(context.Background(), "p1")
	if len(got) != 0 {
		t.Fatalf("terminated session still owns servers: %+v", got)
	}
	h = newHarness(t, listeners, psRows, []Session{{ID: "s1", RootPIDs: []int{10}}})
	got, _ = h.svc.List(context.Background(), "p1")
	if len(got) != 1 || got[0].Port != 5173 {
		t.Fatalf("agent process port not ignored: %+v", got)
	}
}

func TestManagedServersListFirstAndDedupeDetected(t *testing.T) {
	listeners := []Listener{{PID: 11, Port: 5173, Addr: "127.0.0.1"}, {PID: 11, Port: 3000, Addr: "127.0.0.1"}}
	h := newHarness(t, listeners, psRows, []Session{{ID: "s1", Name: "one", WorkspacePath: "/w/s1", RootPIDs: []int{10}}, {ID: "s2", Name: "two"}})
	h.managed.statuses = []previewserver.Status{
		{SessionID: "s1", State: previewserver.StateReady, Configuration: "Yoobi", Command: "npm run dev", Port: 5173, URL: "http://127.0.0.1:5173/", TerminalID: "term-1"},
		{SessionID: "other-project", State: previewserver.StateReady, Port: 7000},
		{SessionID: "s2", State: previewserver.StateStopped, Port: 7001},
	}
	got, err := h.svc.List(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, s := range got {
		keys = append(keys, s.Key)
	}
	if !reflect.DeepEqual(keys, []string{"managed:s1", "detected:3000"}) {
		t.Fatalf("keys = %v", keys)
	}
	m := got[0]
	if !m.OutputAvailable || !m.CanRestart || m.TerminalID != "term-1" || m.Command != "npm run dev" || m.SessionName != "one" {
		t.Fatalf("managed = %+v", m)
	}
}

func TestHealthDownWhenProbeFailsAndIsCached(t *testing.T) {
	h := newHarness(t, nil, "", []Session{{ID: "s1"}})
	h.managed.statuses = []previewserver.Status{{SessionID: "s1", State: previewserver.StateReady, Port: 5173, URL: "http://127.0.0.1:5173/"}}
	h.probes["http://127.0.0.1:5173/"] = errors.New("connection refused")
	for i := 0; i < 2; i++ {
		got, _ := h.svc.List(context.Background(), "p1")
		if got[0].Health != HealthDown || got[0].HealthNote != "connection refused" {
			t.Fatalf("health = %+v", got[0])
		}
	}
	if n := h.probeHit["http://127.0.0.1:5173/"]; n != 1 {
		t.Fatalf("probed %d times, want the cache to absorb the second poll", n)
	}
	now := time.Now()
	h.svc.deps.Now = func() time.Time { return now.Add(time.Minute) }
	h.probes["http://127.0.0.1:5173/"] = nil
	got, _ := h.svc.List(context.Background(), "p1")
	if got[0].Health != HealthUp || h.probeHit["http://127.0.0.1:5173/"] != 2 {
		t.Fatalf("expired cache not re-probed: %+v hits=%d", got[0], h.probeHit["http://127.0.0.1:5173/"])
	}
}

func TestStopDetectedKillsTreeLeavesFirstAndRefusesStaleKeys(t *testing.T) {
	listeners := []Listener{{PID: 11, Port: 5173, Addr: "127.0.0.1"}}
	h := newHarness(t, listeners, psRows, []Session{{ID: "s1", RootPIDs: []int{10}}})
	if err := h.svc.Stop(context.Background(), "p1", "detected:5173"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.killed, []ProcessRef{{PID: 12, Start: "start-12"}, {PID: 11, Start: "start-11"}}) {
		t.Fatalf("killed = %v, want child before parent", h.killed)
	}
	if err := h.svc.Stop(context.Background(), "p1", "detected:9999"); err == nil {
		t.Fatal("stopping an unknown server must fail")
	}
}

func TestStopRefusesAPidThatWasReusedAfterDiscovery(t *testing.T) {
	listeners := []Listener{{PID: 11, Port: 5173, Addr: "127.0.0.1"}}
	h := newHarness(t, listeners, psRows, []Session{{ID: "s1", RootPIDs: []int{10}}})
	// The service reads the listener's start time when it finds the server, then
	// again before killing: the second reading belongs to an unrelated process.
	reads := 0
	h.svc.deps.StartTime = func(pid int) string {
		if pid != 11 {
			return "start-" + strconv.Itoa(pid)
		}
		reads++
		if reads == 1 {
			return "original"
		}
		return "replacement"
	}
	err := h.svc.Stop(context.Background(), "p1", "detected:5173")
	if err == nil || !strings.Contains(err.Error(), "prove") {
		t.Fatalf("Stop = %v, want an identity refusal", err)
	}
	if len(h.killed) != 0 {
		t.Fatalf("killed %v after the pid was reused", h.killed)
	}
}

func TestStopRefusesWhenStartTimeIsUnreadable(t *testing.T) {
	listeners := []Listener{{PID: 11, Port: 5173, Addr: "127.0.0.1"}}
	h := newHarness(t, listeners, psRows, []Session{{ID: "s1", RootPIDs: []int{10}}})
	h.svc.deps.StartTime = func(int) string { return "" }
	if err := h.svc.Stop(context.Background(), "p1", "detected:5173"); err == nil || len(h.killed) != 0 {
		t.Fatalf("Stop = %v killed=%v, want a refusal without killing", err, h.killed)
	}
}

func TestStopAndRestartManaged(t *testing.T) {
	h := newHarness(t, nil, "", []Session{{ID: "s1", WorkspacePath: "/w/s1"}})
	h.managed.statuses = []previewserver.Status{{SessionID: "s1", State: previewserver.StateReady, Configuration: "web", Port: 5173, URL: "http://127.0.0.1:5173/"}}
	h.preview["s1"] = "http://127.0.0.1:5173/"

	if _, err := h.svc.Restart(context.Background(), "p1", "managed:s1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.managed.started, []string{"s1|/w/s1|web"}) {
		t.Fatalf("started = %v", h.managed.started)
	}
	if h.preview["s1"] != "http://127.0.0.1:9000/" {
		t.Fatalf("preview = %q, want it pointed at the restarted server", h.preview["s1"])
	}
	h.preview["s1"] = "http://127.0.0.1:5173/"
	if err := h.svc.Stop(context.Background(), "p1", "managed:s1"); err != nil {
		t.Fatal(err)
	}
	if len(h.managed.stopped) != 1 || h.preview["s1"] != "" {
		t.Fatalf("stopped=%v preview=%q", h.managed.stopped, h.preview["s1"])
	}
}

func TestRestartRefusedForDetected(t *testing.T) {
	h := newHarness(t, []Listener{{PID: 11, Port: 5173, Addr: "127.0.0.1"}}, psRows, []Session{{ID: "s1", RootPIDs: []int{10}}})
	if _, err := h.svc.Restart(context.Background(), "p1", "detected:5173"); err == nil {
		t.Fatal("restart of a detected server must be refused")
	}
}

func TestServerName(t *testing.T) {
	cases := map[string]string{
		"node /w/node_modules/.bin/vite --port 5173": "vite",
		"node.exe":                    "node",
		`C:\tools\python.exe -m http`: "python",
		"":                            "server",
	}
	for in, want := range cases {
		if got := serverName(in); got != want {
			t.Errorf("serverName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParsers(t *testing.T) {
	netstat := "  Proto  Local Address          Foreign Address        State           PID\r\n" +
		"  TCP    0.0.0.0:5173           0.0.0.0:0              LUISTEREN       1234\r\n" +
		"  TCP    [::1]:8080             [::]:0                 LISTENING       99\r\n" +
		"  TCP    127.0.0.1:5173         127.0.0.1:50000        ESTABLISHED     1234\r\n"
	if got := ParseNetstat(netstat); !reflect.DeepEqual(got, []Listener{{PID: 1234, Port: 5173, Addr: "0.0.0.0"}, {PID: 99, Port: 8080, Addr: "::1"}}) {
		t.Fatalf("netstat = %+v", got)
	}
	lsof := "p42\nn*:3000\nn127.0.0.1:3001\np43\nn[::1]:4000\n"
	if got := ParseLsof(lsof); !reflect.DeepEqual(got, []Listener{{PID: 42, Port: 3000}, {PID: 42, Port: 3001, Addr: "127.0.0.1"}, {PID: 43, Port: 4000, Addr: "::1"}}) {
		t.Fatalf("lsof = %+v", got)
	}
	ss := "LISTEN 0 511 127.0.0.1:3000 0.0.0.0:* users:((\"node\",pid=77,fd=19))\n"
	if got := ParseSS(ss); !reflect.DeepEqual(got, []Listener{{PID: 77, Port: 3000, Addr: "127.0.0.1"}}) {
		t.Fatalf("ss = %+v", got)
	}
}

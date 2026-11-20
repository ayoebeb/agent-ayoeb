// Package liveserver lists the dev servers running for one project: the ones AO
// started itself (previewserver) and the ones a worker started by hand, found
// by matching listening TCP ports to the project's session process trees. It
// adds an HTTP health reading per server. It never scans ports it cannot tie
// to one of the project's sessions.
package liveserver

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/previewserver"
	"github.com/aoagents/agent-orchestrator/backend/internal/procmem"
)

// Source says who started a server.
type Source string

const (
	// SourceManaged servers were started by AO from .ao/launch.json.
	SourceManaged Source = "managed"
	// SourceDetected servers were started by a worker on its own.
	SourceDetected Source = "detected"
)

// Health is the result of asking a server for one HTTP response.
type Health string

const (
	// HealthUp means the server answered an HTTP request in time.
	HealthUp Health = "up"
	// HealthDown means the connection was refused or no response came in time.
	HealthDown Health = "down"
)

// Server is one running dev server.
type Server struct {
	Key         string
	Source      Source
	Name        string
	Command     string
	Port        int
	URL         string
	SessionID   domain.SessionID
	SessionName string
	StartedAt   time.Time
	// State is the managed run's lifecycle state; detected servers are "ready".
	State      string
	Health     Health
	HealthMs   int64
	HealthNote string
	TerminalID string
	// WorkingDir is the owning session's workspace, for "open a terminal here".
	WorkingDir string
	PID        int
	Logs       []string
	// OutputAvailable is false for detected servers: AO did not start them, so
	// it holds neither their output nor a terminal.
	OutputAvailable bool
	CanRestart      bool
	Error           string
}

// Session is what the service needs to know about one project session.
type Session struct {
	ID            domain.SessionID
	Name          string
	WorkspacePath string
	// RootPIDs are the process ids the session's tree starts from.
	RootPIDs   []int
	Terminated bool
}

// Listener is one listening TCP socket.
type Listener struct {
	PID  int
	Port int
	// Addr is the bound address; "" or an unspecified address means all.
	Addr string
}

// Managed is the slice of previewserver.Manager the service uses.
type Managed interface {
	List() []previewserver.Status
	Start(ctx context.Context, id domain.SessionID, workspace, configuration string, projectEnv map[string]string) (previewserver.Status, error)
	Stop(ctx context.Context, id domain.SessionID) (previewserver.Status, error)
}

// Deps are the service's collaborators; every one is a seam for tests.
type Deps struct {
	Managed Managed
	// Sessions lists a project's sessions with their process roots.
	Sessions func(ctx context.Context, project domain.ProjectID) ([]Session, error)
	// ProjectEnv is the project's configured environment for a restart.
	ProjectEnv func(ctx context.Context, project domain.ProjectID) map[string]string
	// Listeners lists every listening TCP socket on this computer.
	Listeners func(ctx context.Context) ([]Listener, error)
	// Table snapshots the process table.
	Table func(ctx context.Context) (*procmem.Table, error)
	// Probe asks url for one HTTP response.
	Probe func(ctx context.Context, url string) (status int, err error)
	// Kill ends the given processes, leaves first.
	Kill func(ctx context.Context, pids []int) error
	// SetPreview points a session's Browser preview at url ("" clears it).
	SetPreview func(ctx context.Context, id domain.SessionID, url string) error
	// PreviewURL reads a session's current Browser preview URL.
	PreviewURL func(ctx context.Context, id domain.SessionID) string
	Now        func() time.Time
	// OwnPID is the daemon's process id, never reported as a server.
	OwnPID int
	// HealthTTL caches a probe so several polling clients cost one request.
	HealthTTL time.Duration
	// ProbeTimeout bounds one health probe.
	ProbeTimeout time.Duration
}

// Service lists and controls a project's live servers.
type Service struct {
	deps Deps

	mu     sync.Mutex
	health map[string]cachedHealth
}

type cachedHealth struct {
	at     time.Time
	health Health
	ms     int64
	note   string
}

// New builds the service, filling defaults for unset timing knobs.
func New(deps Deps) *Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.HealthTTL == 0 {
		deps.HealthTTL = 5 * time.Second
	}
	if deps.ProbeTimeout == 0 {
		deps.ProbeTimeout = 3 * time.Second
	}
	return &Service{deps: deps, health: map[string]cachedHealth{}}
}

// List returns the project's running servers, managed first, with health.
func (s *Service) List(ctx context.Context, project domain.ProjectID) ([]Server, error) {
	servers, err := s.discover(ctx, project)
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for i := range servers {
		wg.Add(1)
		go func(sv *Server) {
			defer wg.Done()
			sv.Health, sv.HealthMs, sv.HealthNote = s.healthOf(ctx, sv.URL)
		}(&servers[i])
	}
	wg.Wait()
	return servers, nil
}

func (s *Service) discover(ctx context.Context, project domain.ProjectID) ([]Server, error) {
	sessions, err := s.deps.Sessions(ctx, project)
	if err != nil {
		return nil, err
	}
	byID := make(map[domain.SessionID]Session, len(sessions))
	for _, sess := range sessions {
		byID[sess.ID] = sess
	}
	var out []Server
	managedPorts := map[int]bool{}
	for _, st := range s.deps.Managed.List() {
		sess, ok := byID[st.SessionID]
		if !ok || st.State == previewserver.StateStopped {
			continue
		}
		managedPorts[st.Port] = true
		out = append(out, Server{
			Key: "managed:" + string(st.SessionID), Source: SourceManaged,
			Name: st.Configuration, Command: st.Command, Port: st.Port, URL: st.URL,
			SessionID: st.SessionID, SessionName: sess.Name, StartedAt: st.StartedAt,
			State: string(st.State), TerminalID: st.TerminalID, WorkingDir: sess.WorkspacePath,
			Logs: st.Logs, OutputAvailable: true, CanRestart: true, Error: st.Error,
		})
	}
	detected, err := s.detect(ctx, sessions, managedPorts)
	if err == nil {
		out = append(out, detected...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source == SourceManaged
		}
		return out[i].Port < out[j].Port
	})
	return out, nil
}

// ignoredProcesses are agent harness and AO executables that open local
// sockets of their own; they are never dev servers.
var ignoredProcesses = map[string]bool{
	"claude": true, "codex": true, "gemini": true, "ao": true, "cursor": true,
	"opencode": true, "copilot": true, "aider": true, "conhost": true,
}

func (s *Service) detect(ctx context.Context, sessions []Session, managedPorts map[int]bool) ([]Server, error) {
	if s.deps.Listeners == nil || s.deps.Table == nil || len(sessions) == 0 {
		return nil, nil
	}
	listeners, err := s.deps.Listeners(ctx)
	if err != nil {
		return nil, err
	}
	table, err := s.deps.Table(ctx)
	if err != nil {
		return nil, err
	}
	owner := map[int]Session{}
	for _, sess := range sessions {
		if sess.Terminated {
			continue
		}
		for _, p := range table.Tree(sess.RootPIDs...).Processes {
			if _, taken := owner[p.PID]; !taken {
				owner[p.PID] = sess
			}
		}
	}
	byPort := map[int]Server{}
	for _, l := range listeners {
		if l.PID <= 0 || l.PID == s.deps.OwnPID || managedPorts[l.Port] || !localAddr(l.Addr) {
			continue
		}
		sess, ok := owner[l.PID]
		if !ok {
			continue
		}
		proc, _ := table.Get(l.PID)
		name := serverName(proc.Command)
		if ignoredProcesses[strings.ToLower(name)] {
			continue
		}
		if _, dup := byPort[l.Port]; dup {
			continue
		}
		byPort[l.Port] = Server{
			Key: "detected:" + strconv.Itoa(l.Port), Source: SourceDetected,
			Name: name, Command: proc.Command, Port: l.Port, URL: probeURL(l),
			SessionID: sess.ID, SessionName: sess.Name, State: string(previewserver.StateReady),
			WorkingDir: sess.WorkspacePath, PID: l.PID, Logs: []string{},
		}
	}
	out := make([]Server, 0, len(byPort))
	for _, sv := range byPort {
		out = append(out, sv)
	}
	return out, nil
}

// Stop ends one server. A detected server is re-checked against the current
// process table first, so a recycled pid is never killed.
func (s *Service) Stop(ctx context.Context, project domain.ProjectID, key string) error {
	sv, err := s.find(ctx, project, key)
	if err != nil {
		return err
	}
	if sv.Source == SourceManaged {
		previous := sv.URL
		if _, err := s.deps.Managed.Stop(ctx, sv.SessionID); err != nil {
			return err
		}
		s.clearPreview(ctx, sv.SessionID, previous)
		return nil
	}
	table, err := s.deps.Table(ctx)
	if err != nil {
		return err
	}
	pids := treePIDs(table, sv.PID)
	if len(pids) == 0 {
		return nil
	}
	return s.deps.Kill(ctx, pids)
}

// Restart stops a managed server and starts it again from its configuration.
func (s *Service) Restart(ctx context.Context, project domain.ProjectID, key string) (Server, error) {
	sv, err := s.find(ctx, project, key)
	if err != nil {
		return Server{}, err
	}
	if sv.Source != SourceManaged {
		return Server{}, apierr.Invalid("LIVE_SERVER_RESTART_UNSUPPORTED",
			"AO did not start this server, so it does not know how to start it again", nil)
	}
	sessions, err := s.deps.Sessions(ctx, project)
	if err != nil {
		return Server{}, err
	}
	workspace := ""
	for _, sess := range sessions {
		if sess.ID == sv.SessionID {
			workspace = sess.WorkspacePath
		}
	}
	var env map[string]string
	if s.deps.ProjectEnv != nil {
		env = s.deps.ProjectEnv(ctx, project)
	}
	previous := sv.URL
	status, err := s.deps.Managed.Start(ctx, sv.SessionID, workspace, sv.Name, env)
	if err != nil {
		s.clearPreview(ctx, sv.SessionID, previous)
		return Server{}, err
	}
	if status.TargetKind == previewserver.TargetApp && s.deps.SetPreview != nil {
		if err := s.deps.SetPreview(ctx, sv.SessionID, status.URL); err != nil {
			return Server{}, err
		}
	}
	return Server{Key: sv.Key, Source: SourceManaged, Name: status.Configuration, URL: status.URL, Port: status.Port, SessionID: sv.SessionID, State: string(status.State)}, nil
}

func (s *Service) find(ctx context.Context, project domain.ProjectID, key string) (Server, error) {
	servers, err := s.discover(ctx, project)
	if err != nil {
		return Server{}, err
	}
	for _, sv := range servers {
		if sv.Key == key {
			return sv, nil
		}
	}
	return Server{}, apierr.NotFound("LIVE_SERVER_NOT_FOUND", "That server is no longer running")
}

func (s *Service) clearPreview(ctx context.Context, id domain.SessionID, url string) {
	if s.deps.SetPreview == nil || s.deps.PreviewURL == nil || url == "" {
		return
	}
	if s.deps.PreviewURL(ctx, id) == url {
		_ = s.deps.SetPreview(ctx, id, "")
	}
}

func (s *Service) healthOf(ctx context.Context, url string) (Health, int64, string) {
	now := s.deps.Now()
	s.mu.Lock()
	if c, ok := s.health[url]; ok && now.Sub(c.at) < s.deps.HealthTTL {
		s.mu.Unlock()
		return c.health, c.ms, c.note
	}
	s.mu.Unlock()
	probeCtx, cancel := context.WithTimeout(ctx, s.deps.ProbeTimeout)
	defer cancel()
	started := s.deps.Now()
	status, err := s.deps.Probe(probeCtx, url)
	ms := s.deps.Now().Sub(started).Milliseconds()
	c := cachedHealth{at: now, health: HealthUp, ms: ms, note: "HTTP " + strconv.Itoa(status)}
	if err != nil {
		c.health, c.note = HealthDown, err.Error()
	}
	s.mu.Lock()
	s.health[url] = c
	s.mu.Unlock()
	return c.health, c.ms, c.note
}

// treePIDs lists pid and its descendants, leaves first.
func treePIDs(table *procmem.Table, pid int) []int {
	if _, ok := table.Get(pid); !ok {
		return nil
	}
	procs := table.Tree(pid).Processes
	out := make([]int, 0, len(procs))
	for i := len(procs) - 1; i >= 0; i-- {
		out = append(out, procs[i].PID)
	}
	return out
}

func localAddr(addr string) bool {
	addr = strings.Trim(addr, "[]")
	if addr == "" || addr == "*" {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func probeURL(l Listener) string {
	host := "127.0.0.1"
	if ip := net.ParseIP(strings.Trim(l.Addr, "[]")); ip != nil && ip.To4() == nil {
		host = "[::1]"
	} else if ip != nil && ip.IsLoopback() {
		host = ip.String()
	}
	return fmt.Sprintf("http://%s:%d/", host, l.Port)
}

// knownServers names a dev server by the tool in its command line.
var knownServers = []string{
	"vite", "next", "webpack", "nuxt", "astro", "storybook", "expo", "parcel", "remix",
	"svelte-kit", "uvicorn", "gunicorn", "flask", "django", "rails", "hugo", "jekyll",
}

// serverName is the short label for a chip: the dev tool when the command
// names one, else the executable's name.
func serverName(command string) string {
	lower := strings.ToLower(command)
	for _, k := range knownServers {
		if strings.Contains(lower, k) {
			return k
		}
	}
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "server"
	}
	base := filepath.Base(strings.ReplaceAll(fields[0], `\`, "/"))
	return strings.TrimSuffix(base, filepath.Ext(base))
}

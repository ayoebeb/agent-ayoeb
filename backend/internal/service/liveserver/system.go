package liveserver

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/aoagents/agent-orchestrator/backend/internal/process"
)

// SystemListeners lists listening TCP sockets with their owning pids by
// running the platform's own tool: netstat on Windows, lsof (or ss on Linux)
// elsewhere. It only reads; it never opens a connection.
func SystemListeners(ctx context.Context) ([]Listener, error) {
	if runtime.GOOS == "windows" {
		out, err := process.CommandContext(ctx, "netstat", "-ano", "-p", "TCP").Output()
		if err != nil {
			return nil, err
		}
		return ParseNetstat(string(out)), nil
	}
	out, err := process.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpn").Output()
	if err == nil {
		return ParseLsof(string(out)), nil
	}
	var notFound *exec.Error
	if errors.As(err, &notFound) {
		if ssOut, ssErr := process.CommandContext(ctx, "ss", "-H", "-ltnp").Output(); ssErr == nil {
			return ParseSS(string(ssOut)), nil
		}
	}
	// lsof exits 1 when nothing listens; treat that as an empty list.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil
	}
	return nil, err
}

// splitHostPort splits "127.0.0.1:5173", "[::1]:5173" and "*:5173".
func splitHostPort(s string) (string, int, bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}
	port, err := strconv.Atoi(s[i+1:])
	if err != nil || port <= 0 {
		return "", 0, false
	}
	return strings.Trim(s[:i], "[]"), port, true
}

// ParseNetstat reads `netstat -ano -p TCP`. A listening row has a foreign
// address with port 0; the state word is localized so it is not used.
func ParseNetstat(out string) []Listener {
	var res []Listener
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 5 || !strings.EqualFold(f[0], "TCP") {
			continue
		}
		host, port, ok := splitHostPort(f[1])
		if !ok {
			continue
		}
		if !strings.HasSuffix(f[2], ":0") {
			continue
		}
		pid, err := strconv.Atoi(f[len(f)-1])
		if err != nil {
			continue
		}
		res = append(res, Listener{PID: pid, Port: port, Addr: host})
	}
	return res
}

// ParseLsof reads `lsof -Fpn` output: a "p<pid>" line, then "n<addr>:<port>"
// lines for that process.
func ParseLsof(out string) []Listener {
	var res []Listener
	pid := 0
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid > 0:
			if host, port, ok := splitHostPort(line[1:]); ok {
				if host == "*" {
					host = ""
				}
				res = append(res, Listener{PID: pid, Port: port, Addr: host})
			}
		}
	}
	return res
}

// ParseSS reads `ss -H -ltnp`: the local address is column 4 and the owner
// is in users:(("name",pid=1234,fd=3)).
func ParseSS(out string) []Listener {
	var res []Listener
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		host, port, ok := splitHostPort(f[3])
		if !ok {
			continue
		}
		i := strings.Index(line, "pid=")
		if i < 0 {
			continue
		}
		rest := line[i+4:]
		end := strings.IndexAny(rest, ",)")
		if end < 0 {
			continue
		}
		pid, err := strconv.Atoi(rest[:end])
		if err != nil {
			continue
		}
		if host == "*" {
			host = ""
		}
		res = append(res, Listener{PID: pid, Port: port, Addr: host})
	}
	return res
}

var probeClient = &http.Client{
	Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// SystemProbe asks url for one HTTP response. Any status counts as an answer.
func SystemProbe(ctx context.Context, url string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return 0, err
	}
	resp, err := probeClient.Do(req) //nolint:gosec // url is built from a loopback listener or AO's own preview configuration
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			return 0, errors.New("no response within the time limit")
		}
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.CopyN(io.Discard, resp.Body, 1024)
	return resp.StatusCode, nil
}

// SystemKill ends each pid in order. On Unix it asks politely with SIGTERM; on
// Windows there is no such signal, so the process is killed.
func SystemKill(_ context.Context, pids []int) error {
	var firstErr error
	for _, pid := range pids {
		if pid <= 0 || pid == os.Getpid() {
			continue
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			continue
		}
		if runtime.GOOS == "windows" {
			err = p.Kill()
		} else {
			err = p.Signal(syscall.SIGTERM)
		}
		if err != nil && !errors.Is(err, os.ErrProcessDone) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

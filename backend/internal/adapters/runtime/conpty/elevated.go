// elevated.go - the administrator ("elevated") pty-host.
//
// On Windows the daemon starts the ordinary pty-host engine through a UAC
// prompt (see elevated_windows.go). Unlike a normal host, which serves loopback
// TCP to any local process, an elevated host serves a named pipe and admits a
// connection only when all of these hold:
//
//   - the pipe's DACL names the current user only;
//   - the connecting process is the daemon that launched the host (pipe
//     client PID), so another process of the same user is refused;
//   - the connection's first frame carries the host's per-launch token.
//
// The daemon in turn refuses a pipe whose server PID is not the process it
// launched, so a squatted pipe name never receives keystrokes. The token and
// pipe name live only in daemon memory (never the pty registry), and the host
// exits with that daemon, so an administrator shell cannot outlive its tab.
package conpty

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	elevatedAddrPrefix      = "elevated-pipe:"
	elevatedPipeArgPrefix   = "--elevated-pipe="
	elevatedTokenArgPrefix  = "--elevated-token="
	elevatedParentArgPrefix = "--parent-pid="
	// elevatedPipePrefix keeps elevated pipes recognisable in diagnostics.
	elevatedPipePrefix = `\\.\pipe\ao-elevated-`
	// maxElevatedTokenLen bounds the auth frame a host reads from a peer.
	maxElevatedTokenLen = 256
)

// elevatedAuthTimeout bounds how long an admitted process may take to present
// its token; elevatedIdleTimeout ends a host no authenticated client reached,
// such as one whose request timed out while the UAC prompt was open. Variables
// so tests need not wait them out.
var (
	elevatedAuthTimeout = 2 * time.Second
	elevatedIdleTimeout = 30 * time.Second
)

// elevatedAddr is the in-memory address of an elevated host: its pipe, its
// token, and the PID the daemon launched, which must serve the pipe.
type elevatedAddr struct {
	Pipe      string
	Token     string
	ServerPID int
}

func (a elevatedAddr) String() string {
	return elevatedAddrPrefix + a.Pipe + "|" + a.Token + "|" + strconv.Itoa(a.ServerPID)
}

func parseElevatedAddr(addr string) (elevatedAddr, bool) {
	rest, ok := strings.CutPrefix(addr, elevatedAddrPrefix)
	if !ok {
		return elevatedAddr{}, false
	}
	parts := strings.Split(rest, "|")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return elevatedAddr{}, false
	}
	pid, err := strconv.Atoi(parts[2])
	if err != nil || pid <= 0 {
		return elevatedAddr{}, false
	}
	return elevatedAddr{Pipe: parts[0], Token: parts[1], ServerPID: pid}, true
}

// elevatedHostConfig is an elevated host's launch identity, read from its argv.
type elevatedHostConfig struct {
	Pipe      string
	Token     string
	ParentPID int
}

func (c elevatedHostConfig) enabled() bool { return c.Pipe != "" }

func (c elevatedHostConfig) validate() error {
	if !strings.HasPrefix(c.Pipe, elevatedPipePrefix) || len(c.Pipe) == len(elevatedPipePrefix) {
		return fmt.Errorf("elevated pipe %q must start with %s", c.Pipe, elevatedPipePrefix)
	}
	if c.Token == "" || len(c.Token) > maxElevatedTokenLen {
		return errors.New("elevated token is missing or too long")
	}
	if c.ParentPID <= 0 {
		return errors.New("elevated host needs the launching daemon's pid")
	}
	return nil
}

func elevatedHostArgs(c elevatedHostConfig) []string {
	return []string{
		elevatedPipeArgPrefix + c.Pipe,
		elevatedTokenArgPrefix + c.Token,
		elevatedParentArgPrefix + strconv.Itoa(c.ParentPID),
	}
}

// splitElevatedArgs consumes the elevated options that lead an elevated
// pty-host's argv. A normal host has none and is returned unchanged.
func splitElevatedArgs(args []string) (elevatedHostConfig, []string) {
	var cfg elevatedHostConfig
	for len(args) > 0 {
		switch {
		case strings.HasPrefix(args[0], elevatedPipeArgPrefix):
			cfg.Pipe = strings.TrimPrefix(args[0], elevatedPipeArgPrefix)
		case strings.HasPrefix(args[0], elevatedTokenArgPrefix):
			cfg.Token = strings.TrimPrefix(args[0], elevatedTokenArgPrefix)
		case strings.HasPrefix(args[0], elevatedParentArgPrefix):
			cfg.ParentPID, _ = strconv.Atoi(strings.TrimPrefix(args[0], elevatedParentArgPrefix))
		default:
			return cfg, args
		}
		args = args[1:]
	}
	return cfg, args
}

// writeAuthFrame sends the token frame every elevated-host connection opens with.
func writeAuthFrame(conn net.Conn, token string) error {
	frame, err := EncodeMessage(MsgAuthReq, []byte(token))
	if err != nil {
		return err
	}
	if _, err := conn.Write(frame); err != nil {
		return fmt.Errorf("authenticate to elevated pty-host: %w", err)
	}
	return nil
}

// readAuthAck waits for the host's acknowledgement of this connection's token.
func readAuthAck(conn net.Conn, timeout time.Duration) error { //nolint:unused // called from elevated_windows.go
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var ack [5]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return fmt.Errorf("elevated pty-host refused the connection: %w", err)
	}
	if ack[0] != MsgAuthOK || binary.BigEndian.Uint32(ack[1:]) != 0 {
		return fmt.Errorf("elevated pty-host answered 0x%02x instead of an acknowledgement", ack[0])
	}
	return nil
}

// authListener admits only the launching daemon presenting the host's token.
// Everything else is closed before the host engine ever sees it.
type authListener struct {
	net.Listener
	token     string
	clientPID int
	peerPID   func(net.Conn) (int, error)

	authOnce sync.Once
	authed   chan struct{} // closed once a client has authenticated
}

func newAuthListener(inner net.Listener, token string, clientPID int, peerPID func(net.Conn) (int, error)) *authListener {
	return &authListener{Listener: inner, token: token, clientPID: clientPID, peerPID: peerPID, authed: make(chan struct{})}
}

// Authenticated is closed once the first client has authenticated.
func (l *authListener) Authenticated() <-chan struct{} { return l.authed }

func (l *authListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.admit(conn) {
			l.authOnce.Do(func() { close(l.authed) })
			return conn, nil
		}
		_ = conn.Close()
	}
}

// admit checks the peer process before reading anything from it, so only the
// daemon itself can hold the accept loop for up to elevatedAuthTimeout.
func (l *authListener) admit(conn net.Conn) bool {
	pid, err := l.peerPID(conn)
	if err != nil || pid != l.clientPID {
		return false
	}
	_ = conn.SetReadDeadline(time.Now().Add(elevatedAuthTimeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var header [5]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != MsgAuthReq {
		return false
	}
	n := binary.BigEndian.Uint32(header[1:])
	if n == 0 || n > maxElevatedTokenLen {
		return false
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return false
	}
	if subtle.ConstantTimeCompare(payload, []byte(l.token)) != 1 {
		return false
	}
	ack, _ := EncodeMessage(MsgAuthOK, nil)
	_ = conn.SetWriteDeadline(time.Now().Add(elevatedAuthTimeout))
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	_, err = conn.Write(ack)
	return err == nil
}

// superviseElevatedHost ends the host when no client authenticates within
// elevatedIdleTimeout. The platform part also ends it with its parent daemon.
func superviseElevatedHost(done <-chan struct{}, ln *authListener, stop func()) {
	// Read the timeout here, not inside the goroutine, so a test that restores
	// elevatedIdleTimeout afterwards cannot race with the supervisor.
	idle := elevatedIdleTimeout
	go func() {
		timer := time.NewTimer(idle)
		defer timer.Stop()
		select {
		case <-ln.Authenticated():
		case <-timer.C:
			stop()
		case <-done:
		}
	}()
}

// elevatedChildEnvCleared lists AO credentials an administrator shell must
// never inherit, whatever environment UAC hands the elevated host.
var elevatedChildEnvCleared = []string{
	"AO_SESSION_ID",
	"AO_PREVIEW_CAPABILITY",
	"AO_BROWSER_CAPABILITY",
	"AO_BROWSER_RUNTIME_TOKEN",
	"AO_CUE_COMMAND",
}

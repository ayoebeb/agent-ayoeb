//go:build windows

package httpd

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"testing"

	"golang.org/x/sys/windows"
)

// The reported bug from the other side: with Go's default options Windows lets
// a second process bind 127.0.0.1:p beside this listener's 0.0.0.0:p, and every
// loopback connection then reaches that process. SO_EXCLUSIVEADDRUSE on the
// listener refuses the second bind, which protects it from builds that do not
// probe first.
func TestLANListenerRefusesALoopbackShadowOnWindows(t *testing.T) {
	requireWildcardBind(t) // the 0.0.0.0 listener is the subject
	m := NewMobileLAN(http.NotFoundHandler(), "", 0, nil, nil)
	stopLANOnCleanup(t, m)
	port, err := m.Start(freeLANPorts(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	shadow, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err == nil {
		_ = shadow.Close()
		t.Fatalf("another socket bound 127.0.0.1:%d beside the 0.0.0.0 listener", port)
	}
	if !errors.Is(err, windows.WSAEACCES) && !errors.Is(err, windows.WSAEADDRINUSE) {
		t.Fatalf("shadow bind failed for an unexpected reason: %v", err)
	}
}

func TestLANListenerSetsExclusiveAddrUseOnWindows(t *testing.T) {
	for _, mode := range []struct {
		name     string
		start    func(*LANManager, int) (int, error)
		wildcard bool
	}{
		{"lan", (*LANManager).Start, true},
		{"tunnel-only", (*LANManager).StartLoopback, false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			lanPortTest(t, mode.wildcard)
			m := NewMobileLAN(http.NotFoundHandler(), "", 0, nil, nil)
			stopLANOnCleanup(t, m)
			if _, err := mode.start(m, freeLANPorts(t, 1)); err != nil {
				t.Fatal(err)
			}
			m.mu.Lock()
			tcp, ok := m.ln.Listener.(*net.TCPListener)
			m.mu.Unlock()
			if !ok {
				t.Fatalf("listener is %T, want *net.TCPListener", m.ln.Listener)
			}
			raw, err := tcp.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var value int
			var sockErr error
			if err := raw.Control(func(fd uintptr) {
				value, sockErr = windows.GetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse)
			}); err != nil {
				t.Fatal(err)
			}
			if sockErr != nil || value != 1 {
				t.Fatalf("SO_EXCLUSIVEADDRUSE = %d (%v), want 1", value, sockErr)
			}
		})
	}
}

package httpd

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// freeLANPorts returns the first of n consecutive ports that no socket holds.
// The base comes from the OS's ephemeral range and anything below 10000 is
// refused, so these tests never bind, probe, or connect to a real AO listener
// (3001, 3011) on the machine running them.
func freeLANPorts(t *testing.T, n int) int {
	t.Helper()
	for range 50 {
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if base < 10000 || base+n-1 > 65535 {
			continue
		}
		free := true
		for port := base; port < base+n && free; port++ {
			free = !lanPortHeld(context.Background(), port)
		}
		if free {
			return base
		}
	}
	t.Fatalf("no run of %d free ports found", n)
	return 0
}

// holdPort stands in for another process, such as a second AO instance, that
// owns port on one address with Go's default socket options.
func holdPort(t *testing.T, network, host string, port int) {
	t.Helper()
	ln, err := net.Listen(network, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		if network == "tcp6" {
			t.Skipf("IPv6 unavailable here: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

func stopLANOnCleanup(t *testing.T, m *LANManager) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Stop(ctx)
	})
}

func TestLANManagerSkipsAPortHeldOnAnyAddress(t *testing.T) {
	// wildcard marks a holder or mode that binds 0.0.0.0, [::] or both. Those
	// cases are the point of this test and run wherever a wildcard bind is
	// allowed; the loopback-only pairs run everywhere.
	holders := []struct {
		name, network, host string
		wildcard            bool
	}{
		{"ipv4 wildcard", "tcp4", "0.0.0.0", true},
		{"ipv4 loopback", "tcp4", "127.0.0.1", false},
		{"ipv6 wildcard", "tcp6", "::", true},
		{"ipv6 loopback", "tcp6", "::1", false},
		{"dual-stack wildcard", "tcp", "", true},
	}
	modes := []struct {
		name     string
		start    func(*LANManager, int) (int, error)
		wildcard bool
	}{
		{"lan", (*LANManager).Start, true},
		{"tunnel-only", (*LANManager).StartLoopback, false},
	}
	for _, h := range holders {
		for _, mode := range modes {
			// "tunnel-only beside ipv4 wildcard" is the reported bug: a second
			// instance bound 127.0.0.1 next to the installed AO's 0.0.0.0 and
			// took its loopback traffic.
			t.Run(mode.name+" beside "+h.name, func(t *testing.T) {
				lanPortTest(t, h.wildcard || mode.wildcard)
				base := freeLANPorts(t, 2)
				holdPort(t, h.network, h.host, base)
				m := NewMobileLAN(http.NotFoundHandler(), "", 0, nil, nil)
				stopLANOnCleanup(t, m)

				got, err := mode.start(m, base)
				if err != nil {
					t.Fatal(err)
				}
				if got != base+1 || m.BoundPort() != base+1 {
					t.Fatalf("bound %d (BoundPort %d), want the next free port %d; %d is held on %s",
						got, m.BoundPort(), base+1, base, h.name)
				}
			})
		}
	}
}

func TestLANManagerKeepsAFreePreferredPort(t *testing.T) {
	lanPortTest(t, false)
	base := freeLANPorts(t, 1)
	m := NewMobileLAN(http.NotFoundHandler(), "", base, nil, nil)
	stopLANOnCleanup(t, m)
	got, err := m.StartLoopback(0) // 0 selects the manager's default
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatalf("bound %d, want the free default %d", got, base)
	}
}

func TestLANManagerFailsWhenEverySearchedPortIsInUse(t *testing.T) {
	lanPortTest(t, false)
	base := freeLANPorts(t, lanPortSearchSpan)
	for port := base; port < base+lanPortSearchSpan; port++ {
		holdPort(t, "tcp4", "127.0.0.1", port)
	}
	m := NewMobileLAN(http.NotFoundHandler(), "", 0, nil, nil)
	stopLANOnCleanup(t, m)

	_, err := startLANForTest(m, base)
	if err == nil {
		t.Fatal("Start bound a port inside a fully held range")
	}
	want := strconv.Itoa(base) + "-" + strconv.Itoa(base+lanPortSearchSpan-1) + " are all in use by other processes"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the held range (%q)", err, want)
	}
	if m.Running() || m.BoundPort() != 0 {
		t.Fatal("a failed Start left the listener marked running")
	}
}

func TestLANManagerRejectsAPortOutsideTheTCPRange(t *testing.T) {
	for _, port := range []int{-1, 65536, 70000} {
		t.Run(strconv.Itoa(port), func(t *testing.T) {
			m := NewMobileLAN(http.NotFoundHandler(), "", 0, nil, nil)
			stopLANOnCleanup(t, m)
			_, err := m.StartLoopback(port)
			if err == nil || !strings.Contains(err.Error(), "port "+strconv.Itoa(port)+" is outside 1-65535") {
				t.Fatalf("StartLoopback(%d) error %v, want it to name the valid range", port, err)
			}
			if m.Running() {
				t.Fatal("a rejected port left the listener running")
			}
		})
	}
}

// Enable starts from the saved lastPort rather than the default, persists the
// port it actually bound, and reports it in the status it returns.
func TestBridgeEnableReusesLastPortAndMovesPastAHeldOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		enable func(*controllers.BridgeService) (controllers.MobileStatusResponse, error)
		// The other instance's bind that would shadow this mode's listener.
		otherNetwork, otherHost string
		// wildcard: this mode binds 0.0.0.0, or the other instance holds a
		// wildcard address. The last case is its loopback-only twin.
		wildcard bool
	}{
		{"lan", (*controllers.BridgeService).Enable, "tcp4", "127.0.0.1", true},
		{"lan only", (*controllers.BridgeService).EnableLANOnly, "tcp4", "127.0.0.1", true},
		{"tunnel only", (*controllers.BridgeService).EnableTunnelOnly, "tcp4", "0.0.0.0", true},
		{"tunnel only beside loopback", (*controllers.BridgeService).EnableTunnelOnly, "tcp4", "127.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lanPortTest(t, tc.wildcard)
			last := freeLANPorts(t, 2)
			// Never the real default: if Enable ignored lastPort it would bind
			// this free port instead, and the assertions below would catch it.
			defaultPort := freeLANPorts(t, 1)
			for defaultPort >= last && defaultPort <= last+1 {
				defaultPort = freeLANPorts(t, 1)
			}
			path := filepath.Join(t.TempDir(), "mobile", "config.json")
			if err := mobilebridge.Save(path, mobilebridge.State{LastPort: last}); err != nil {
				t.Fatal(err)
			}
			m := NewMobileLAN(http.NotFoundHandler(), "", defaultPort, nil, nil)
			stopLANOnCleanup(t, m)
			bridge := &controllers.BridgeService{
				LAN:                m,
				ConfigPath:         path,
				DefaultPort:        defaultPort,
				PickLANHosts:       func() []string { return nil },
				PickTailscaleHosts: func() []string { return nil },
			}

			assertPort := func(res controllers.MobileStatusResponse, want int) {
				t.Helper()
				state, err := mobilebridge.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if res.Port != want || m.BoundPort() != want || state.LastPort != want {
					t.Fatalf("reported %d, bound %d, saved %d; want %d", res.Port, m.BoundPort(), state.LastPort, want)
				}
			}
			res, err := tc.enable(bridge)
			if err != nil {
				t.Fatal(err)
			}
			assertPort(res, last)

			if err := bridge.Disable(); err != nil {
				t.Fatal(err)
			}
			holdPort(t, tc.otherNetwork, tc.otherHost, last)
			res, err = tc.enable(bridge)
			if err != nil {
				t.Fatal(err)
			}
			assertPort(res, last+1)
		})
	}
}

// openServedConn connects to the listener on port and waits for one response,
// so the listener has accepted the connection before the caller disables it.
func openServedConn(t *testing.T, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("GET /probe HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatalf("no response before disable: %v", err)
	}
	return conn
}

// Disable closes the listener's accepted connections from the server side, so
// their sockets linger on its port (FIN_WAIT_2 while the client stays
// connected, TIME_WAIT after it closes). Re-enabling must not mistake those for
// another process and move the port, which would strand paired devices.
func TestBridgeReEnableKeepsItsPortWithALingeringConnection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		enable   func(*controllers.BridgeService) (controllers.MobileStatusResponse, error)
		wildcard bool // binds 0.0.0.0
	}{
		{"lan", (*controllers.BridgeService).Enable, true},
		{"tunnel only", (*controllers.BridgeService).EnableTunnelOnly, false},
	} {
		for _, client := range []string{"client still connected", "client closed"} {
			t.Run(tc.name+"/"+client, func(t *testing.T) {
				lanPortTest(t, tc.wildcard)
				port := freeLANPorts(t, 2)
				path := filepath.Join(t.TempDir(), "mobile", "config.json")
				m := NewMobileLAN(http.NotFoundHandler(), "", port, nil, nil)
				stopLANOnCleanup(t, m)
				bridge := &controllers.BridgeService{
					LAN:                m,
					ConfigPath:         path,
					DefaultPort:        port,
					PickLANHosts:       func() []string { return nil },
					PickTailscaleHosts: func() []string { return nil },
				}
				if _, err := tc.enable(bridge); err != nil {
					t.Fatal(err)
				}
				conn := openServedConn(t, port)
				if err := bridge.Disable(); err != nil {
					t.Fatal(err)
				}
				if client == "client closed" {
					_ = conn.Close()
				}

				res, err := tc.enable(bridge)
				if err != nil {
					t.Fatal(err)
				}
				state, err := mobilebridge.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				if res.Port != port || state.LastPort != port {
					t.Fatalf("re-enable reported %d and saved %d; want the same port %d", res.Port, state.LastPort, port)
				}
			})
		}
	}
}

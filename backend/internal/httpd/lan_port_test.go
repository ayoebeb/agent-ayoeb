package httpd

import (
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
	holders := []struct{ name, network, host string }{
		{"ipv4 wildcard", "tcp4", "0.0.0.0"},
		{"ipv4 loopback", "tcp4", "127.0.0.1"},
		{"ipv6 wildcard", "tcp6", "::"},
		{"ipv6 loopback", "tcp6", "::1"},
		{"dual-stack wildcard", "tcp", ""},
	}
	modes := []struct {
		name  string
		start func(*LANManager, int) (int, error)
	}{
		{"lan", (*LANManager).Start},
		{"tunnel-only", (*LANManager).StartLoopback},
	}
	for _, h := range holders {
		for _, mode := range modes {
			// "tunnel-only beside ipv4 wildcard" is the reported bug: a second
			// instance bound 127.0.0.1 next to the installed AO's 0.0.0.0 and
			// took its loopback traffic.
			t.Run(mode.name+" beside "+h.name, func(t *testing.T) {
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
	base := freeLANPorts(t, lanPortSearchSpan)
	for port := base; port < base+lanPortSearchSpan; port++ {
		holdPort(t, "tcp4", "127.0.0.1", port)
	}
	m := NewMobileLAN(http.NotFoundHandler(), "", 0, nil, nil)
	stopLANOnCleanup(t, m)

	_, err := m.Start(base)
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

// Enable starts from the saved lastPort rather than the default, persists the
// port it actually bound, and reports it in the status it returns.
func TestBridgeEnableReusesLastPortAndMovesPastAHeldOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		enable func(*controllers.BridgeService) (controllers.MobileStatusResponse, error)
		// The other instance's bind that would shadow this mode's listener.
		otherNetwork, otherHost string
	}{
		{"lan", (*controllers.BridgeService).Enable, "tcp4", "127.0.0.1"},
		{"lan only", (*controllers.BridgeService).EnableLANOnly, "tcp4", "127.0.0.1"},
		{"tunnel only", (*controllers.BridgeService).EnableTunnelOnly, "tcp4", "0.0.0.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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

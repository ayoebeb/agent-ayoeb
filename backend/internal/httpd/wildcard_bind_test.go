package httpd

import (
	"net"
	"os"
	"runtime"
	"testing"
)

// wildcardBindEnv opts a Windows run into the tests that bind 0.0.0.0 or [::].
const wildcardBindEnv = "AO_TEST_WILDCARD_BIND"

// wildcardBindAllowed reports whether a test may bind a wildcard address.
//
// Windows Defender Firewall asks "allow network access?" the first time an
// executable listens on a non-loopback address, and `go test` builds a new
// executable at a new temp path every run, so every run raises a fresh prompt.
// The prompts are hidden dialog hosts (PickerHost.exe) that stay until
// someone answers them: one afternoon of runs left 391 holding 5.6 GB.
//
// Every other platform keeps the full wildcard coverage. So does a Windows run
// that sets AO_TEST_WILDCARD_BIND=1, which the Windows CI job does because a
// runner has no interactive firewall.
func wildcardBindAllowed() bool {
	return runtime.GOOS != "windows" || os.Getenv(wildcardBindEnv) == "1"
}

// requireWildcardBind skips a test whose subject is binding, or sharing a port
// with, a wildcard address.
func requireWildcardBind(t *testing.T) {
	t.Helper()
	if !wildcardBindAllowed() {
		t.Skipf("binds a wildcard address, which raises a Windows Firewall prompt for every new test binary; set %s=1 to run it (CI does)", wildcardBindEnv)
	}
}

// startLANForTest starts m on an OS-chosen port for a test whose subject is
// what the listener serves, not where it binds. Where a wildcard bind is
// allowed it takes the real Start path; elsewhere it binds loopback, which
// serves the same handler. TestLANManagerStartBindsEveryInterface pins that
// Start itself stays on every interface.
func startLANForTest(m *LANManager, port int) (int, error) {
	if wildcardBindAllowed() {
		return m.Start(port)
	}
	return m.StartLoopback(port)
}

// narrowLANProbesToLoopback limits the product's port-conflict probes to
// loopback addresses for the rest of the test unless a wildcard bind is
// allowed. The probes bind 0.0.0.0, [::] and the dual-stack wildcard on every
// candidate port, even for a loopback-only listener, so without this a test
// that never meant to touch a wildcard address still raises the prompt.
func narrowLANProbesToLoopback(t *testing.T) {
	t.Helper()
	if wildcardBindAllowed() {
		return
	}
	saved := lanPortProbes
	loopback := make([]struct{ network, host string }, 0, len(saved))
	for _, p := range saved {
		if ip := net.ParseIP(p.host); ip != nil && ip.IsLoopback() {
			loopback = append(loopback, p)
		}
	}
	if len(loopback) == 0 {
		t.Fatal("lanPortProbes has no loopback probe to fall back to")
	}
	lanPortProbes = loopback
	t.Cleanup(func() { lanPortProbes = saved })
}

// lanPortTest readies a test that selects listener ports. wildcard is true when
// the test binds or holds a wildcard address itself: it is skipped where that is
// not allowed. Otherwise the port probes are limited to loopback there, so the
// test keeps its coverage without a prompt.
func lanPortTest(t *testing.T, wildcard bool) {
	t.Helper()
	if wildcard {
		requireWildcardBind(t)
		return
	}
	narrowLANProbesToLoopback(t)
}

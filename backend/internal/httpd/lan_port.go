package httpd

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// lanPortSearchSpan is how many consecutive ports, starting at the preferred
// one, the authenticated listener tries before giving up.
const lanPortSearchSpan = 20

// lanPortProbes are the addresses where another socket on the same port would
// share or shadow the authenticated listener. A loopback client reaches the
// socket with the most specific match, so when one AO instance owns 0.0.0.0:p
// and another binds 127.0.0.1:p (or the reverse), the local cloudflared and
// Tailscale Serve connectors, which dial 127.0.0.1, reach the wrong instance.
// Windows and macOS both allow that pair of binds. Each exact address catches
// a holder of that address on every OS; the dual-stack wildcard also catches a
// holder on any other address where the OS reports one (Windows with
// SO_EXCLUSIVEADDRUSE, Linux).
var lanPortProbes = []struct{ network, host string }{
	{"tcp4", "0.0.0.0"},
	{"tcp4", "127.0.0.1"},
	{"tcp6", "::"},
	{"tcp6", "::1"},
	{"tcp", ""},
}

// lanPortHeld reports whether another socket holds port on any probed address.
// An address family the host lacks fails with a different error and is
// skipped. Probes run before the real bind because, once this process holds
// the port, its own socket would make the overlapping probes fail.
func lanPortHeld(ctx context.Context, port int) bool {
	lc := net.ListenConfig{Control: lanBindControl}
	for _, p := range lanPortProbes {
		ln, err := lc.Listen(ctx, p.network, net.JoinHostPort(p.host, strconv.Itoa(port)))
		if err == nil {
			_ = ln.Close()
			continue
		}
		if isPortUnavailable(err) {
			return true
		}
	}
	return false
}

// listenLAN binds host on preferred, or on the next port in the search span
// that no other socket holds on any probed address. It never shares a port: a
// shared port lets whichever socket is more specific take the other's
// connections. skipped lists the ports it passed over. Port 0 asks the OS.
func listenLAN(ctx context.Context, host string, preferred int) (ln net.Listener, skipped []int, err error) {
	lc := net.ListenConfig{Control: lanBindControl}
	if preferred == 0 {
		ln, err = lc.Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return nil, nil, fmt.Errorf("bind authenticated listener on %s: %w", host, err)
		}
		return ln, nil, nil
	}
	last := min(preferred+lanPortSearchSpan-1, 65535)
	for port := preferred; port <= last; port++ {
		if lanPortHeld(ctx, port) {
			skipped = append(skipped, port)
			continue
		}
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		ln, err = lc.Listen(ctx, "tcp", addr)
		if err == nil {
			return ln, skipped, nil
		}
		if !isPortUnavailable(err) {
			return nil, skipped, fmt.Errorf("bind authenticated listener %s: %w", addr, err)
		}
		skipped = append(skipped, port) // taken between the probe and the bind
	}
	return nil, skipped, fmt.Errorf("bind authenticated listener on %s: ports %d-%d are all in use by other processes; stop the process using port %d or free a port in that range",
		host, preferred, last, preferred)
}

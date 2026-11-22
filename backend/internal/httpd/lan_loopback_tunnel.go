package httpd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// lanLoopbackTunnelPath lets a paired desktop reach a dev server on this
// computer's own loopback (for example `npm run dev` on localhost:5173) from the
// Browser panel of a session that runs here. One WebSocket carries the bytes of
// one TCP connection, so TLS stays end to end between the desktop's browser and
// the dev server and WebSockets (hot reload) pass through unchanged.
//
// It is served on the LAN listener only, behind the same authMiddleware as every
// other route (a paired desktop's password, the host id check), never on the
// primary loopback listener. The daemon dials loopback addresses and nothing
// else: see loopbackDialTargets.
const lanLoopbackTunnelPath = "/api/v1/loopback-tunnel"

const (
	// A dev server that does not answer fails fast instead of holding a slot.
	loopbackDialTimeout = 5 * time.Second
	// A connection that carries no byte in either direction this long is dropped.
	// Dev-server hot-reload sockets idle quietly, so this is generous.
	loopbackIdleTimeout = 15 * time.Minute
	// Concurrent tunneled connections per daemon. A page opens a handful per
	// origin; this stops a runaway client from exhausting the host's sockets.
	maxLoopbackTunnels = 256
)

var errNotLoopback = errors.New("loopback tunnel only reaches loopback addresses")

// loopbackDialTargets turns the requested host and port into the literal
// loopback addresses the daemon may dial. It never resolves a name through DNS:
// "localhost" and "*.localhost" (which browsers treat as loopback) map to
// 127.0.0.1 and ::1, an IP literal must itself be loopback, and anything else is
// refused. A name that merely resolves to a loopback address is refused too, so
// DNS on this computer cannot widen what the tunnel reaches.
func loopbackDialTargets(host, port string) ([]string, error) {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("invalid port")
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return nil, errNotLoopback
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return []string{
			net.JoinHostPort("127.0.0.1", port),
			net.JoinHostPort("::1", port),
		}, nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, errNotLoopback
	}
	return []string{net.JoinHostPort(ip.String(), port)}, nil
}

func (m *LANManager) serveLoopbackTunnel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	targets, err := loopbackDialTargets(q.Get("host"), q.Get("port"))
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, errNotLoopback) {
			code = http.StatusForbidden
		}
		http.Error(w, err.Error(), code)
		return
	}
	select {
	case m.loopbackSlots <- struct{}{}:
		defer func() { <-m.loopbackSlots }()
	default:
		http.Error(w, "too many loopback tunnels", http.StatusServiceUnavailable)
		return
	}
	// Dial before upgrading: a refused port is then an HTTP status the desktop
	// can read, not a WebSocket that opens and closes.
	var target net.Conn
	for _, addr := range targets {
		dialer := net.Dialer{Timeout: loopbackDialTimeout}
		if target, err = dialer.DialContext(r.Context(), "tcp", addr); err == nil {
			break
		}
	}
	if target == nil {
		http.Error(w, "nothing is listening on that loopback port", http.StatusBadGateway)
		return
	}
	defer func() { _ = target.Close() }()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	conn := websocket.NetConn(context.WithoutCancel(r.Context()), ws, websocket.MessageBinary)
	defer func() { _ = conn.Close() }()
	pumpIdle(conn, target, loopbackIdleTimeout)
}

// pumpIdle copies both ways until either side ends or idle passes with no byte
// in either direction. Each direction refreshes the shared deadline.
func pumpIdle(a, b net.Conn, idle time.Duration) {
	var mu sync.Mutex
	var deadline time.Time
	refresh := func() {
		mu.Lock()
		defer mu.Unlock()
		deadline = time.Now().Add(idle)
		_ = a.SetDeadline(deadline)
		_ = b.SetDeadline(deadline)
	}
	refresh()
	var wg sync.WaitGroup
	wg.Add(2)
	copyDir := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				refresh()
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		// Either end finishing ends the connection; closing unblocks the other copy.
		_ = a.Close()
		_ = b.Close()
	}
	go copyDir(a, b)
	go copyDir(b, a)
	wg.Wait()
}

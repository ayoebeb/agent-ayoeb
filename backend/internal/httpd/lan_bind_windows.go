//go:build windows

package httpd

import (
	"errors"
	"syscall"

	"golang.org/x/sys/windows"
)

// soExclusiveAddrUse is winsock2.h's SO_EXCLUSIVEADDRUSE, defined there as
// ~SO_REUSEADDR. golang.org/x/sys/windows does not export it.
const soExclusiveAddrUse = ^windows.SO_REUSEADDR

// lanBindControl sets SO_EXCLUSIVEADDRUSE before bind. Without it, Windows lets
// a second process bind 127.0.0.1:p while this one owns 0.0.0.0:p, and the more
// specific socket then receives every loopback connection, including the
// cloudflared and Tailscale Serve connectors'. Set on the listener, the option
// refuses that later bind; set on a wildcard probe, the probe fails when any
// address in its family holds the port.
func lanBindControl(_, _ string, c syscall.RawConn) error {
	var sockErr error
	if err := c.Control(func(fd uintptr) {
		sockErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, soExclusiveAddrUse, 1)
	}); err != nil {
		return err
	}
	return sockErr
}

// isPortUnavailable reports a bind refused because the port belongs to another
// socket: in use, held exclusively, or inside a reserved range such as a
// Hyper-V excluded port range (the last two answer WSAEACCES).
func isPortUnavailable(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, windows.WSAEACCES)
}

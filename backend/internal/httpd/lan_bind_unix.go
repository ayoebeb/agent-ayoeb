//go:build !windows

package httpd

import (
	"errors"
	"syscall"
)

// lanBindControl keeps Go's listener defaults. Linux already refuses a bind
// that overlaps another listening socket, even with SO_REUSEADDR. macOS allows
// a specific address beside a wildcard one, which the exact-address probes in
// probeLANPort catch; there is no option that stops a later process doing it.
var lanBindControl func(network, address string, c syscall.RawConn) error

// isPortUnavailable reports a bind refused because another socket holds the port.
func isPortUnavailable(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

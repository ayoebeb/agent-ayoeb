//go:build !windows

package conpty

import (
	"context"
	"net"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// macOS and Linux offer no administrator terminal: a root shell would run in
// an ordinary host that any local process can reach. Users type sudo in a
// user terminal instead.

func defaultSpawnElevatedHost(context.Context, string, string, []string, HostOptions) (string, int, error) {
	return "", 0, ports.ErrElevationUnsupported
}

func dialElevatedHost(context.Context, elevatedAddr, time.Duration) (net.Conn, error) {
	return nil, ports.ErrElevationUnsupported
}

func listenElevatedHost(elevatedHostConfig) (*authListener, error) {
	return nil, ports.ErrElevationUnsupported
}

func watchElevatedParent(_ <-chan struct{}, _ int, stop func()) { stop() }

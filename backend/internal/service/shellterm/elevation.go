package shellterm

import (
	"context"
	"crypto/subtle"
	"path/filepath"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
)

// Administrator ("elevated") terminals are a local-desktop-only capability:
//
//   - Opening one shows a UAC prompt on this computer, so it is refused for
//     every request that did not come from the AO desktop app sitting in front
//     of it: the LAN listener (remote hosts, phones, tunnels), an agent's CLI
//     call (it names its session), or any loopback caller without the desktop
//     credential (a script, curl, an automation).
//   - Once open, the same rule decides who may see, attach to (read or type
//     into), rename or close it. Typing into an open administrator shell needs
//     no prompt, so this is the boundary that keeps agents out.
//
// The desktop credential is a random secret the desktop app generates once per
// app run and hands only to a daemon it spawns, over that daemon's private stdin
// pipe. Both sides keep it in memory only: nothing on disk (not
// browser-runtime.key, not running.json) lets another process of this user
// compute it, so reading AO's files is not enough to reach an administrator
// shell. The app's main process adds it only to its own window's requests to
// this daemon, so the renderer, agent pages and remote hosts never hold it.
// Code that can read another process's memory is out of scope; UAC stays the
// hard boundary for opening one.

// elevatedHandlePrefix names an administrator terminal's runtime handle, so the
// terminal mux and the list can recognise one without a database column.
// Handles are minted only by the daemon, and an ordinary handle is
// "shellterm-" followed by hex, which can never spell "admin-".
const elevatedHandlePrefix = "shellterm-admin-"

// IsElevatedHandleID reports whether a runtime handle names an administrator
// terminal. Runtime adapters may wrap the id (ptyhost-v1:shellterm-...), so the
// final component is what counts.
func IsElevatedHandleID(handleID string) bool {
	if i := strings.LastIndexByte(handleID, ':'); i >= 0 {
		handleID = handleID[i+1:]
	}
	return strings.HasPrefix(handleID, elevatedHandlePrefix)
}

// SetDesktopElevationSecret enables administrator terminals for requests that
// carry this per-app-run secret (see the package comment above). Empty, as in a
// daemon the desktop app adopted rather than spawned, keeps them unavailable.
// Call once at boot, before serving requests.
func (s *Service) SetDesktopElevationSecret(secret string) {
	s.desktopElevationSecret = secret
}

// Reasons an administrator terminal is unavailable to a request.
const (
	ElevationUnsupported = "unsupported"  // this platform has no administrator terminal
	ElevationRemote      = "remote"       // the request reached this daemon over the network
	ElevationDesktopOnly = "desktop-only" // not the AO desktop app on this computer
)

// ElevationAccess says whether a request may open and use administrator
// terminals, and if not, why.
type ElevationAccess struct {
	Available bool
	Reason    string
}

// ElevationAccess applies the local-desktop-only rule to a request context.
func (s *Service) ElevationAccess(ctx context.Context) ElevationAccess {
	switch {
	case !s.elevationSupported:
		return ElevationAccess{Reason: ElevationUnsupported}
	case requestscope.IsLAN(ctx):
		return ElevationAccess{Reason: ElevationRemote}
	case caller.From(ctx) != "":
		return ElevationAccess{Reason: ElevationDesktopOnly}
	case s.desktopElevationSecret == "",
		subtle.ConstantTimeCompare([]byte(requestscope.DesktopCredential(ctx)), []byte(s.desktopElevationSecret)) != 1:
		return ElevationAccess{Reason: ElevationDesktopOnly}
	default:
		return ElevationAccess{Available: true}
	}
}

func elevationDenied(access ElevationAccess) error {
	switch access.Reason {
	case ElevationUnsupported:
		return apierr.Invalid("ELEVATED_TERMINAL_UNSUPPORTED",
			"Administrator terminals are only available on Windows", nil)
	case ElevationRemote:
		return apierr.Forbidden("ELEVATED_TERMINAL_REMOTE",
			"Administrator terminals can only be opened at this computer, not remotely")
	default:
		return apierr.Forbidden("ELEVATED_TERMINAL_DESKTOP_ONLY",
			"Administrator terminals can only be opened from the AO desktop app on this computer")
	}
}

// elevatedShellAllowed limits what an administrator terminal may launch to the
// shells this daemon detected (by selector or exact path) and "auto", which
// resolves among them. A saved default shell qualifies when it is one of
// those. An arbitrary path never does: the saved setting lives in a file any
// process of this user can write, and the UAC prompt names AO, not the
// program, so an agent must not be able to choose what it starts.
func (s *Service) elevatedShellAllowed(shell string) bool {
	shell = strings.TrimSpace(shell)
	if shell == "" || strings.EqualFold(shell, "auto") {
		return true
	}
	for _, candidate := range s.detectShells().Shells {
		if strings.EqualFold(shell, candidate.ID) || sameExecutable(shell, candidate.Path) {
			return true
		}
	}
	return false
}

func sameExecutable(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// requireElevationAccess refuses a request that may not use the handle when
// the handle names an administrator terminal. Ordinary handles pass.
func (s *Service) requireElevationAccess(ctx context.Context, handleID string) error {
	if !IsElevatedHandleID(handleID) {
		return nil
	}
	if access := s.ElevationAccess(ctx); !access.Available {
		return elevationDenied(access)
	}
	return nil
}

// AuthorizeTerminalAttach is the terminal mux's gate: an administrator
// terminal attaches (and so can be read and typed into) only for the local
// desktop app. The mux carries the request's LAN flag, caller and desktop
// credential on its context.
func (s *Service) AuthorizeTerminalAttach(ctx context.Context, terminalID string) error {
	return s.requireElevationAccess(ctx, terminalID)
}

// SetElevationSupportedForTest lets tests outside this package exercise the
// Windows-only rule on any platform.
func (s *Service) SetElevationSupportedForTest(supported bool) {
	s.elevationSupported = supported
}

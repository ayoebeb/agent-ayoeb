package shellterm

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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
// The desktop credential is derived from the browser-runtime token the app
// hands its daemon over a private stdin pipe at spawn; the app's main process
// adds it only to its own window's requests to this daemon, so the renderer,
// agent pages and remote hosts never hold it.

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

const desktopElevationLabel = "ao-desktop-elevated-terminal-v1"

// DesktopElevationToken derives the desktop credential from the browser-runtime
// token the desktop app gave this daemon. The app's main process derives the
// same value (frontend/src/main/desktop-elevation.ts).
func DesktopElevationToken(desktopRuntimeToken string) string {
	if desktopRuntimeToken == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(desktopRuntimeToken))
	mac.Write([]byte(desktopElevationLabel))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// SetDesktopElevationToken enables administrator terminals for requests that
// carry this credential. Empty (a daemon the desktop app did not start) keeps
// them unavailable. Call once at boot, before serving requests.
func (s *Service) SetDesktopElevationToken(token string) {
	s.desktopElevationToken = token
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
	case s.desktopElevationToken == "",
		subtle.ConstantTimeCompare([]byte(requestscope.DesktopCredential(ctx)), []byte(s.desktopElevationToken)) != 1:
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

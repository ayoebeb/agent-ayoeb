package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
)

const (
	// sharedBrowserProfileChain is the broker's per-session command chain these
	// internal actions share. It is not a session: the desktop app dispatches
	// the actions before it reads the session id.
	sharedBrowserProfileChain domain.SessionID = "__shared-browser-profiles"
	// The actions are internal to this wiring: they are not in the
	// service/browser allowlist, so `ao browser` cannot send them.
	sharedBrowserProfileListAction   = "__browser-profile-share-list"
	sharedBrowserProfileExportAction = "__browser-profile-share-export"

	sharedBrowserProfileListTimeout   = 20 * time.Second
	sharedBrowserProfileExportTimeout = 60 * time.Second

	// A sealed page is a 3 MiB page in base64url plus its tag, well under the
	// desktop bridge frame. The receiving machine opens it and validates every
	// field before it writes anything.
	sharedBrowserProfileMaxSealedBytes = 6 << 20
)

var (
	sharedBrowserProfileSealedKey  = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	sharedBrowserProfileSealedIV   = regexp.MustCompile(`^[A-Za-z0-9_-]{16}$`)
	sharedBrowserProfileSealedData = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// errSharedBrowserProfileDesktopFailed carries no desktop text: the desktop's
// message could in principle describe the data it was reading.
var errSharedBrowserProfileDesktopFailed = errors.New("desktop app could not read shared browser profiles")

type sharedBrowserProfileBroker interface {
	Execute(ctx context.Context, id domain.SessionID, action string, args map[string]interface{}) (browserruntime.Result, error)
}

// sharedBrowserProfilesViaDesktop reads the browser profiles this machine
// shares from the desktop app, which owns the Electron cookie partitions and
// the history files, over the token-authenticated browser runtime link.
type sharedBrowserProfilesViaDesktop struct {
	broker sharedBrowserProfileBroker
	log    *slog.Logger
}

func newSharedBrowserProfilesViaDesktop(broker sharedBrowserProfileBroker, log *slog.Logger) sharedBrowserProfilesViaDesktop {
	if log == nil {
		log = slog.Default()
	}
	return sharedBrowserProfilesViaDesktop{broker: broker, log: log}
}

func (s sharedBrowserProfilesViaDesktop) ListSharedBrowserProfiles(ctx context.Context, receiver controllers.SharedBrowserProfileReceiver) (controllers.SealedBrowserProfilePayload, error) {
	ctx, cancel := context.WithTimeout(ctx, sharedBrowserProfileListTimeout)
	defer cancel()
	return s.sealed(ctx, sharedBrowserProfileListAction, receiverArgs(receiver))
}

func (s sharedBrowserProfilesViaDesktop) ExportSharedBrowserProfile(ctx context.Context, receiver controllers.SharedBrowserProfileReceiver, profileID, cursor string) (controllers.SealedBrowserProfilePayload, error) {
	ctx, cancel := context.WithTimeout(ctx, sharedBrowserProfileExportTimeout)
	defer cancel()
	args := receiverArgs(receiver)
	args["profileId"] = profileID
	if cursor != "" {
		args["cursor"] = cursor
	}
	return s.sealed(ctx, sharedBrowserProfileExportAction, args)
}

func receiverArgs(receiver controllers.SharedBrowserProfileReceiver) map[string]interface{} {
	return map[string]interface{}{"ticket": receiver.Ticket, "signature": receiver.Signature}
}

// sealed runs one action and passes on only a well-formed sealed payload. The
// daemon cannot read it: the desktop sealed it to the receiver's ticket key.
func (s sharedBrowserProfilesViaDesktop) sealed(ctx context.Context, action string, args map[string]interface{}) (controllers.SealedBrowserProfilePayload, error) {
	result, err := s.broker.Execute(ctx, sharedBrowserProfileChain, action, args)
	if err != nil {
		return controllers.SealedBrowserProfilePayload{}, s.desktopError(err)
	}
	var out controllers.SealedBrowserProfilePayload
	if err := decodeDesktopValue(result.Value, &out); err != nil ||
		out.Version != 1 ||
		!sharedBrowserProfileSealedKey.MatchString(out.Key) ||
		!sharedBrowserProfileSealedIV.MatchString(out.IV) ||
		len(out.Data) > sharedBrowserProfileMaxSealedBytes ||
		!sharedBrowserProfileSealedData.MatchString(out.Data) {
		s.log.Warn("shared browser profiles: desktop returned a malformed sealed payload")
		return controllers.SealedBrowserProfilePayload{}, errSharedBrowserProfileDesktopFailed
	}
	return out, nil
}

// desktopError maps a bridge failure onto the controller's fixed errors. Only
// the stable code is logged, never the desktop's message.
func (s sharedBrowserProfilesViaDesktop) desktopError(err error) error {
	if errors.Is(err, browserruntime.ErrUnavailable) {
		return controllers.ErrSharedBrowserProfilesUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	var commandErr browserruntime.CommandError
	if errors.As(err, &commandErr) {
		switch commandErr.Code {
		case "BROWSER_PROFILE_SHARING_OFF":
			return controllers.ErrBrowserProfileSharingOff
		case "BROWSER_PROFILE_RECEIVER_UNVERIFIED":
			return controllers.ErrSharedBrowserProfileReceiverUnverified
		case "BROWSER_PROFILE_NOT_FOUND", "BROWSER_PROFILE_NOT_SHARED":
			return controllers.ErrSharedBrowserProfileNotFound
		case "INVALID_CURSOR":
			return controllers.ErrSharedBrowserProfileBadCursor
		case "BROWSER_TARGET_UNAVAILABLE":
			return controllers.ErrSharedBrowserProfilesUnavailable
		}
		s.log.Warn("shared browser profiles: desktop read failed", "code", commandErr.Code)
		return errSharedBrowserProfileDesktopFailed
	}
	s.log.Warn("shared browser profiles: desktop bridge failed")
	return errSharedBrowserProfileDesktopFailed
}

func decodeDesktopValue(value, out any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

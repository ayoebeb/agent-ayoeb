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

	// Bounds on what the desktop may hand back. The receiving machine validates
	// every field again before it writes anything.
	sharedBrowserProfileMaxProfiles       = 32
	sharedBrowserProfileMaxCookiesPerPage = 20_000
	sharedBrowserProfileMaxHistoryPerPage = 5_000
)

var sharedBrowserProfileNextCursor = regexp.MustCompile(`^\d{1,6}:\d{1,6}$`)

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

func (s sharedBrowserProfilesViaDesktop) ListSharedBrowserProfiles(ctx context.Context) (controllers.SharedBrowserProfilesResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, sharedBrowserProfileListTimeout)
	defer cancel()
	result, err := s.broker.Execute(ctx, sharedBrowserProfileChain, sharedBrowserProfileListAction, nil)
	if err != nil {
		return controllers.SharedBrowserProfilesResponse{}, s.desktopError(err)
	}
	var out controllers.SharedBrowserProfilesResponse
	if err := decodeDesktopValue(result.Value, &out); err != nil || len(out.Profiles) > sharedBrowserProfileMaxProfiles {
		s.log.Warn("shared browser profiles: desktop returned a malformed list")
		return controllers.SharedBrowserProfilesResponse{}, errSharedBrowserProfileDesktopFailed
	}
	return out, nil
}

func (s sharedBrowserProfilesViaDesktop) ExportSharedBrowserProfile(ctx context.Context, profileID, cursor string) (controllers.SharedBrowserProfileExportResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, sharedBrowserProfileExportTimeout)
	defer cancel()
	args := map[string]interface{}{"profileId": profileID}
	if cursor != "" {
		args["cursor"] = cursor
	}
	result, err := s.broker.Execute(ctx, sharedBrowserProfileChain, sharedBrowserProfileExportAction, args)
	if err != nil {
		return controllers.SharedBrowserProfileExportResponse{}, s.desktopError(err)
	}
	var out controllers.SharedBrowserProfileExportResponse
	if err := decodeDesktopValue(result.Value, &out); err != nil ||
		out.Profile.ID != profileID ||
		len(out.Cookies) > sharedBrowserProfileMaxCookiesPerPage ||
		len(out.History) > sharedBrowserProfileMaxHistoryPerPage ||
		(out.NextCursor != "" && !sharedBrowserProfileNextCursor.MatchString(out.NextCursor)) {
		// Never log the decoded page: it holds cookie values.
		s.log.Warn("shared browser profiles: desktop returned a malformed export page")
		return controllers.SharedBrowserProfileExportResponse{}, errSharedBrowserProfileDesktopFailed
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

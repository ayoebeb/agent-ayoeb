package controllers

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

const (
	sharedBrowserProfilesPath      = "/api/v1/shared-browser-profiles"
	sharedBrowserProfileExportPath = "/api/v1/shared-browser-profiles/{profileId}/export"
)

// Errors a SharedBrowserProfileSource returns. Their text is fixed: profile
// data never reaches an error message, a log line, or telemetry.
var (
	// ErrBrowserProfileSharingOff means this machine's owner has not turned on
	// "Share my browser profiles with my linked machines".
	ErrBrowserProfileSharingOff = errors.New("browser profile sharing is turned off on this machine")
	// ErrSharedBrowserProfileNotFound covers a missing profile and one that is
	// not shared, such as a profile that was itself copied from another machine.
	ErrSharedBrowserProfileNotFound = errors.New("shared browser profile not found")
	// ErrSharedBrowserProfilesUnavailable means no desktop app is connected to
	// read the profiles from.
	ErrSharedBrowserProfilesUnavailable = errors.New("the Orchestrator desktop app is not running on this machine")
	// ErrSharedBrowserProfileBadCursor rejects a cursor the desktop did not issue.
	ErrSharedBrowserProfileBadCursor = errors.New("shared browser profile cursor is invalid")
)

// SharedBrowserProfileSource reads this machine's shared browser profiles from
// its desktop app, which owns the cookie partitions and history.
type SharedBrowserProfileSource interface {
	ListSharedBrowserProfiles(ctx context.Context) (SharedBrowserProfilesResponse, error)
	ExportSharedBrowserProfile(ctx context.Context, profileID, cursor string) (SharedBrowserProfileExportResponse, error)
}

// SharedBrowserProfilesController serves this machine's browser profiles to
// the machines linked to it. The router mounts it behind remoteLinkOnly, so
// only a bearer-authenticated request on the remote listener reaches it.
type SharedBrowserProfilesController struct {
	Source SharedBrowserProfileSource
}

// Register mounts the shared browser profile routes on the supplied router.
func (c *SharedBrowserProfilesController) Register(r chi.Router) {
	r.Get("/shared-browser-profiles", c.list)
	r.Get("/shared-browser-profiles/{profileId}/export", c.export)
}

var (
	sharedBrowserProfileIDPattern     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
	sharedBrowserProfileCursorPattern = regexp.MustCompile(`^\d{1,6}:\d{1,6}$`)
)

func (c *SharedBrowserProfilesController) list(w http.ResponseWriter, r *http.Request) {
	if c.Source == nil {
		apispec.NotImplemented(w, r, http.MethodGet, sharedBrowserProfilesPath)
		return
	}
	result, err := c.Source.ListSharedBrowserProfiles(r.Context())
	if err != nil {
		writeSharedBrowserProfileError(w, r, err)
		return
	}
	if result.Profiles == nil {
		result.Profiles = []SharedBrowserProfile{}
	}
	w.Header().Set("Cache-Control", "no-store")
	envelope.WriteJSON(w, http.StatusOK, result)
}

func (c *SharedBrowserProfilesController) export(w http.ResponseWriter, r *http.Request) {
	if c.Source == nil {
		apispec.NotImplemented(w, r, http.MethodGet, sharedBrowserProfileExportPath)
		return
	}
	profileID := chi.URLParam(r, "profileId")
	if !sharedBrowserProfileIDPattern.MatchString(profileID) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "INVALID_PROFILE_ID", "profile id is invalid", nil)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !sharedBrowserProfileCursorPattern.MatchString(cursor) {
		writeSharedBrowserProfileError(w, r, ErrSharedBrowserProfileBadCursor)
		return
	}
	result, err := c.Source.ExportSharedBrowserProfile(r.Context(), strings.ToLower(profileID), cursor)
	if err != nil {
		writeSharedBrowserProfileError(w, r, err)
		return
	}
	if result.Cookies == nil {
		result.Cookies = []SharedBrowserCookie{}
	}
	if result.History == nil {
		result.History = []SharedBrowserHistoryEntry{}
	}
	// Cookie values must not linger in any cache between the two machines.
	w.Header().Set("Cache-Control", "no-store")
	envelope.WriteJSON(w, http.StatusOK, result)
}

func writeSharedBrowserProfileError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrBrowserProfileSharingOff):
		envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "BROWSER_PROFILE_SHARING_OFF",
			"Browser profile sharing is turned off on this machine. Turn on sharing in its Browser settings.", nil)
	case errors.Is(err, ErrSharedBrowserProfileNotFound):
		envelope.WriteAPIError(w, r, http.StatusNotFound, "not_found", "SHARED_BROWSER_PROFILE_NOT_FOUND",
			"This browser profile is not shared by this machine.", nil)
	case errors.Is(err, ErrSharedBrowserProfileBadCursor):
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "INVALID_CURSOR", "export cursor is invalid", nil)
	case errors.Is(err, ErrSharedBrowserProfilesUnavailable):
		envelope.WriteAPIError(w, r, http.StatusServiceUnavailable, "unavailable", "DESKTOP_UNAVAILABLE",
			"The Orchestrator desktop app is not running on this machine, so its browser profiles cannot be read.", nil)
	case errors.Is(err, context.DeadlineExceeded):
		envelope.WriteAPIError(w, r, http.StatusGatewayTimeout, "unavailable", "DESKTOP_TIMEOUT",
			"The Orchestrator desktop app on this machine did not answer in time.", nil)
	default:
		// The cause is deliberately not echoed: it came from the desktop bridge.
		envelope.WriteAPIError(w, r, http.StatusBadGateway, "unavailable", "SHARED_BROWSER_PROFILES_FAILED",
			"The Orchestrator desktop app on this machine could not read its browser profiles.", nil)
	}
}

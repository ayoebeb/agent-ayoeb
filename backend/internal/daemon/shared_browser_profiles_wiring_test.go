package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/browserruntime"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
)

const wiringProfileID = "0f8fad5b-d9cb-469f-a165-70867728950e"

type fakeShareBroker struct {
	value   any
	err     error
	session domain.SessionID
	action  string
	args    map[string]interface{}
}

func (f *fakeShareBroker) Execute(_ context.Context, id domain.SessionID, action string, args map[string]interface{}) (browserruntime.Result, error) {
	f.session, f.action, f.args = id, action, args
	return browserruntime.Result{Value: f.value}, f.err
}

func exportPage(cookies int, nextCursor string) map[string]any {
	list := make([]any, cookies)
	for i := range list {
		list[i] = map[string]any{"name": "sid", "value": "dummy-cookie-value", "domain": "example.test", "hostOnly": true, "path": "/", "secure": true, "httpOnly": true, "sameSite": "lax"}
	}
	page := map[string]any{
		"profile": map[string]any{"id": wiringProfileID, "name": "Brave", "cookieCount": cookies, "historyCount": 1, "updatedAt": "2026-11-10T10:00:00Z"},
		"cookies": list,
		"history": []any{map[string]any{"url": "https://example.test/", "lastVisited": "2026-11-10T10:00:00Z", "visitCount": 2}},
	}
	if nextCursor != "" {
		page["nextCursor"] = nextCursor
	}
	return page
}

func TestSharedBrowserProfilesUseInternalDesktopActions(t *testing.T) {
	broker := &fakeShareBroker{value: map[string]any{"profiles": []any{map[string]any{"id": wiringProfileID, "name": "Brave", "sourceBrowser": "Brave", "cookieCount": 3, "historyCount": 4, "updatedAt": "2026-11-10T10:00:00Z"}}}}
	source := newSharedBrowserProfilesViaDesktop(broker, nil)
	list, err := source.ListSharedBrowserProfiles(context.Background())
	if err != nil || len(list.Profiles) != 1 || list.Profiles[0].CookieCount != 3 || list.Profiles[0].SourceBrowser != "Brave" {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if broker.action != "__browser-profile-share-list" || broker.session != sharedBrowserProfileChain {
		t.Fatalf("list sent %q on %q", broker.action, broker.session)
	}

	broker.value = exportPage(2, "2:1")
	page, err := source.ExportSharedBrowserProfile(context.Background(), wiringProfileID, "1:0")
	if err != nil || len(page.Cookies) != 2 || page.NextCursor != "2:1" || page.Cookies[0].Value != "dummy-cookie-value" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if broker.action != "__browser-profile-share-export" || broker.args["profileId"] != wiringProfileID || broker.args["cursor"] != "1:0" {
		t.Fatalf("export sent %q %#v", broker.action, broker.args)
	}
}

func TestSharedBrowserProfilesMapDesktopErrors(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want error
	}{
		{browserruntime.ErrUnavailable, controllers.ErrSharedBrowserProfilesUnavailable},
		{browserruntime.CommandError{Code: "BROWSER_PROFILE_SHARING_OFF", Message: "off"}, controllers.ErrBrowserProfileSharingOff},
		{browserruntime.CommandError{Code: "BROWSER_PROFILE_NOT_SHARED", Message: "copy"}, controllers.ErrSharedBrowserProfileNotFound},
		{browserruntime.CommandError{Code: "BROWSER_PROFILE_NOT_FOUND", Message: "gone"}, controllers.ErrSharedBrowserProfileNotFound},
		{browserruntime.CommandError{Code: "INVALID_CURSOR", Message: "bad"}, controllers.ErrSharedBrowserProfileBadCursor},
		{context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		source := newSharedBrowserProfilesViaDesktop(&fakeShareBroker{err: tc.err}, nil)
		if _, err := source.ListSharedBrowserProfiles(context.Background()); !errors.Is(err, tc.want) {
			t.Fatalf("list %v -> %v, want %v", tc.err, err, tc.want)
		}
		if _, err := source.ExportSharedBrowserProfile(context.Background(), wiringProfileID, ""); !errors.Is(err, tc.want) {
			t.Fatalf("export %v -> %v, want %v", tc.err, err, tc.want)
		}
	}
}

func TestSharedBrowserProfilesRejectMalformedDesktopPages(t *testing.T) {
	other := exportPage(1, "")
	other["profile"] = map[string]any{"id": "7c9e6679-7425-40de-944b-e07fc1f90ae7", "name": "Other", "updatedAt": "2026-11-10T10:00:00Z"}
	for name, value := range map[string]any{
		"other profile": other,
		"too many":      exportPage(sharedBrowserProfileMaxCookiesPerPage+1, ""),
		"bad cursor":    exportPage(1, "../x"),
		"not an object": "dummy-cookie-value",
	} {
		source := newSharedBrowserProfilesViaDesktop(&fakeShareBroker{value: value}, nil)
		if _, err := source.ExportSharedBrowserProfile(context.Background(), wiringProfileID, ""); !errors.Is(err, errSharedBrowserProfileDesktopFailed) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

// Cookie values travel through this adapter; none may reach the daemon log,
// whether the desktop answered, failed, or answered with something malformed.
func TestSharedBrowserProfilesNeverLogCookieValues(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	malformed := exportPage(1, "")
	malformed["profile"] = map[string]any{"id": "wrong"}
	for _, broker := range []*fakeShareBroker{
		{value: exportPage(3, "")},
		{value: malformed},
		{err: browserruntime.CommandError{Code: "BROWSER_COMMAND_FAILED", Message: "could not set sid=dummy-cookie-value"}},
		{err: errors.New("socket wrote dummy-cookie-value")},
	} {
		source := newSharedBrowserProfilesViaDesktop(broker, log)
		_, err := source.ExportSharedBrowserProfile(context.Background(), wiringProfileID, "")
		if err != nil && strings.Contains(err.Error(), "dummy-cookie-value") {
			t.Fatalf("returned error carries a cookie value: %v", err)
		}
	}
	if strings.Contains(logs.String(), "dummy-cookie-value") {
		t.Fatalf("daemon log carries a cookie value:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "BROWSER_COMMAND_FAILED") {
		t.Fatalf("the desktop's stable code should still be logged:\n%s", logs.String())
	}
}

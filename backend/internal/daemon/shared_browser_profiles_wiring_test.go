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

var wiringReceiver = controllers.SharedBrowserProfileReceiver{Ticket: "dGlja2V0LXBheWxvYWQtdGVzdA", Signature: strings.Repeat("S", 86)}

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

func sealedPayload(data string) map[string]any {
	return map[string]any{"version": 1, "key": strings.Repeat("k", 43), "iv": strings.Repeat("i", 16), "data": data}
}

func TestSharedBrowserProfilesForwardTheTicketAndRelayCiphertext(t *testing.T) {
	broker := &fakeShareBroker{value: sealedPayload("Y2lwaGVydGV4dA")}
	source := newSharedBrowserProfilesViaDesktop(broker, nil)
	list, err := source.ListSharedBrowserProfiles(context.Background(), wiringReceiver)
	if err != nil || list.Data != "Y2lwaGVydGV4dA" || list.Version != 1 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	if broker.action != "__browser-profile-share-list" || broker.session != sharedBrowserProfileChain ||
		broker.args["ticket"] != wiringReceiver.Ticket || broker.args["signature"] != wiringReceiver.Signature {
		t.Fatalf("list sent %q on %q with %#v", broker.action, broker.session, broker.args)
	}

	page, err := source.ExportSharedBrowserProfile(context.Background(), wiringReceiver, wiringProfileID, "1:0")
	if err != nil || page.Data != "Y2lwaGVydGV4dA" {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if broker.action != "__browser-profile-share-export" || broker.args["profileId"] != wiringProfileID ||
		broker.args["cursor"] != "1:0" || broker.args["ticket"] != wiringReceiver.Ticket {
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
		{browserruntime.CommandError{Code: "BROWSER_PROFILE_RECEIVER_UNVERIFIED", Message: "who"}, controllers.ErrSharedBrowserProfileReceiverUnverified},
		{browserruntime.CommandError{Code: "BROWSER_PROFILE_NOT_SHARED", Message: "copy"}, controllers.ErrSharedBrowserProfileNotFound},
		{browserruntime.CommandError{Code: "BROWSER_PROFILE_NOT_FOUND", Message: "gone"}, controllers.ErrSharedBrowserProfileNotFound},
		{browserruntime.CommandError{Code: "INVALID_CURSOR", Message: "bad"}, controllers.ErrSharedBrowserProfileBadCursor},
		{context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		source := newSharedBrowserProfilesViaDesktop(&fakeShareBroker{err: tc.err}, nil)
		if _, err := source.ListSharedBrowserProfiles(context.Background(), wiringReceiver); !errors.Is(err, tc.want) {
			t.Fatalf("list %v -> %v, want %v", tc.err, err, tc.want)
		}
		if _, err := source.ExportSharedBrowserProfile(context.Background(), wiringReceiver, wiringProfileID, ""); !errors.Is(err, tc.want) {
			t.Fatalf("export %v -> %v, want %v", tc.err, err, tc.want)
		}
	}
}

// The daemon relays only a sealed payload. A desktop that answers with
// anything else, such as a plaintext page from an older build, is refused, so
// cookie values never cross the remote connection unsealed.
func TestSharedBrowserProfilesRefuseAnythingButASealedPayload(t *testing.T) {
	plaintext := map[string]any{
		"profile": map[string]any{"id": wiringProfileID, "name": "Brave"},
		"cookies": []any{map[string]any{"name": "sid", "value": "dummy-cookie-value"}},
	}
	wrongVersion := sealedPayload("YQ")
	wrongVersion["version"] = 2
	shortKey := sealedPayload("YQ")
	shortKey["key"] = "abc"
	for name, value := range map[string]any{
		"plaintext page": plaintext,
		"wrong version":  wrongVersion,
		"short key":      shortKey,
		"not base64url":  sealedPayload("dummy-cookie-value=="),
		"empty data":     sealedPayload(""),
		"too large":      sealedPayload(strings.Repeat("a", sharedBrowserProfileMaxSealedBytes+1)),
		"not an object":  "dummy-cookie-value",
	} {
		source := newSharedBrowserProfilesViaDesktop(&fakeShareBroker{value: value}, nil)
		if _, err := source.ExportSharedBrowserProfile(context.Background(), wiringReceiver, wiringProfileID, ""); !errors.Is(err, errSharedBrowserProfileDesktopFailed) {
			t.Fatalf("%s: err = %v", name, err)
		}
		if _, err := source.ListSharedBrowserProfiles(context.Background(), wiringReceiver); !errors.Is(err, errSharedBrowserProfileDesktopFailed) {
			t.Fatalf("%s list: err = %v", name, err)
		}
	}
}

// Nothing the desktop sends back, or fails with, may reach the daemon log.
func TestSharedBrowserProfilesNeverLogCookieValues(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	for _, broker := range []*fakeShareBroker{
		{value: sealedPayload("Y2lwaGVydGV4dA")},
		{value: map[string]any{"cookies": []any{map[string]any{"value": "dummy-cookie-value"}}}},
		{err: browserruntime.CommandError{Code: "BROWSER_COMMAND_FAILED", Message: "could not set sid=dummy-cookie-value"}},
		{err: errors.New("socket wrote dummy-cookie-value")},
	} {
		source := newSharedBrowserProfilesViaDesktop(broker, log)
		_, err := source.ExportSharedBrowserProfile(context.Background(), wiringReceiver, wiringProfileID, "")
		if err != nil && strings.Contains(err.Error(), "dummy-cookie-value") {
			t.Fatalf("returned error carries a cookie value: %v", err)
		}
	}
	if strings.Contains(logs.String(), "dummy-cookie-value") || strings.Contains(logs.String(), wiringReceiver.Ticket) {
		t.Fatalf("daemon log carries a cookie value or the ticket:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "BROWSER_COMMAND_FAILED") {
		t.Fatalf("the desktop's stable code should still be logged:\n%s", logs.String())
	}
}

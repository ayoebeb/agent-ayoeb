package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

const (
	sharedProfileID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	sharedTicket    = "eyJ2IjoxLCJwdXJwb3NlIjoiYW8tYnJvd3Nlci1wcm9maWxlLXNoYXJlIn0"
)

var sharedSignature = strings.Repeat("S", 86)

type stubSharedBrowserProfiles struct {
	err        error
	calls      int
	receiver   SharedBrowserProfileReceiver
	lastID     string
	lastCursor string
}

func (s *stubSharedBrowserProfiles) payload() SealedBrowserProfilePayload {
	return SealedBrowserProfilePayload{Version: 1, Key: strings.Repeat("k", 43), IV: strings.Repeat("i", 16), Data: "c2VhbGVk"}
}

func (s *stubSharedBrowserProfiles) ListSharedBrowserProfiles(_ context.Context, receiver SharedBrowserProfileReceiver) (SealedBrowserProfilePayload, error) {
	s.calls++
	s.receiver = receiver
	if s.err != nil {
		return SealedBrowserProfilePayload{}, s.err
	}
	return s.payload(), nil
}

func (s *stubSharedBrowserProfiles) ExportSharedBrowserProfile(_ context.Context, receiver SharedBrowserProfileReceiver, profileID, cursor string) (SealedBrowserProfilePayload, error) {
	s.calls++
	s.receiver, s.lastID, s.lastCursor = receiver, profileID, cursor
	if s.err != nil {
		return SealedBrowserProfilePayload{}, s.err
	}
	return s.payload(), nil
}

func serveSharedBrowserProfiles(t *testing.T, source SharedBrowserProfileSource, path string, headers map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	router := chi.NewRouter()
	router.Route("/api/v1", (&SharedBrowserProfilesController{Source: source}).Register)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	router.ServeHTTP(recorder, req)
	body := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder, body
}

var ticketHeaders = map[string]string{"X-AO-Receiver-Ticket": sharedTicket, "X-AO-Receiver-Signature": sharedSignature}

func TestSharedBrowserProfilesRelayTheSealedPayload(t *testing.T) {
	source := &stubSharedBrowserProfiles{}
	recorder, body := serveSharedBrowserProfiles(t, source, "/api/v1/shared-browser-profiles", ticketHeaders)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	if body["version"] != float64(1) || body["data"] != "c2VhbGVk" || len(body) != 4 {
		t.Fatalf("body = %#v, want the sealed payload alone", body)
	}
	if source.receiver.Ticket != sharedTicket || source.receiver.Signature != sharedSignature {
		t.Fatalf("receiver = %#v", source.receiver)
	}
}

func TestSharedBrowserProfileExportPassesTicketCursorAndNormalizedID(t *testing.T) {
	source := &stubSharedBrowserProfiles{}
	const cursor = "0123456789abcdef0123456789abcdef:40:2"
	recorder, body := serveSharedBrowserProfiles(t, source, "/api/v1/shared-browser-profiles/"+strings.ToUpper(sharedProfileID)+"/export?cursor="+cursor, ticketHeaders)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if source.lastID != sharedProfileID || source.lastCursor != cursor || source.receiver.Ticket != sharedTicket {
		t.Fatalf("source got id=%q cursor=%q receiver=%#v", source.lastID, source.lastCursor, source.receiver)
	}
	if body["data"] != "c2VhbGVk" || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("body = %#v cache = %q", body, recorder.Header().Get("Cache-Control"))
	}
}

func TestSharedBrowserProfilesRequireAWellFormedReceiverTicket(t *testing.T) {
	for name, headers := range map[string]map[string]string{
		"none":              nil,
		"no signature":      {"X-AO-Receiver-Ticket": sharedTicket},
		"short signature":   {"X-AO-Receiver-Ticket": sharedTicket, "X-AO-Receiver-Signature": "abc"},
		"ticket with slash": {"X-AO-Receiver-Ticket": sharedTicket + "/..", "X-AO-Receiver-Signature": sharedSignature},
		"huge ticket":       {"X-AO-Receiver-Ticket": strings.Repeat("a", 2049), "X-AO-Receiver-Signature": sharedSignature},
	} {
		for _, path := range []string{"/api/v1/shared-browser-profiles", "/api/v1/shared-browser-profiles/" + sharedProfileID + "/export"} {
			source := &stubSharedBrowserProfiles{}
			recorder, body := serveSharedBrowserProfiles(t, source, path, headers)
			if recorder.Code != http.StatusBadRequest || body["code"] != "RECEIVER_TICKET_REQUIRED" || source.calls != 0 {
				t.Fatalf("%s %s: status=%d body=%#v calls=%d", name, path, recorder.Code, body, source.calls)
			}
		}
	}
}

func TestSharedBrowserProfileExportValidatesInput(t *testing.T) {
	for _, tc := range []struct {
		path, code string
	}{
		{"/api/v1/shared-browser-profiles/not-a-uuid/export", "INVALID_PROFILE_ID"},
		{"/api/v1/shared-browser-profiles/" + sharedProfileID + "/export?cursor=../../etc", "INVALID_CURSOR"},
		{"/api/v1/shared-browser-profiles/" + sharedProfileID + "/export?cursor=0123456789abcdef0123456789abcdef:1234567:0", "INVALID_CURSOR"},
		{"/api/v1/shared-browser-profiles/" + sharedProfileID + "/export?cursor=40:2", "INVALID_CURSOR"},
	} {
		source := &stubSharedBrowserProfiles{}
		recorder, body := serveSharedBrowserProfiles(t, source, tc.path, ticketHeaders)
		if recorder.Code != http.StatusBadRequest || body["code"] != tc.code || source.calls != 0 {
			t.Fatalf("%s: status=%d body=%#v calls=%d", tc.path, recorder.Code, body, source.calls)
		}
	}
}

func TestSharedBrowserProfileErrorsAreFixedEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ErrBrowserProfileSharingOff, http.StatusForbidden, "BROWSER_PROFILE_SHARING_OFF"},
		{ErrSharedBrowserProfileReceiverUnverified, http.StatusForbidden, "RECEIVER_NOT_VERIFIED"},
		{ErrSharedBrowserProfileNotFound, http.StatusNotFound, "SHARED_BROWSER_PROFILE_NOT_FOUND"},
		{ErrSharedBrowserProfilesUnavailable, http.StatusServiceUnavailable, "DESKTOP_UNAVAILABLE"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "DESKTOP_TIMEOUT"},
		{errors.New("cookie sid=dummy-cookie-value failed"), http.StatusBadGateway, "SHARED_BROWSER_PROFILES_FAILED"},
	} {
		for _, path := range []string{"/api/v1/shared-browser-profiles", "/api/v1/shared-browser-profiles/" + sharedProfileID + "/export"} {
			recorder, body := serveSharedBrowserProfiles(t, &stubSharedBrowserProfiles{err: tc.err}, path, ticketHeaders)
			if recorder.Code != tc.status || body["code"] != tc.code {
				t.Fatalf("%v %s: status=%d body=%#v", tc.err, path, recorder.Code, body)
			}
			if strings.Contains(recorder.Body.String(), "dummy-cookie-value") {
				t.Fatalf("error envelope echoed the cause: %s", recorder.Body.String())
			}
		}
	}
}

func TestSharedBrowserProfilesWithoutSourceAnswerNotImplemented(t *testing.T) {
	recorder, _ := serveSharedBrowserProfiles(t, nil, "/api/v1/shared-browser-profiles", ticketHeaders)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d", recorder.Code)
	}
}

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

const sharedProfileID = "0f8fad5b-d9cb-469f-a165-70867728950e"

type stubSharedBrowserProfiles struct {
	err        error
	lastID     string
	lastCursor string
}

func (s *stubSharedBrowserProfiles) ListSharedBrowserProfiles(context.Context) (SharedBrowserProfilesResponse, error) {
	if s.err != nil {
		return SharedBrowserProfilesResponse{}, s.err
	}
	return SharedBrowserProfilesResponse{}, nil
}

func (s *stubSharedBrowserProfiles) ExportSharedBrowserProfile(_ context.Context, profileID, cursor string) (SharedBrowserProfileExportResponse, error) {
	s.lastID, s.lastCursor = profileID, cursor
	if s.err != nil {
		return SharedBrowserProfileExportResponse{}, s.err
	}
	return SharedBrowserProfileExportResponse{Profile: SharedBrowserProfile{ID: profileID, Name: "Brave"}, NextCursor: "120:0"}, nil
}

func serveSharedBrowserProfiles(t *testing.T, source SharedBrowserProfileSource, path string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	router := chi.NewRouter()
	router.Route("/api/v1", (&SharedBrowserProfilesController{Source: source}).Register)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	body := map[string]any{}
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	return recorder, body
}

func TestSharedBrowserProfilesListAnswersEmptyArray(t *testing.T) {
	recorder, body := serveSharedBrowserProfiles(t, &stubSharedBrowserProfiles{}, "/api/v1/shared-browser-profiles")
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	if profiles, ok := body["profiles"].([]any); !ok || len(profiles) != 0 {
		t.Fatalf("profiles = %#v, want []", body["profiles"])
	}
}

func TestSharedBrowserProfileExportPassesCursorAndNormalizesID(t *testing.T) {
	source := &stubSharedBrowserProfiles{}
	recorder, body := serveSharedBrowserProfiles(t, source, "/api/v1/shared-browser-profiles/"+strings.ToUpper(sharedProfileID)+"/export?cursor=40:2")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if source.lastID != sharedProfileID || source.lastCursor != "40:2" {
		t.Fatalf("source got id=%q cursor=%q", source.lastID, source.lastCursor)
	}
	if body["nextCursor"] != "120:0" || body["cookies"] == nil || body["history"] == nil {
		t.Fatalf("body = %#v", body)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("export must not be cacheable")
	}
}

func TestSharedBrowserProfileExportValidatesInput(t *testing.T) {
	for _, tc := range []struct {
		path, code string
	}{
		{"/api/v1/shared-browser-profiles/not-a-uuid/export", "INVALID_PROFILE_ID"},
		{"/api/v1/shared-browser-profiles/" + sharedProfileID + "/export?cursor=../../etc", "INVALID_CURSOR"},
		{"/api/v1/shared-browser-profiles/" + sharedProfileID + "/export?cursor=1234567:0", "INVALID_CURSOR"},
	} {
		source := &stubSharedBrowserProfiles{}
		recorder, body := serveSharedBrowserProfiles(t, source, tc.path)
		if recorder.Code != http.StatusBadRequest || body["code"] != tc.code || source.lastID != "" {
			t.Fatalf("%s: status=%d body=%#v source=%q", tc.path, recorder.Code, body, source.lastID)
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
		{ErrSharedBrowserProfileNotFound, http.StatusNotFound, "SHARED_BROWSER_PROFILE_NOT_FOUND"},
		{ErrSharedBrowserProfilesUnavailable, http.StatusServiceUnavailable, "DESKTOP_UNAVAILABLE"},
		{context.DeadlineExceeded, http.StatusGatewayTimeout, "DESKTOP_TIMEOUT"},
		{errors.New("cookie sid=dummy-cookie-value failed"), http.StatusBadGateway, "SHARED_BROWSER_PROFILES_FAILED"},
	} {
		for _, path := range []string{"/api/v1/shared-browser-profiles", "/api/v1/shared-browser-profiles/" + sharedProfileID + "/export"} {
			recorder, body := serveSharedBrowserProfiles(t, &stubSharedBrowserProfiles{err: tc.err}, path)
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
	recorder, _ := serveSharedBrowserProfiles(t, nil, "/api/v1/shared-browser-profiles")
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d", recorder.Code)
	}
}

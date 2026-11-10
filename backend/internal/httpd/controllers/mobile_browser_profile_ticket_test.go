package controllers

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

const ticketRouteKey = "q83vEjRWeJCrze8SNFZ4kKvN7xI0VniQq83vEjRWeJA"

func ticketBridge(t *testing.T) *BridgeService {
	t.Helper()
	return &BridgeService{HostID: "h_receiver", ConfigPath: mobilebridge.Path(t.TempDir())}
}

func TestIssueBrowserProfileTicketSignsForThisMachine(t *testing.T) {
	bridge := ticketBridge(t)
	controller := &MobileController{Bridge: bridge}
	recorder := httptest.NewRecorder()
	controller.IssueBrowserProfileTicket(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/mobile/browser-profile-ticket",
		strings.NewReader(`{"sourceHostId":"h_source","key":"`+ticketRouteKey+`"}`)))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body BrowserProfileTicketResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(body.Ticket)
	var payload mobilebridge.BrowserProfileTicketPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.ReceiverHostID != "h_receiver" || payload.SourceHostID != "h_source" || payload.Key != ticketRouteKey {
		t.Fatalf("payload = %#v, %v", payload, err)
	}
	key, err := bridge.RemoteHostKey()
	if err != nil || key.HostID != "h_receiver" {
		t.Fatalf("host key = %#v, %v", key, err)
	}
	public, _ := base64.RawURLEncoding.DecodeString(key.HostKey)
	sig, _ := base64.RawURLEncoding.DecodeString(body.Signature)
	if !ed25519.Verify(public, []byte(mobilebridge.BrowserProfileTicketSignaturePrefix+body.Ticket), sig) {
		t.Fatal("signature does not verify against the served host key")
	}
}

func TestIssueBrowserProfileTicketRefusesBadRequests(t *testing.T) {
	controller := &MobileController{Bridge: ticketBridge(t)}
	for name, body := range map[string]string{
		"not json":       "{",
		"self as source": `{"sourceHostId":"h_receiver","key":"` + ticketRouteKey + `"}`,
		"bad key":        `{"sourceHostId":"h_source","key":"short"}`,
		"oversized":      `{"sourceHostId":"` + strings.Repeat("h", 5000) + `","key":"` + ticketRouteKey + `"}`,
	} {
		recorder := httptest.NewRecorder()
		controller.IssueBrowserProfileTicket(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/mobile/browser-profile-ticket", strings.NewReader(body)))
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "INVALID_BROWSER_PROFILE_TICKET") {
			t.Fatalf("%s: status=%d body=%s", name, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRemoteHostKeyRouteServesThePublicKeyOnly(t *testing.T) {
	bridge := ticketBridge(t)
	recorder := httptest.NewRecorder()
	(&MobileController{Bridge: bridge}).HostKey(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/remote-host/host-key", nil))
	var body map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &body)
	key, _ := body["hostKey"].(string)
	if recorder.Code != http.StatusOK || body["hostId"] != "h_receiver" || len(key) != 43 || len(body) != 2 {
		t.Fatalf("status=%d body=%#v", recorder.Code, body)
	}
}

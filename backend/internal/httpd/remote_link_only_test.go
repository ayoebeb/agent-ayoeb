package httpd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

const (
	linkTestPairing = "pairing-secret"
	linkTestAccount = "account-token-0123456789abcdef"
	linkTestTicket  = "eyJ2IjoxLCJwdXJwb3NlIjoiYW8tYnJvd3Nlci1wcm9maWxlLXNoYXJlIn0"
)

var linkTestSignature = strings.Repeat("A", 86)

type fakeSharedBrowserProfiles struct {
	calls    int
	receiver controllers.SharedBrowserProfileReceiver
}

func (f *fakeSharedBrowserProfiles) sealed() controllers.SealedBrowserProfilePayload {
	return controllers.SealedBrowserProfilePayload{Version: 1, Key: strings.Repeat("k", 43), IV: strings.Repeat("i", 16), Data: "c2VhbGVk"}
}

func (f *fakeSharedBrowserProfiles) ListSharedBrowserProfiles(_ context.Context, receiver controllers.SharedBrowserProfileReceiver) (controllers.SealedBrowserProfilePayload, error) {
	f.calls++
	f.receiver = receiver
	return f.sealed(), nil
}

func (f *fakeSharedBrowserProfiles) ExportSharedBrowserProfile(_ context.Context, receiver controllers.SharedBrowserProfileReceiver, _, _ string) (controllers.SealedBrowserProfilePayload, error) {
	f.calls++
	f.receiver = receiver
	return f.sealed(), nil
}

func newLinkTestLAN(t *testing.T) (*fakeSharedBrowserProfiles, http.Handler, *LANManager) {
	t.Helper()
	source := &fakeSharedBrowserProfiles{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := NewRouterWithControl(config.Config{}, log, nil, APIDeps{SharedBrowserProfiles: source}, ControlDeps{})
	lan := NewMobileLAN(router, "h_A", 0, log, nil)
	lan.SetPasswordHash(mobilebridge.HashPassword(linkTestPairing))
	lan.SetAccountTokenHash(mobilebridge.HashPassword(linkTestAccount))
	return source, router, lan
}

func responseCode(body []byte) string {
	var decoded struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &decoded)
	return decoded.Code
}

func withTicket(headers map[string]string) map[string]string {
	out := map[string]string{"X-AO-Receiver-Ticket": linkTestTicket, "X-AO-Receiver-Signature": linkTestSignature}
	for key, value := range headers {
		out[key] = value
	}
	return out
}

// The shared-profile routes return a machine's sign-in cookies, so the
// unauthenticated loopback listener must never serve them, and the remote
// listener only to the account credential, never the pairing password.
func TestSharedBrowserProfilesServedOnlyToTheAccountCredential(t *testing.T) {
	const profilePath = "/api/v1/shared-browser-profiles/0f8fad5b-d9cb-469f-a165-70867728950e/export"
	source, router, lan := newLinkTestLAN(t)

	serve := func(handler http.Handler, path, remoteAddr string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if remoteAddr != "" {
			req.RemoteAddr = remoteAddr
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		return recorder
	}

	for _, path := range []string{"/api/v1/shared-browser-profiles", profilePath} {
		// Loopback, even with credential-looking headers: no linked machine
		// reaches the daemon this way, only local processes.
		for _, headers := range []map[string]string{nil, withTicket(map[string]string{"Authorization": "Bearer " + linkTestAccount, "X-Forwarded-For": "192.0.2.7"})} {
			got := serve(router, path, "", headers)
			if got.Code != http.StatusForbidden || responseCode(got.Body.Bytes()) != "REMOTE_LINK_REQUIRED" {
				t.Fatalf("loopback %s %v: status=%d body=%s", path, headers, got.Code, got.Body.String())
			}
		}
		for _, headers := range []map[string]string{nil, {"Authorization": "Bearer wrong-secret"}} {
			got := serve(lan.handler, path, "", headers)
			if got.Code != http.StatusUnauthorized || responseCode(got.Body.Bytes()) != "BAD_PASSWORD" {
				t.Fatalf("unauthenticated LAN %s: status=%d body=%s", path, got.Code, got.Body.String())
			}
		}
		// The pairing password opens the rest of the remote API, but never
		// these routes: from the network or from a local process that
		// connects to the remote listener over loopback.
		for _, remoteAddr := range []string{"192.0.2.10:50000", "127.0.0.1:50001", "[::1]:50002"} {
			got := serve(lan.handler, path, remoteAddr, withTicket(map[string]string{"Authorization": "Bearer " + linkTestPairing, "X-AO-Expected-Host-ID": "h_A"}))
			if got.Code != http.StatusForbidden || responseCode(got.Body.Bytes()) != "REMOTE_LINK_REQUIRED" {
				t.Fatalf("pairing password from %s %s: status=%d body=%s", remoteAddr, path, got.Code, got.Body.String())
			}
		}
		got := serve(lan.handler, path, "", withTicket(map[string]string{"Authorization": "Bearer " + linkTestAccount, "X-AO-Expected-Host-ID": "h_other"}))
		if got.Code != http.StatusMisdirectedRequest {
			t.Fatalf("other host LAN %s: status=%d", path, got.Code)
		}
		// The account credential alone is not enough: a local process may
		// read it from disk. Without a receiver ticket nothing is read.
		got = serve(lan.handler, path, "127.0.0.1:50003", map[string]string{"Authorization": "Bearer " + linkTestAccount, "X-AO-Expected-Host-ID": "h_A"})
		if got.Code != http.StatusBadRequest || responseCode(got.Body.Bytes()) != "RECEIVER_TICKET_REQUIRED" {
			t.Fatalf("account credential without a ticket %s: status=%d body=%s", path, got.Code, got.Body.String())
		}
	}
	if source.calls != 0 {
		t.Fatalf("source was read %d times by refused requests", source.calls)
	}

	for _, path := range []string{"/api/v1/shared-browser-profiles", profilePath} {
		got := serve(lan.handler, path, "", withTicket(map[string]string{"Authorization": "Bearer " + linkTestAccount, "X-AO-Expected-Host-ID": "h_A"}))
		if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"data":"c2VhbGVk"`) {
			t.Fatalf("account credential with a ticket %s: status=%d body=%s", path, got.Code, got.Body.String())
		}
		if cache := got.Header().Get("Cache-Control"); cache != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", path, cache)
		}
	}
	if source.calls != 2 || source.receiver.Ticket != linkTestTicket || source.receiver.Signature != linkTestSignature {
		t.Fatalf("source calls = %d receiver = %#v", source.calls, source.receiver)
	}
}

// Behind cloudflared every request arrives inside a tunnel opened over
// loopback, which a local process can open as well. The requests inside are
// authorized one by one, so the pairing password is refused there too.
func TestSharedBrowserProfilesRefuseThePairingPasswordInsideTheTunnel(t *testing.T) {
	source, _, lan := newLinkTestLAN(t)
	port, err := lan.StartLoopback(0)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer lan.Stop(context.Background())
	ws, _, err := websocket.Dial(context.Background(), fmt.Sprintf("ws://127.0.0.1:%d%s", port, lanTunnelPath), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + linkTestPairing}},
	})
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer conn.Close()
	protocols := new(http.Protocols)
	protocols.SetUnencryptedHTTP2(true)
	client := &http.Client{Transport: &http.Transport{
		Protocols:   protocols,
		DialContext: func(context.Context, string, string) (net.Conn, error) { return conn, nil },
	}}
	get := func(bearer string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, "http://tunnel/api/v1/shared-browser-profiles", nil)
		for key, value := range withTicket(map[string]string{"Authorization": "Bearer " + bearer, "X-AO-Expected-Host-ID": "h_A"}) {
			req.Header.Set(key, value)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request through the tunnel: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, responseCode(body)
	}
	if status, code := get(linkTestPairing); status != http.StatusForbidden || code != "REMOTE_LINK_REQUIRED" {
		t.Fatalf("pairing password inside the tunnel: %d %s", status, code)
	}
	if source.calls != 0 {
		t.Fatalf("source read %d times for the pairing password", source.calls)
	}
	if status, _ := get(linkTestAccount); status != http.StatusOK || source.calls != 1 {
		t.Fatalf("account credential inside the tunnel: %d, source calls %d", status, source.calls)
	}
}

// A receiver ticket is signed with this machine's host key, so only this
// machine's own desktop may ask for one: the remote listener 404s the route
// whatever credential the request carries.
func TestBrowserProfileTicketRouteIsNeverServedRemotely(t *testing.T) {
	_, _, lan := newLinkTestLAN(t)
	for _, bearer := range []string{linkTestAccount, linkTestPairing} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mobile/browser-profile-ticket", strings.NewReader(`{"sourceHostId":"h_B","key":"x"}`))
		req.Header.Set("Authorization", "Bearer "+bearer)
		recorder := httptest.NewRecorder()
		lan.handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("remote ticket request with %s: status=%d body=%s", bearer, recorder.Code, recorder.Body.String())
		}
	}
}

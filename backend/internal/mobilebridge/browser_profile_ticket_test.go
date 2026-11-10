package mobilebridge

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const ticketTestKey = "q83vEjRWeJCrze8SNFZ4kKvN7xI0VniQq83vEjRWeJA"

func TestBrowserProfileTicketsAreSignedWithTheHostKey(t *testing.T) {
	statePath := Path(t.TempDir())
	at := time.Unix(1_794_325_659, 0)
	ticket, signature, err := IssueBrowserProfileTicket(statePath, "h_receiver", "h_source", ticketTestKey, at)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(ticket)
	if err != nil {
		t.Fatalf("ticket is not base64url: %v", err)
	}
	var payload BrowserProfileTicketPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	want := BrowserProfileTicketPayload{V: 1, Purpose: BrowserProfileTicketPurpose, SourceHostID: "h_source", ReceiverHostID: "h_receiver", Key: ticketTestKey, IssuedAt: at.Unix()}
	if payload != want {
		t.Fatalf("payload = %#v, want %#v", payload, want)
	}
	public, err := HostPublicKey(statePath)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _ := base64.RawURLEncoding.DecodeString(public)
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || len(signature) != 86 {
		t.Fatalf("signature %q: %v", signature, err)
	}
	if !ed25519.Verify(publicKey, []byte(BrowserProfileTicketSignaturePrefix+ticket), sig) {
		t.Fatal("the signature does not verify against the host key")
	}
	// Domain separation: the same bytes without the prefix, as an AO Cloud
	// request signature would sign them, do not verify.
	if ed25519.Verify(publicKey, []byte(ticket), sig) {
		t.Fatal("the ticket signature verifies without its prefix")
	}
	if !strings.HasPrefix(BrowserProfileTicketSignaturePrefix, "ao-browser-profile-") || strings.HasPrefix(BrowserProfileTicketSignaturePrefix, "ao-host-v1") {
		t.Fatalf("prefix %q is not separated from AO Cloud's", BrowserProfileTicketSignaturePrefix)
	}
}

func TestBrowserProfileTicketsRefuseBadRequests(t *testing.T) {
	statePath := Path(t.TempDir())
	for name, args := range map[string][3]string{
		"self as source":   {"h_same", "h_same", ticketTestKey},
		"bad source":       {"h_receiver", "h_source/../x", ticketTestKey},
		"empty source":     {"h_receiver", "", ticketTestKey},
		"short key":        {"h_receiver", "h_source", "abc"},
		"padded key":       {"h_receiver", "h_source", ticketTestKey + "="},
		"not base64url":    {"h_receiver", "h_source", strings.Repeat("+", 43)},
		"no receiver host": {"", "h_source", ticketTestKey},
	} {
		if _, _, err := IssueBrowserProfileTicket(statePath, args[0], args[1], args[2], time.Now()); !errors.Is(err, ErrInvalidBrowserProfileTicket) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}

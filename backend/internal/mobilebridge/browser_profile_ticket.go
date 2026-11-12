package mobilebridge

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"time"
)

// A browser profile ticket is how a machine that copies browser profiles
// proves to the machine that shares them which machine it is, and which key
// the answer must be sealed to. The receiving desktop makes a one-time X25519
// key and asks its own daemon to sign a ticket naming it; the sharing desktop
// checks the signature against the host key it reads from the receiver itself,
// over the https address AO Cloud lists for it, and seals every page to that
// key. A process that holds only a connection credential gets nothing it can
// read.

// BrowserProfileTicketSignaturePrefix starts every signed ticket message. It
// differs from AO Cloud's "ao-host-v1" signatures, so a ticket signature can
// never pass as a signed request to AO Cloud, nor the other way round.
const BrowserProfileTicketSignaturePrefix = "ao-browser-profile-receiver-v1\n"

// BrowserProfileTicketPurpose names what a ticket is for inside its payload.
const BrowserProfileTicketPurpose = "ao-browser-profile-share"

// ErrInvalidBrowserProfileTicket rejects a ticket request this machine will
// not sign: a malformed host id or key, or a "source" that is this machine.
var ErrInvalidBrowserProfileTicket = errors.New("browser profile ticket request is invalid")

var browserProfileTicketHostID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// BrowserProfileTicketPayload is the signed ticket's JSON, base64url-encoded
// on the wire exactly as signed.
type BrowserProfileTicketPayload struct {
	V              int    `json:"v"`
	Purpose        string `json:"purpose"`
	SourceHostID   string `json:"sourceHostId"`
	ReceiverHostID string `json:"receiverHostId"`
	// Key is the receiver's one-time X25519 public key, unpadded base64url.
	Key      string `json:"key"`
	IssuedAt int64  `json:"issuedAt"`
}

// IssueBrowserProfileTicket signs a ticket for receiverHostID, this machine,
// to read sourceHostID's shared browser profiles sealed to key. It returns
// the ticket and the host key's signature over the prefix and the ticket,
// both unpadded base64url.
func IssueBrowserProfileTicket(statePath, receiverHostID, sourceHostID, key string, now time.Time) (string, string, error) {
	if !browserProfileTicketHostID.MatchString(receiverHostID) || !browserProfileTicketHostID.MatchString(sourceHostID) ||
		receiverHostID == sourceHostID {
		return "", "", ErrInvalidBrowserProfileTicket
	}
	raw, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != key {
		return "", "", ErrInvalidBrowserProfileTicket
	}
	hostKey, err := HostKey(statePath)
	if err != nil {
		return "", "", err
	}
	payload, err := json.Marshal(BrowserProfileTicketPayload{
		V:              1,
		Purpose:        BrowserProfileTicketPurpose,
		SourceHostID:   sourceHostID,
		ReceiverHostID: receiverHostID,
		Key:            key,
		IssuedAt:       now.Unix(),
	})
	if err != nil {
		return "", "", err
	}
	ticket := base64.RawURLEncoding.EncodeToString(payload)
	signature := ed25519.Sign(hostKey, []byte(BrowserProfileTicketSignaturePrefix+ticket))
	return ticket, base64.RawURLEncoding.EncodeToString(signature), nil
}

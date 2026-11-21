package httpd

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// CommandRunKeyHeader carries the desktop's command-run key on a local Run.
const CommandRunKeyHeader = "X-AO-Command-Run-Key"

// commandRunKeyLabel scopes the key derived from the browser runtime token to
// command runs. The desktop derives the same key (main/command-run-key.ts);
// change both together.
const commandRunKeyLabel = "ao-command-run-v1"

// DeriveCommandRunKey is the key a local desktop presents to run a command.
// It is derived from the token the desktop hands an app-owned daemon on stdin
// (never on a command line, in the environment, or over HTTP), so a process
// that merely reaches the loopback port cannot present it. A daemon without
// that token (started by `ao start`) mints its own, so no client can present
// the key and local Run is refused.
func DeriveCommandRunKey(runtimeToken string) string {
	if runtimeToken == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(runtimeToken))
	mac.Write([]byte(commandRunKeyLabel))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// commandRunGate admits only a person's client to the command-run routes:
//
//   - a paired remote client (a phone, or another desktop's remote-host proxy)
//     that authenticated on the LAN listener with the pairing credential; or
//   - the local desktop renderer, which must present both an exact renderer
//     Origin and the command-run key.
//
// Everything else is refused: the ao CLI and agents (no key; an agent's CLI
// also names its session in caller.Header), automations (they run inside the
// daemon and never reach HTTP), previews and other local web pages (wrong
// Origin, no key). A future elevated "Run as administrator" must additionally
// require the local branch, never the remote one.
func commandRunGate(allowedOrigins []string, key string) func(http.Handler) http.Handler {
	return userClientGate(allowedOrigins, key, "COMMAND_RUN_USER_CLIENT_REQUIRED",
		"Commands can only be run from the Orchestrator app")
}

// liveServerGate admits only a person's client to the Live server routes, by
// the same rule as commandRunGate: they list every server in a project and
// stop or restart them, so an agent, the ao CLI, a preview page or any local
// process that merely reaches the loopback port is refused. A paired remote
// client is admitted on purpose: another desktop reads a remote computer's
// Live list through that connection.
func liveServerGate(allowedOrigins []string, key string) func(http.Handler) http.Handler {
	return userClientGate(allowedOrigins, key, "LIVE_SERVER_USER_CLIENT_REQUIRED",
		"Running servers can only be managed from the Orchestrator app")
}

func userClientGate(allowedOrigins []string, key, code, message string) func(http.Handler) http.Handler {
	allowed := exactAllowedOrigins(allowedOrigins)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !commandRunUserClient(r, allowed, key) {
				envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", code, message, nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func commandRunUserClient(r *http.Request, allowed map[string]struct{}, key string) bool {
	if caller.From(r.Context()) != "" {
		return false
	}
	if authenticatedLANBearer(r) {
		return true
	}
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		return false
	}
	if _, ok := allowed[origins[0]]; !ok {
		return false
	}
	presented := r.Header.Get(CommandRunKeyHeader)
	return key != "" && presented != "" && hmac.Equal([]byte(presented), []byte(key))
}

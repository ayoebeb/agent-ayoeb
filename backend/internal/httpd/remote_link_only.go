package httpd

import (
	"net/http"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/requestscope"
)

// remoteLinkOnly serves a route only to a linked machine: a request that
// arrived on the authenticated remote listener (LAN or its tunnel) and
// presented the connection credential as a bearer token. The primary loopback
// listener is unauthenticated by design, so any local process, including the
// agents AO starts, could otherwise read what these routes return; a preview
// cookie never confers API authority either. The LAN socket is the transport
// fact behind requestscope.IsLAN, which a forged header cannot set.
func remoteLinkOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requestscope.IsLAN(r.Context()) || !authenticatedLANBearer(r) {
			envelope.WriteAPIError(w, r, http.StatusForbidden, "forbidden", "REMOTE_LINK_REQUIRED",
				"This route is only served to a linked machine over its authenticated remote connection.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

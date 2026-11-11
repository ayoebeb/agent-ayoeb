package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemoteHostEnableReportsTheBoundListenerAddress(t *testing.T) {
	cfg := setConfigEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/mobile/enable-lan-only":
			_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","port":45012,"endpoints":[{"kind":"lan","host":"192.168.1.10","port":45012}],"password":"pairing-secret"}`)
		case "/api/v1/mobile/enable-tunnel-only":
			_, _ = io.WriteString(w, `{"enabled":true,"hostId":"host-a","loopbackOnly":true,"port":45013,"tunnel":{"supported":true,"running":true},"password":"pairing-secret"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	writeRunFileFor(t, cfg, srv)

	for _, tc := range []struct{ args, want string }{
		{"enable", "Listening on 0.0.0.0:45012"},
		{"enable --tunnel-only", "Listening on 127.0.0.1:45013"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			out, _, err := executeCLI(t, Deps{ProcessAlive: func(int) bool { return true }}, append([]string{"remote-host"}, strings.Fields(tc.args)...)...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("output %q missing %q", out, tc.want)
			}
		})
	}
}

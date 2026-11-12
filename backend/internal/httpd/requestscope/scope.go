package requestscope

import "context"

type lanKey struct{}

func WithLAN(ctx context.Context) context.Context {
	return context.WithValue(ctx, lanKey{}, true)
}

func IsLAN(ctx context.Context) bool {
	value, _ := ctx.Value(lanKey{}).(bool)
	return value
}

// DesktopCredentialHeader carries the local desktop app's credential for
// administrator terminals. Only the desktop's main process sets it, and only
// on its own window's requests to this machine's daemon; the renderer never
// sees the value. It means nothing on its own: the daemon still refuses it
// from the LAN listener, a tunnel, or an agent's CLI call.
const DesktopCredentialHeader = "X-AO-Desktop-Elevation"

type desktopCredentialKey struct{}

// WithDesktopCredential records the request's desktop credential, unchecked.
func WithDesktopCredential(ctx context.Context, credential string) context.Context {
	if credential == "" {
		return ctx
	}
	return context.WithValue(ctx, desktopCredentialKey{}, credential)
}

// DesktopCredential returns the credential WithDesktopCredential recorded.
func DesktopCredential(ctx context.Context) string {
	value, _ := ctx.Value(desktopCredentialKey{}).(string)
	return value
}

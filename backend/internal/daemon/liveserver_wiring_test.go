package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
)

type fakePreviewReaper struct {
	handles []string
	owner   string
	closed  []string
	failOn  string
}

func (f *fakePreviewReaper) CommandTerminalHandlesOwnedBy(_ context.Context, owner string) ([]string, error) {
	f.owner = owner
	return f.handles, nil
}

func (f *fakePreviewReaper) CloseShellTerminal(_ context.Context, handle string) error {
	if handle == f.failOn {
		return errors.New("still running")
	}
	f.closed = append(f.closed, handle)
	return nil
}

func TestReapAbandonedPreviewTerminalsClosesThisLaunchsPreviewTerminals(t *testing.T) {
	reaper := &fakePreviewReaper{handles: []string{"a", "stuck", "b"}, failOn: "stuck"}
	n := reapAbandonedPreviewTerminals(context.Background(), reaper, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if reaper.owner != previewOwner {
		t.Fatalf("owner = %q, want %q", reaper.owner, previewOwner)
	}
	if n != 2 || !reflect.DeepEqual(reaper.closed, []string{"a", "b"}) {
		t.Fatalf("closed %d %v, want a and b closed and the stuck one skipped", n, reaper.closed)
	}
}

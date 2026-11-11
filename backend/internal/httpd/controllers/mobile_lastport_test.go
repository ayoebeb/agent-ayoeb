package controllers

import (
	"path/filepath"
	"testing"

	"github.com/aoagents/agent-orchestrator/backend/internal/mobilebridge"
)

// movedPortLAN records the port each start asks for and binds the one after
// it, as LANManager does when another process holds the requested port.
type movedPortLAN struct {
	fakeLAN
	asked []int
	bound int
}

func (f *movedPortLAN) Start(port int) (int, error) {
	f.asked = append(f.asked, port)
	f.running, f.bindHost, f.bound = true, "0.0.0.0", port+1
	return f.bound, nil
}

func (f *movedPortLAN) StartLoopback(port int) (int, error) {
	f.asked = append(f.asked, port)
	f.running, f.bindHost, f.bound = true, "127.0.0.1", port+1
	return f.bound, nil
}

func (f *movedPortLAN) BoundPort() int {
	if !f.running {
		return 0
	}
	return f.bound
}

func TestMobileEnableStartsFromTheSavedLastPort(t *testing.T) {
	const defaultPort, lastPort = 45011, 45020
	for _, enable := range []struct {
		name string
		call func(*BridgeService) (MobileStatusResponse, error)
	}{
		{"enable", (*BridgeService).Enable},
		{"lan only", (*BridgeService).EnableLANOnly},
		{"tunnel only", (*BridgeService).EnableTunnelOnly},
	} {
		for _, tc := range []struct {
			name  string
			saved int
			want  int
		}{
			{"saved port", lastPort, lastPort},
			{"no saved port", 0, defaultPort},
		} {
			t.Run(enable.name+"/"+tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "mobile", "config.json")
				if err := mobilebridge.Save(path, mobilebridge.State{LastPort: tc.saved}); err != nil {
					t.Fatal(err)
				}
				lan := &movedPortLAN{}
				b := &BridgeService{
					LAN:                lan,
					ConfigPath:         path,
					DefaultPort:        defaultPort,
					PickLANHosts:       func() []string { return nil },
					PickTailscaleHosts: func() []string { return nil },
				}
				res, err := enable.call(b)
				if err != nil {
					t.Fatal(err)
				}
				if len(lan.asked) != 1 || lan.asked[0] != tc.want {
					t.Fatalf("listener asked for %v, want [%d]", lan.asked, tc.want)
				}
				state, err := mobilebridge.Load(path)
				if err != nil {
					t.Fatal(err)
				}
				// The port actually bound is what devices are told and what the
				// next enable starts from.
				if res.Port != tc.want+1 || state.LastPort != tc.want+1 {
					t.Fatalf("reported %d, saved %d; want the bound port %d", res.Port, state.LastPort, tc.want+1)
				}
			})
		}
	}
}

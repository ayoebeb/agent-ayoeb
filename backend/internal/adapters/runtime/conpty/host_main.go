// host_main.go is the RunHost entrypoint for the "ao pty-host" subcommand.
// It is cross-platform: the loopback TCP bind and signal wiring work on all
// OSes; native PTY creation is OS-gated via build tags.
package conpty

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// RunHost is the "ao pty-host" entrypoint. argv is everything after the
// subcommand name: [--start=attach] <sessionId> <cwd> <shellCmd> [shellArg...]
//
// --start=attach defers starting the process until a client first reports its
// grid (see deferredPTY), so it starts at the size its viewer shows. Without it
// the process starts immediately at the default grid.
//
// It binds 127.0.0.1:0 (OS assigns the port), creates the native PTY, prints
// "READY:<pid> <port>\n" to stdout (the parent process reads this to learn the
// port), installs SIGTERM/SIGINT handlers, then runs Serve. Returns a process
// exit code.
//
// ponytail: loopback bind only; any local process on this host can connect to
// the assigned port. A per-session random token handshake is the upgrade path
// if multi-user isolation is needed.
func RunHost(args []string, stdout io.Writer) int {
	elevated, args := splitElevatedArgs(args)
	opts, args := splitHostOptionArgs(args)
	if len(args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: ao pty-host [--start=attach] [--surface=lazy] [--size=COLSxROWS] <sessionId> <cwd> <shellCmd> [shellArg...]\n")
		return 1
	}

	sessionID := args[0]
	cwd := args[1]
	shellCmd := args[2]
	shellArgs := args[3:]
	if err := os.Chdir(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: chdir %s: %v\n", sessionID, cwd, err)
		return 1
	}

	if elevated.enabled() {
		return runElevatedHost(elevated, opts, sessionID, cwd, shellCmd, shellArgs)
	}

	// Bind before creating the PTY so we can report READY atomically.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: listen: %v\n", sessionID, err)
		return 1
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		fmt.Fprintf(os.Stderr, "pty-host [%s]: listener is not TCP\n", sessionID)
		return 1
	}
	port := tcpAddr.Port

	var pty ptyConn
	cols, rows, deferred := hostStartGrid(opts)
	if deferred {
		pty = newDeferredPTY(func(cols, rows uint16) (ptyConn, error) {
			conn, err := newConPTY(cwd, shellCmd, shellArgs, cols, rows)
			if err != nil {
				fmt.Fprintf(os.Stderr, "pty-host [%s]: newConPTY: %v\n", sessionID, err)
			}
			return conn, err
		})
	} else {
		pty, err = newConPTY(cwd, shellCmd, shellArgs, cols, rows)
		if err != nil {
			_ = ln.Close()
			fmt.Fprintf(os.Stderr, "pty-host [%s]: newConPTY: %v\n", sessionID, err)
			return 1
		}
	}

	// Print READY after both the listener and the PTY are up.
	_, _ = fmt.Fprintf(stdout, "READY:%d %d\n", pty.PID(), port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Install signal handlers so SIGTERM/SIGINT trigger graceful shutdown.
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case sig := <-sigC:
			fmt.Fprintf(os.Stderr, "pty-host [%s]: signal %v, shutting down\n", sessionID, sig)
			cancel()
		case <-ctx.Done():
		}
	}()

	ring := NewRing()
	cfg := ServeConfig{
		SessionID:   sessionID,
		Listener:    ln,
		PTY:         pty,
		Ring:        ring,
		LazySurface: opts.LazySurface,
	}

	if err := Serve(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: serve: %v\n", sessionID, err)
		return 1
	}
	return 0
}

// runElevatedHost serves an administrator shell on its authenticated pipe
// (see elevated.go). Nothing reads its stdout: UAC launches it detached from
// the daemon, which finds it by dialing the pipe.
func runElevatedHost(elevated elevatedHostConfig, opts HostOptions, sessionID, cwd, shellCmd string, shellArgs []string) int {
	ln, err := listenElevatedHost(elevated)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: elevated listen: %v\n", sessionID, err)
		return 1
	}
	// Whatever environment UAC handed us, the shell gets no AO credential and
	// the same terminal capabilities as any other AO terminal.
	for _, key := range elevatedChildEnvCleared {
		_ = os.Unsetenv(key)
	}
	_ = os.Setenv("TERM", "xterm-256color")
	_ = os.Setenv("COLORTERM", "truecolor")

	var pty ptyConn
	cols, rows, deferred := hostStartGrid(opts)
	if deferred {
		pty = newDeferredPTY(func(cols, rows uint16) (ptyConn, error) {
			return newConPTY(cwd, shellCmd, shellArgs, cols, rows)
		})
	} else {
		pty, err = newConPTY(cwd, shellCmd, shellArgs, cols, rows)
		if err != nil {
			_ = ln.Close()
			fmt.Fprintf(os.Stderr, "pty-host [%s]: newConPTY: %v\n", sessionID, err)
			return 1
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watchElevatedParent(ctx.Done(), elevated.ParentPID, cancel)
	superviseElevatedHost(ctx.Done(), ln, cancel)

	cfg := ServeConfig{SessionID: sessionID, Listener: ln, PTY: pty, Ring: NewRing(), LazySurface: opts.LazySurface}
	if err := Serve(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "pty-host [%s]: serve: %v\n", sessionID, err)
		return 1
	}
	return 0
}

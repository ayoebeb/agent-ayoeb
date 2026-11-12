//go:build windows

package conpty

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"

	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var (
	modShell32              = windows.NewLazySystemDLL("shell32.dll")
	procShellExecuteExW     = modShell32.NewProc("ShellExecuteExW")
	modUser32               = windows.NewLazySystemDLL("user32.dll")
	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW; Go's field alignment matches the
// C layout on both 386 and amd64/arm64.
type shellExecuteInfo struct {
	cbSize         uint32
	fMask          uint32
	hwnd           windows.Handle
	lpVerb         *uint16
	lpFile         *uint16
	lpParameters   *uint16
	lpDirectory    *uint16
	nShow          int32
	hInstApp       windows.Handle
	lpIDList       uintptr
	lpClass        *uint16
	hkeyClass      windows.Handle
	dwHotKey       uint32
	hIconOrMonitor windows.Handle
	hProcess       windows.Handle
}

const (
	seeMaskNoCloseProcess = 0x00000040
	seeMaskNoAsync        = 0x00000100
	seeMaskFlagNoUI       = 0x00000400
	swHide                = 0
	// sFalse is COM's "already initialized on this thread" success code.
	sFalse = syscall.Errno(1)

	elevatedPipeBufferSize = 64 << 10
)

// shellExecuteRunAs starts exe through the UAC prompt and returns the new
// process handle; the caller closes it. It blocks until the user answers the
// prompt and returns ports.ErrElevationCancelled when they decline. Tests
// replace it: a real call would put a UAC prompt on the user's screen.
var shellExecuteRunAs = func(exe, params, dir string) (windows.Handle, error) {
	// ShellExecuteEx may hand the launch to shell extensions, which need COM on
	// the calling thread, so the whole call stays on one locked thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err == nil || errors.Is(err, sFalse) {
		defer windows.CoUninitialize()
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}
	file, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	args, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return 0, err
	}
	cwd, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	// The user just clicked in AO, so its window is in the foreground. Owning
	// the prompt by that window lets UAC show it at once instead of as a
	// flashing taskbar button.
	foreground, _, _ := procGetForegroundWindow.Call()
	info := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync | seeMaskFlagNoUI,
		hwnd:         windows.Handle(foreground),
		lpVerb:       verb,
		lpFile:       file,
		lpParameters: args,
		lpDirectory:  cwd,
		nShow:        swHide,
	}
	info.cbSize = uint32(unsafe.Sizeof(info))
	ok, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&info)))
	if ok == 0 {
		if errors.Is(callErr, windows.ERROR_CANCELLED) {
			return 0, ports.ErrElevationCancelled
		}
		return 0, fmt.Errorf("start elevated pty-host: %w", callErr)
	}
	if info.hProcess == 0 {
		return 0, errors.New("start elevated pty-host: no process handle")
	}
	return info.hProcess, nil
}

// defaultSpawnElevatedHost launches the pty-host through UAC and waits until
// it serves its pipe. The token and pipe name never touch disk or the
// registry: they reach the host in its argv and stay in daemon memory.
func defaultSpawnElevatedHost(ctx context.Context, sessionID, cwd string, argv []string, opts HostOptions) (string, int, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, fmt.Errorf("elevated spawn: resolve executable: %w", err)
	}
	token, err := randomHex(32)
	if err != nil {
		return "", 0, fmt.Errorf("elevated spawn: token: %w", err)
	}
	pipeID, err := randomHex(16)
	if err != nil {
		return "", 0, fmt.Errorf("elevated spawn: pipe name: %w", err)
	}
	cfg := elevatedHostConfig{Pipe: elevatedPipePrefix + pipeID, Token: token, ParentPID: os.Getpid()}
	_, argv = stripEnvAssignments(argv)
	hostArgs := ptyHostArgs(sessionID, cwd, argv, opts)
	args := append([]string{hostArgs[0]}, append(elevatedHostArgs(cfg), hostArgs[1:]...)...)
	if err := validateWindowsCommandLine(append([]string{exe}, args...)); err != nil {
		return "", 0, fmt.Errorf("elevated spawn: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	process, err := shellExecuteRunAs(exe, windows.ComposeCommandLine(args), cwd)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	pid, err := windows.GetProcessId(process)
	if err != nil {
		return "", 0, fmt.Errorf("elevated spawn: process id: %w", err)
	}
	addr := elevatedAddr{Pipe: cfg.Pipe, Token: cfg.Token, ServerPID: int(pid)}
	// The prompt may have outlived the request that asked for it. Readiness
	// gets its own budget so a host that arrives late is still found and ended.
	readyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), spawnReadyTimeout)
	defer cancel()
	if err := waitElevatedHostReady(readyCtx, addr, process); err != nil {
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		_ = clientKill(addr.String())
		return "", 0, err
	}
	return addr.String(), int(pid), nil
}

// waitElevatedHostReady polls until the launched host serves its pipe, fails
// fast when the host exits, and refuses a pipe another process serves.
func waitElevatedHostReady(ctx context.Context, addr elevatedAddr, process windows.Handle) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if event, _ := windows.WaitForSingleObject(process, 0); event == windows.WAIT_OBJECT_0 {
			var code uint32
			_ = windows.GetExitCodeProcess(process, &code)
			return fmt.Errorf("elevated pty-host exited (code %d) before serving its pipe", code)
		}
		conn, err := dialElevatedHost(ctx, addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if errors.Is(err, errElevatedPipeImpostor) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("elevated pty-host did not serve its pipe: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

var errElevatedPipeImpostor = errors.New("elevated pty-host pipe is served by another process")

// dialElevatedHost connects to an elevated host's pipe, proves the server is
// the process AO launched, and authenticates with the host's token.
func dialElevatedHost(ctx context.Context, addr elevatedAddr, timeout time.Duration) (net.Conn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := winio.DialPipeContext(dialCtx, addr.Pipe)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			// No pipe means no host: report it like a refused loopback dial.
			return nil, fmt.Errorf("elevated pty-host pipe is gone: %w", syscall.ECONNREFUSED)
		}
		return nil, fmt.Errorf("dial elevated pty-host: %w", err)
	}
	serverPID, err := pipePeerPID(conn, windows.GetNamedPipeServerProcessId)
	if err != nil || serverPID != addr.ServerPID {
		_ = conn.Close()
		return nil, fmt.Errorf("%w (server pid %d, launched pid %d)", errElevatedPipeImpostor, serverPID, addr.ServerPID)
	}
	if err := writeAuthFrame(conn, addr.Token); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := readAuthAck(conn, timeout); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &elevatedConn{Conn: conn}, nil
}

// elevatedFlushTimeout bounds how long closing a connection waits for the
// host to read what was written on it.
var elevatedFlushTimeout = 2 * time.Second

// elevatedConn waits for the host to read everything before hanging up. The
// host answers every new connection with its scrollback; over a pipe, that
// write fails at once if the client has already gone, and the host then drops
// the connection with a frame (keystrokes, a kill) still unread. TCP hides
// this race, a pipe does not, so the client flushes first (FlushFileBuffers on
// a pipe returns once the other end has read all buffered data).
type elevatedConn struct {
	net.Conn
}

func (c *elevatedConn) Close() error {
	if flusher, ok := c.Conn.(interface{ Flush() error }); ok {
		flushed := make(chan struct{})
		go func() {
			_ = flusher.Flush()
			close(flushed)
		}()
		timer := time.NewTimer(elevatedFlushTimeout)
		select {
		case <-flushed:
		case <-timer.C:
		}
		timer.Stop()
	}
	return c.Conn.Close()
}

func pipePeerPID(conn net.Conn, query func(windows.Handle, *uint32) error) (int, error) {
	f, ok := conn.(interface{ Fd() uintptr })
	if !ok {
		return 0, errors.New("not a named pipe connection")
	}
	var pid uint32
	if err := query(windows.Handle(f.Fd()), &pid); err != nil {
		return 0, err
	}
	return int(pid), nil
}

func pipeClientPID(conn net.Conn) (int, error) {
	return pipePeerPID(conn, windows.GetNamedPipeClientProcessId)
}

// elevatedPipeSDDL grants the current user only (a protected DACL, so nothing
// is inherited), with a medium no-write-up label so the unelevated daemon of
// that same user may connect while lower-integrity processes may not write.
func elevatedPipeSDDL() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("read current user: %w", err)
	}
	return "D:P(A;;GA;;;" + user.User.Sid.String() + ")S:(ML;;NW;;;ME)", nil
}

// listenElevatedHost serves the host's pipe. winio creates the first instance
// with FILE_CREATE, so a pipe someone else created first fails here rather
// than being shared, and remote (SMB) clients are rejected.
func listenElevatedHost(cfg elevatedHostConfig) (*authListener, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	sddl, err := elevatedPipeSDDL()
	if err != nil {
		return nil, err
	}
	// Buffered like a TCP socket: with winio's zero default every write would
	// wait for the other side's read.
	ln, err := winio.ListenPipe(cfg.Pipe, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		InputBufferSize:    elevatedPipeBufferSize,
		OutputBufferSize:   elevatedPipeBufferSize,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", cfg.Pipe, err)
	}
	return newAuthListener(ln, cfg.Token, cfg.ParentPID, pipeClientPID), nil
}

// watchElevatedParent stops the host when the daemon that launched it exits,
// so an administrator shell never outlives the daemon showing it. A parent
// that cannot be watched stops the host at once.
func watchElevatedParent(done <-chan struct{}, parentPID int, stop func()) {
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(parentPID))
	if err != nil {
		stop()
		return
	}
	go func() {
		defer func() { _ = windows.CloseHandle(parent) }()
		for {
			if event, _ := windows.WaitForSingleObject(parent, 250); event == windows.WAIT_OBJECT_0 {
				stop()
				return
			}
			select {
			case <-done:
				return
			default:
			}
		}
	}()
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

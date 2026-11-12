package daemon

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// desktopHandoff is what the desktop app writes to a daemon it spawns, over the
// daemon's private stdin pipe, and then closes:
//
//	<browser runtime token>\n
//	<elevation secret>\n      (optional; absent from older apps)
//
// Neither value is ever put in a file, a command line or the environment. The
// elevation secret in particular is generated per app run and held only in
// memory on both sides, so nothing on disk lets another process of this user
// (an agent, a script) compute it. A daemon this app did not spawn has no
// secret, and administrator terminals stay unavailable there.
type desktopHandoff struct {
	browserRuntimeToken string
	elevationSecret     string
}

const (
	maxHandoffLineBytes = 256
	// minElevationSecretBytes rejects anything weaker than the 32 random bytes
	// (43 base64url characters) the app generates.
	minElevationSecretBytes = 43
)

func readDesktopHandoff(r io.Reader) (desktopHandoff, error) {
	reader := bufio.NewReader(io.LimitReader(r, 2*(maxHandoffLineBytes+1)))
	token, err := readHandoffLine(reader)
	if err != nil {
		return desktopHandoff{}, fmt.Errorf("read browser runtime token: %w", err)
	}
	if token == "" {
		return desktopHandoff{}, errors.New("browser runtime token handoff was empty")
	}
	secret, err := readHandoffLine(reader)
	if err != nil {
		return desktopHandoff{}, fmt.Errorf("read elevation secret: %w", err)
	}
	if secret != "" && len(secret) < minElevationSecretBytes {
		return desktopHandoff{}, errors.New("elevation secret handoff was too short")
	}
	return desktopHandoff{browserRuntimeToken: token, elevationSecret: secret}, nil
}

func readHandoffLine(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if len(line) > maxHandoffLineBytes {
		return "", errors.New("handoff line was too long")
	}
	return line, nil
}

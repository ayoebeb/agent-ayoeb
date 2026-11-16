package jira

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	connectionsFile = "jira-connections.json"
	linksFile       = "jira-links.json"
	fileVersion     = 1
)

// storedConnection is one project's Jira connection as written to disk. The
// API token lives here and nowhere else: it is never part of a response type,
// a log line or telemetry.
type storedConnection struct {
	SiteURL     string    `json:"siteUrl"`
	Email       string    `json:"email"`
	APIToken    string    `json:"apiToken"`
	ProjectKey  string    `json:"projectKey,omitempty"`
	BoardID     int       `json:"boardId,omitempty"`
	JQL         string    `json:"jql,omitempty"`
	AccountID   string    `json:"accountId,omitempty"`
	AccountName string    `json:"accountName,omitempty"`
	ConnectedAt time.Time `json:"connectedAt"`
}

type connectionsDoc struct {
	Version     int                         `json:"version"`
	Connections map[string]storedConnection `json:"connections"`
}

// storedLink records that a session received a ticket.
type storedLink struct {
	ProjectID string    `json:"projectId"`
	IssueKey  string    `json:"issueKey"`
	SessionID string    `json:"sessionId"`
	StartedAt time.Time `json:"startedAt"`
}

type linksDoc struct {
	Version int          `json:"version"`
	Links   []storedLink `json:"links"`
}

// readJSON reads path into v. A missing file leaves v untouched.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return nil
}

// writeJSON writes v to path with owner-only permissions, through a temp file
// so a crash never leaves half a document (or half a token) behind.
func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, os.ErrPermission) {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Service) connectionsPath() string { return filepath.Join(s.dataDir, connectionsFile) }
func (s *Service) linksPath() string       { return filepath.Join(s.dataDir, linksFile) }

// loadConnectionsLocked reads every project's connection. The caller holds s.mu.
func (s *Service) loadConnectionsLocked() (connectionsDoc, error) {
	doc := connectionsDoc{Version: fileVersion, Connections: map[string]storedConnection{}}
	if err := readJSON(s.connectionsPath(), &doc); err != nil {
		return doc, err
	}
	if doc.Connections == nil {
		doc.Connections = map[string]storedConnection{}
	}
	return doc, nil
}

func (s *Service) loadLinksLocked() (linksDoc, error) {
	doc := linksDoc{Version: fileVersion}
	err := readJSON(s.linksPath(), &doc)
	return doc, err
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

// MarkAgentStop records that AO stopped the session's agent on purpose and kept
// the session, so the worker keeper does not resume it as a crash or a restart.
// The newest stop replaces the older one.
func (s *Store) MarkAgentStop(ctx context.Context, id domain.SessionID, at time.Time, reason string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO agent_stop_marks (session_id, stopped_at, reason) VALUES (?, ?, ?)
		 ON CONFLICT (session_id) DO UPDATE SET stopped_at = excluded.stopped_at, reason = excluded.reason`,
		string(id), at.UTC(), reason)
	if err != nil {
		return fmt.Errorf("mark agent stop for %s: %w", id, err)
	}
	return nil
}

// AgentStopMark returns when AO last stopped the session's agent on purpose.
func (s *Store) AgentStopMark(ctx context.Context, id domain.SessionID) (time.Time, bool, error) {
	var at time.Time
	err := s.readDB.QueryRowContext(ctx,
		`SELECT stopped_at FROM agent_stop_marks WHERE session_id = ?`, string(id)).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read agent stop for %s: %w", id, err)
	}
	return at, true, nil
}

// ListWatchedWorkerIDs returns the live, awake worker sessions whose agent is
// exited or idle: the only ones the worker keeper resumes or watches. It reads
// the id column only, so a sweep does not decode every session.
func (s *Store) ListWatchedWorkerIDs(ctx context.Context) ([]domain.SessionID, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id FROM sessions
		 WHERE kind = 'worker' AND is_terminated = 0 AND is_task_preparation = 0
		   AND provision_state IN ('', 'ready') AND hibernated_at IS NULL
		   AND activity_state IN ('exited', 'idle')
		 ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list watched workers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []domain.SessionID
	for rows.Next() {
		var id domain.SessionID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan watched worker: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// ErrAutopilotRunOpen is returned when a session already has an open run.
var ErrAutopilotRunOpen = errors.New("session already has an open autopilot run")

// CreateAutopilotRun inserts a running run. A session can have only one open run.
func (s *Store) CreateAutopilotRun(ctx context.Context, run domain.AutopilotRun) (domain.AutopilotRun, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.CreateAutopilotRun(ctx, gen.CreateAutopilotRunParams{
		ID:            run.ID,
		SessionID:     string(run.SessionID),
		ProjectID:     nullString(string(run.ProjectID)),
		Task:          run.Task,
		Criteria:      run.Criteria,
		MaxIterations: int64(run.MaxIterations),
		StartedAt:     run.StartedAt,
		UpdatedAt:     run.UpdatedAt,
	})
	if err != nil {
		if isSQLiteUnique(err) {
			return domain.AutopilotRun{}, ErrAutopilotRunOpen
		}
		return domain.AutopilotRun{}, fmt.Errorf("create autopilot run: %w", err)
	}
	return autopilotRunFromGen(row), nil
}

// GetAutopilotRun reads one run by id.
func (s *Store) GetAutopilotRun(ctx context.Context, id string) (domain.AutopilotRun, bool, error) {
	row, err := s.qr.GetAutopilotRun(ctx, id)
	return autopilotRunResult(row, err, "get autopilot run")
}

// GetOpenAutopilotRunForSession reads the session's running, paused or parked run.
func (s *Store) GetOpenAutopilotRunForSession(ctx context.Context, id domain.SessionID) (domain.AutopilotRun, bool, error) {
	row, err := s.qr.GetOpenAutopilotRunForSession(ctx, string(id))
	return autopilotRunResult(row, err, "get open autopilot run")
}

// GetLatestAutopilotRunForSession reads the session's most recent run, open or finished.
func (s *Store) GetLatestAutopilotRunForSession(ctx context.Context, id domain.SessionID) (domain.AutopilotRun, bool, error) {
	row, err := s.qr.GetLatestAutopilotRunForSession(ctx, string(id))
	return autopilotRunResult(row, err, "get latest autopilot run")
}

// ListOpenAutopilotRuns returns every running, paused or parked run, oldest first.
func (s *Store) ListOpenAutopilotRuns(ctx context.Context) ([]domain.AutopilotRun, error) {
	rows, err := s.qr.ListOpenAutopilotRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open autopilot runs: %w", err)
	}
	return autopilotRunsFromGen(rows), nil
}

// ListActiveAutopilotRunsForProject returns the project's running or paused runs.
func (s *Store) ListActiveAutopilotRunsForProject(ctx context.Context, id domain.ProjectID) ([]domain.AutopilotRun, error) {
	rows, err := s.qr.ListActiveAutopilotRunsForProject(ctx, nullString(string(id)))
	if err != nil {
		return nil, fmt.Errorf("list active autopilot runs for project: %w", err)
	}
	return autopilotRunsFromGen(rows), nil
}

// UpdateAutopilotRun writes every mutable column of the run.
func (s *Store) UpdateAutopilotRun(ctx context.Context, run domain.AutopilotRun) (domain.AutopilotRun, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.UpdateAutopilotRun(ctx, gen.UpdateAutopilotRunParams{
		Criteria:           run.Criteria,
		State:              string(run.State),
		EndReason:          string(run.EndReason),
		Iteration:          int64(run.Iteration),
		MaxIterations:      int64(run.MaxIterations),
		StagnantIterations: int64(run.Stagnant),
		Fingerprint:        run.Fingerprint,
		LastGap:            run.LastGap,
		Evidence:           run.Evidence,
		CIGap:              run.CIGap,
		PermissionsAllowed: int64(run.PermissionsAllowed),
		PermissionsDenied:  int64(run.PermissionsDenied),
		UpdatedAt:          run.UpdatedAt,
		LastNudgeAt:        nullTime(run.LastNudgeAt),
		EndedAt:            nullTime(run.EndedAt),
		ID:                 run.ID,
	})
	if err != nil {
		return domain.AutopilotRun{}, fmt.Errorf("update autopilot run %s: %w", run.ID, err)
	}
	return autopilotRunFromGen(row), nil
}

// CreateAutopilotEvent appends one line to a run's log.
func (s *Store) CreateAutopilotEvent(ctx context.Context, ev domain.AutopilotEvent) (domain.AutopilotEvent, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.CreateAutopilotEvent(ctx, gen.CreateAutopilotEventParams{
		ID:        ev.ID,
		RunID:     ev.RunID,
		SessionID: string(ev.SessionID),
		Kind:      string(ev.Kind),
		Iteration: int64(ev.Iteration),
		Summary:   ev.Summary,
		Detail:    ev.Detail,
		CreatedAt: ev.CreatedAt,
	})
	if err != nil {
		return domain.AutopilotEvent{}, fmt.Errorf("create autopilot event: %w", err)
	}
	return autopilotEventFromGen(row), nil
}

// GetAutopilotEvent reads one log line.
func (s *Store) GetAutopilotEvent(ctx context.Context, id string) (domain.AutopilotEvent, bool, error) {
	row, err := s.qr.GetAutopilotEvent(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AutopilotEvent{}, false, nil
	}
	if err != nil {
		return domain.AutopilotEvent{}, false, fmt.Errorf("get autopilot event: %w", err)
	}
	return autopilotEventFromGen(row), true, nil
}

// ListAutopilotEventsForRun returns up to limit log lines, newest first.
func (s *Store) ListAutopilotEventsForRun(ctx context.Context, runID string, limit int) ([]domain.AutopilotEvent, error) {
	rows, err := s.qr.ListAutopilotEventsForRun(ctx, gen.ListAutopilotEventsForRunParams{RunID: runID, RowLimit: int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("list autopilot events: %w", err)
	}
	return autopilotEventsFromGen(rows), nil
}

// ListOpenParkedAutopilotEvents returns the run's parked items the user has not handled.
func (s *Store) ListOpenParkedAutopilotEvents(ctx context.Context, runID string) ([]domain.AutopilotEvent, error) {
	rows, err := s.qr.ListOpenParkedAutopilotEvents(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("list open parked autopilot events: %w", err)
	}
	return autopilotEventsFromGen(rows), nil
}

// CountOpenParkedAutopilotEventsForSession counts the session's parked items still open.
func (s *Store) CountOpenParkedAutopilotEventsForSession(ctx context.Context, id domain.SessionID) (int, error) {
	n, err := s.qr.CountOpenParkedAutopilotEventsForSession(ctx, string(id))
	if err != nil {
		return 0, fmt.Errorf("count open parked autopilot events: %w", err)
	}
	return int(n), nil
}

// ResolveAutopilotEvent marks a parked item handled. It reports false when the
// item does not exist, is not a parked item, or was already handled.
func (s *Store) ResolveAutopilotEvent(ctx context.Context, id string, at time.Time) (domain.AutopilotEvent, bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	row, err := s.qw.ResolveAutopilotEvent(ctx, gen.ResolveAutopilotEventParams{ResolvedAt: nullTime(at), ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AutopilotEvent{}, false, nil
	}
	if err != nil {
		return domain.AutopilotEvent{}, false, fmt.Errorf("resolve autopilot event: %w", err)
	}
	return autopilotEventFromGen(row), true, nil
}

// ListMergedPRNotificationsForProjectSince returns the project's "PR merged"
// notifications created at or after since, oldest first.
func (s *Store) ListMergedPRNotificationsForProjectSince(ctx context.Context, project domain.ProjectID, since time.Time) ([]domain.NotificationRecord, error) {
	rows, err := s.qr.ListMergedPRNotificationsForProjectSince(ctx, gen.ListMergedPRNotificationsForProjectSinceParams{
		ProjectID: &project,
		CreatedAt: since,
	})
	if err != nil {
		return nil, fmt.Errorf("list merged PR notifications: %w", err)
	}
	return notificationsFromGen(rows), nil
}

// AutopilotMaxIterations reads the default iteration cap.
func (s *Store) AutopilotMaxIterations(ctx context.Context) (int, error) {
	n, err := s.qr.GetAutopilotMaxIterations(ctx)
	if err != nil {
		return 0, fmt.Errorf("read autopilot max iterations: %w", err)
	}
	return int(n), nil
}

// SetAutopilotMaxIterations persists the default iteration cap.
func (s *Store) SetAutopilotMaxIterations(ctx context.Context, n int, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.SetAutopilotMaxIterations(ctx, gen.SetAutopilotMaxIterationsParams{
		AutopilotMaxIterations: int64(n),
		UpdatedAt:              now,
	}); err != nil {
		return fmt.Errorf("set autopilot max iterations: %w", err)
	}
	return nil
}

func autopilotRunResult(row gen.AutopilotRun, err error, what string) (domain.AutopilotRun, bool, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AutopilotRun{}, false, nil
	}
	if err != nil {
		return domain.AutopilotRun{}, false, fmt.Errorf("%s: %w", what, err)
	}
	return autopilotRunFromGen(row), true, nil
}

func autopilotRunFromGen(row gen.AutopilotRun) domain.AutopilotRun {
	return domain.AutopilotRun{
		ID:                 row.ID,
		SessionID:          domain.SessionID(row.SessionID),
		ProjectID:          domain.ProjectID(row.ProjectID.String),
		Task:               row.Task,
		Criteria:           row.Criteria,
		State:              domain.AutopilotState(row.State),
		EndReason:          domain.AutopilotEndReason(row.EndReason),
		Iteration:          int(row.Iteration),
		MaxIterations:      int(row.MaxIterations),
		Stagnant:           int(row.StagnantIterations),
		Fingerprint:        row.Fingerprint,
		LastGap:            row.LastGap,
		Evidence:           row.Evidence,
		CIGap:              row.CIGap,
		PermissionsAllowed: int(row.PermissionsAllowed),
		PermissionsDenied:  int(row.PermissionsDenied),
		StartedAt:          row.StartedAt,
		UpdatedAt:          row.UpdatedAt,
		LastNudgeAt:        timeFromNull(row.LastNudgeAt),
		EndedAt:            timeFromNull(row.EndedAt),
	}
}

func autopilotRunsFromGen(rows []gen.AutopilotRun) []domain.AutopilotRun {
	out := make([]domain.AutopilotRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, autopilotRunFromGen(row))
	}
	return out
}

func autopilotEventFromGen(row gen.AutopilotEvent) domain.AutopilotEvent {
	return domain.AutopilotEvent{
		ID:         row.ID,
		RunID:      row.RunID,
		SessionID:  domain.SessionID(row.SessionID),
		Kind:       domain.AutopilotEventKind(row.Kind),
		Iteration:  int(row.Iteration),
		Summary:    row.Summary,
		Detail:     row.Detail,
		CreatedAt:  row.CreatedAt,
		ResolvedAt: timeFromNull(row.ResolvedAt),
	}
}

func autopilotEventsFromGen(rows []gen.AutopilotEvent) []domain.AutopilotEvent {
	out := make([]domain.AutopilotEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, autopilotEventFromGen(row))
	}
	return out
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

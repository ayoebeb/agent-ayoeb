package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// ListLatestFailedTurns returns, for each given session, its latest turn when
// that turn failed. Whether the failure is a usage limit is the caller's call.
func (s *Store) ListLatestFailedTurns(ctx context.Context, ids []domain.SessionID) ([]domain.LimitStopTurn, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("marshal session ids: %w", err)
	}
	rows, err := s.qr.ListLatestFailedTurns(ctx, string(encoded))
	if err != nil {
		return nil, fmt.Errorf("list latest failed turns: %w", err)
	}
	out := make([]domain.LimitStopTurn, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.LimitStopTurn{
			SessionID: domain.SessionID(row.SessionID), TurnID: row.TurnID, ErrorMessage: row.ErrorMessage,
			CompletedAt: timePtr(row.CompletedAt), RequestedAt: row.RequestedAt,
			StoredResetsAt: timePtr(row.UsageLimitResetsAt), RetryArmed: row.UsageLimitAutoResume != 0,
			ResumeState: row.ResumeState, SkipReason: row.SkipReason,
		})
	}
	return out, nil
}

// ListLimitStopCandidates returns the failed turns that are still the latest of
// their session's conversation, completed since the cutoff, and not yet
// followed up or still waiting to be resumed.
func (s *Store) ListLimitStopCandidates(ctx context.Context, completedSince time.Time) ([]domain.LimitStopTurn, error) {
	rows, err := s.qr.ListLimitStopCandidates(ctx, sql.NullTime{Time: completedSince.UTC(), Valid: true})
	if err != nil {
		return nil, fmt.Errorf("list limit stop candidates: %w", err)
	}
	out := make([]domain.LimitStopTurn, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.LimitStopTurn{
			SessionID: domain.SessionID(row.SessionID), TurnID: row.TurnID, ErrorMessage: row.ErrorMessage,
			CompletedAt: timePtr(row.CompletedAt), RequestedAt: row.RequestedAt,
			StoredResetsAt: timePtr(row.UsageLimitResetsAt), RetryArmed: row.UsageLimitAutoResume != 0,
			ResumeState: row.ResumeState, SkipReason: row.SkipReason,
			StopResetsAt: timePtr(row.StopResetsAt), RetryAt: timePtr(row.RetryAt), Attempts: int(row.Attempts),
		})
	}
	return out, nil
}

// InsertUsageLimitStop records a stop and reports whether this call recorded it:
// false means it was already known.
func (s *Store) InsertUsageLimitStop(ctx context.Context, stop domain.UsageLimitStop) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.InsertUsageLimitStop(ctx, gen.InsertUsageLimitStopParams{
		TurnID: stop.TurnID, SessionID: string(stop.SessionID), ProjectID: string(stop.ProjectID),
		AccountKey: stop.AccountKey, WindowKey: stop.WindowKey, WindowLabel: stop.WindowLabel,
		ResetsAt: nullableTime(utcOrZero(stop.ResetsAt)), DetectedAt: stop.DetectedAt.UTC(),
	})
	if err != nil {
		return false, fmt.Errorf("insert usage-limit stop %s: %w", stop.TurnID, err)
	}
	return n > 0, nil
}

func utcOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// ListPendingUsageLimitStops returns the stops still waiting to be resumed.
func (s *Store) ListPendingUsageLimitStops(ctx context.Context) ([]domain.UsageLimitStop, error) {
	rows, err := s.qr.ListPendingUsageLimitStops(ctx)
	if err != nil {
		return nil, fmt.Errorf("list pending usage-limit stops: %w", err)
	}
	out := make([]domain.UsageLimitStop, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.UsageLimitStop{
			TurnID: row.TurnID, SessionID: domain.SessionID(row.SessionID), ProjectID: domain.ProjectID(row.ProjectID),
			AccountKey: row.AccountKey, WindowKey: row.WindowKey, WindowLabel: row.WindowLabel,
			ResetsAt: timePtr(row.ResetsAt), DetectedAt: row.DetectedAt, ReportedAt: timePtr(row.ReportedAt),
			ResumeState: row.ResumeState, SkipReason: row.SkipReason, Attempts: int(row.Attempts), RetryAt: timePtr(row.RetryAt),
		})
	}
	return out, nil
}

// ListUnreportedUsageLimitStops returns the stops whose orchestrator report has
// not gone out yet and is still worth sending.
func (s *Store) ListUnreportedUsageLimitStops(ctx context.Context) ([]domain.UsageLimitStop, error) {
	rows, err := s.qr.ListUnreportedUsageLimitStops(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unreported usage-limit stops: %w", err)
	}
	out := make([]domain.UsageLimitStop, 0, len(rows))
	for _, row := range rows {
		out = append(out, domain.UsageLimitStop{
			TurnID: row.TurnID, SessionID: domain.SessionID(row.SessionID), ProjectID: domain.ProjectID(row.ProjectID),
			AccountKey: row.AccountKey, WindowKey: row.WindowKey, WindowLabel: row.WindowLabel,
			ResetsAt: timePtr(row.ResetsAt), DetectedAt: row.DetectedAt, ReportedAt: timePtr(row.ReportedAt),
			ResumeState: row.ResumeState, SkipReason: row.SkipReason, Attempts: int(row.Attempts), RetryAt: timePtr(row.RetryAt),
		})
	}
	return out, nil
}

// MarkUsageLimitStopReported records that the orchestrator was told.
func (s *Store) MarkUsageLimitStopReported(ctx context.Context, turnID string, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.MarkUsageLimitStopReported(ctx, gen.MarkUsageLimitStopReportedParams{
		ReportedAt: nullableTime(at.UTC()), TurnID: turnID,
	})
}

// SkipUsageLimitStop stops AO from resuming a pending stop. It reports whether
// a pending stop was changed.
func (s *Store) SkipUsageLimitStop(ctx context.Context, turnID, reason string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, err := s.qw.SkipUsageLimitStop(ctx, gen.SkipUsageLimitStopParams{SkipReason: reason, TurnID: turnID})
	if err != nil {
		return false, fmt.Errorf("skip usage-limit stop %s: %w", turnID, err)
	}
	return n > 0, nil
}

// SetPendingUsageLimitStopsDisabled parks every pending stop while automatic
// resume is off, or reopens the parked ones when it is on again.
func (s *Store) SetPendingUsageLimitStopsDisabled(ctx context.Context, disabled bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var err error
	if disabled {
		_, err = s.qw.SkipPendingUsageLimitStopsDisabled(ctx)
	} else {
		_, err = s.qw.ReopenDisabledUsageLimitStops(ctx)
	}
	if err != nil {
		return fmt.Errorf("apply usage-limit resume setting: %w", err)
	}
	return nil
}

// DeferUsageLimitStop counts a failed attempt and delays the next one.
func (s *Store) DeferUsageLimitStop(ctx context.Context, turnID string, retryAt time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.DeferUsageLimitStop(ctx, gen.DeferUsageLimitStopParams{RetryAt: nullableTime(retryAt.UTC()), TurnID: turnID})
}

// MarkUsageLimitStopResumed records that the continue message went out, and
// which turn it started (the newest requested at or after since).
func (s *Store) MarkUsageLimitStopResumed(ctx context.Context, turnID string, resumedAt, since time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.MarkUsageLimitStopResumed(ctx, gen.MarkUsageLimitStopResumedParams{
		ResumedAt: nullableTime(resumedAt.UTC()), Since: since.UTC(), TurnID: turnID,
	})
}

// CountRunningUsageLimitResumes is how many continued turns are still running.
func (s *Store) CountRunningUsageLimitResumes(ctx context.Context) (int, error) {
	n, err := s.qr.CountRunningUsageLimitResumes(ctx)
	return int(n), err
}

// LastUsageLimitResumeAt is when AO last continued a session after a limit.
func (s *Store) LastUsageLimitResumeAt(ctx context.Context) (time.Time, bool, error) {
	at, err := s.qr.LastUsageLimitResumeAt(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil || !at.Valid {
		return time.Time{}, false, err
	}
	return at.Time, true, nil
}

// CountRecentUsageLimitResumes is how many times AO continued the session since.
func (s *Store) CountRecentUsageLimitResumes(ctx context.Context, session domain.SessionID, since time.Time) (int, error) {
	n, err := s.qr.CountRecentUsageLimitResumes(ctx, gen.CountRecentUsageLimitResumesParams{
		SessionID: string(session), ResumedAt: nullableTime(since.UTC()),
	})
	return int(n), err
}

// SetUsageLimitAutoResume stores the automatic resume preference.
func (s *Store) SetUsageLimitAutoResume(ctx context.Context, enabled bool, maxConcurrent int, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.qw.SetUsageLimitAutoResume(ctx, gen.SetUsageLimitAutoResumeParams{
		UsageLimitAutoResume: enabled, UsageLimitMaxResumes: int64(maxConcurrent), UpdatedAt: now.UTC(),
	})
}

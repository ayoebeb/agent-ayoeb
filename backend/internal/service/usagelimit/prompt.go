package usagelimit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	"github.com/aoagents/agent-orchestrator/backend/internal/service/agentaccounts"
)

// Prompt errors the API maps to statuses.
var (
	ErrPromptNotFound      = errors.New("usage-limit prompt not found")
	ErrPromptAnswered      = errors.New("usage-limit prompt was already answered")
	ErrPromptTargetInvalid = errors.New("the chosen account cannot take these sessions")
	ErrPromptSwitchFailed  = errors.New("no stopped session could be moved")
)

// AccountLister reads the accounts with their latest limit readings.
type AccountLister interface {
	List(ctx context.Context, refresh bool) ([]agentaccounts.AccountView, error)
}

// SessionMover moves a session's agent to another account through the durable
// handoff. It returns ports.ErrAgentAccountUnchanged for a session that is
// already there.
type SessionMover interface {
	MoveToAccount(ctx context.Context, id domain.SessionID, accountID string) error
}

// Weekly window id as the provider's reading names it.
const weeklyWindowID = "seven_day"

// PromptOption is an account the owner can switch to, with its usage.
type PromptOption struct {
	AccountID           string
	Email               string
	Plan                string
	Device              bool
	FiveHourUsedPercent *float64
	FiveHourResetsAt    *time.Time
	WeeklyUsedPercent   *float64
	WeeklyResetsAt      *time.Time
	// Full is true when a window of the account is used up until its reset.
	Full bool
}

// PromptSession is a session that stopped on the limit.
type PromptSession struct {
	ID          domain.SessionID
	DisplayName string
	Kind        domain.SessionKind
	ProjectID   domain.ProjectID
}

// Prompt is one open question: an account hit its limit, these sessions stopped,
// and these accounts could take them.
type Prompt struct {
	ID           string
	Provider     string
	WindowLabel  string
	AccountID    string
	AccountEmail string
	ResetsAt     *time.Time
	CreatedAt    time.Time
	Sessions     []PromptSession
	Options      []PromptOption
}

// SwitchResult is what a Switch answer started.
type SwitchResult struct {
	Moved  []domain.SessionID
	Failed []SwitchFailure
}

// SwitchFailure is a session that could not be moved.
type SwitchFailure struct {
	SessionID domain.SessionID
	Reason    string
}

func windowUsage(limits *domain.AgentAccountLimits, id string) (used *float64, resets *time.Time) {
	if limits == nil {
		return nil, nil
	}
	for _, w := range limits.Windows {
		if w.ID == id {
			v := w.UsedPercent
			return &v, w.ResetsAt
		}
	}
	return nil, nil
}

func optionFor(view agentaccounts.AccountView, now time.Time) PromptOption {
	opt := PromptOption{
		AccountID: view.Account.ID, Email: view.Account.Email, Plan: view.Account.Plan, Device: view.Account.Device(),
	}
	opt.FiveHourUsedPercent, opt.FiveHourResetsAt = windowUsage(view.Limits, agentaccounts.FiveHourWindowID)
	opt.WeeklyUsedPercent, opt.WeeklyResetsAt = windowUsage(view.Limits, weeklyWindowID)
	if view.Limits != nil {
		for _, w := range view.Limits.Windows {
			if w.UsedPercent >= 100 && (w.ResetsAt == nil || w.ResetsAt.After(now)) {
				opt.Full = true
			}
		}
	}
	return opt
}

// switchTargets are the signed-in accounts of provider other than exclude, the
// ones with the most 5-hour room first.
func (s *Service) switchTargets(ctx context.Context, provider, exclude string) ([]PromptOption, map[string]agentaccounts.AccountView, error) {
	views, err := s.lister.List(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]agentaccounts.AccountView, len(views))
	var out []PromptOption
	now := s.now()
	for _, v := range views {
		byID[v.Account.ID] = v
		if string(v.Account.Provider) != provider || v.Account.ID == exclude || !v.SignedIn || !v.LoggedIn {
			continue
		}
		out = append(out, optionFor(v, now))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Full != out[j].Full {
			return !out[i].Full
		}
		return fiveHourUsed(out[i]) < fiveHourUsed(out[j])
	})
	return out, byID, nil
}

func fiveHourUsed(o PromptOption) float64 {
	if o.FiveHourUsedPercent == nil {
		return 50 // unknown sorts between a light and a heavy account
	}
	return *o.FiveHourUsedPercent
}

// hasSwitchTargets reports whether another signed-in account could take sessions.
func (s *Service) hasSwitchTargets(ctx context.Context, provider, exclude string) bool {
	if s.lister == nil {
		return false
	}
	targets, _, err := s.switchTargets(ctx, provider, exclude)
	if err != nil {
		return false
	}
	for _, t := range targets {
		if !t.Full {
			return true
		}
	}
	return false
}

// openPromptSessions are the sessions of a prompt that are still stopped.
func (s *Service) openPromptSessions(ctx context.Context, p domain.UsageLimitPrompt) ([]PromptSession, []domain.UsageLimitStop, error) {
	stops, err := s.store.ListUsageLimitPromptStops(ctx, p.WindowKey)
	if err != nil {
		return nil, nil, err
	}
	var sessions []PromptSession
	var live []domain.UsageLimitStop
	seen := map[domain.SessionID]bool{}
	for _, stop := range stops {
		if seen[stop.SessionID] {
			continue
		}
		rec, found, err := s.store.GetSession(ctx, stop.SessionID)
		if err != nil {
			return nil, nil, err
		}
		if !found || rec.IsTerminated || rec.Activity.State == domain.ActivityExited {
			continue
		}
		seen[stop.SessionID] = true
		live = append(live, stop)
		sessions = append(sessions, PromptSession{ID: rec.ID, DisplayName: rec.DisplayName, Kind: rec.Kind, ProjectID: rec.ProjectID})
	}
	// The orchestrator first: it is the one the owner talks to.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].Kind == domain.KindOrchestrator && sessions[j].Kind != domain.KindOrchestrator
	})
	return sessions, live, nil
}

// ListPrompts returns the questions waiting for the owner. A prompt whose
// sessions all moved on, or that has no other account to offer, is not shown.
func (s *Service) ListPrompts(ctx context.Context) ([]Prompt, error) {
	if s.lister == nil {
		return nil, nil
	}
	open, err := s.store.ListOpenUsageLimitPrompts(ctx)
	if err != nil {
		return nil, err
	}
	var out []Prompt
	for _, p := range open {
		sessions, _, err := s.openPromptSessions(ctx, p)
		if err != nil {
			return nil, err
		}
		if len(sessions) == 0 {
			continue
		}
		options, byID, err := s.switchTargets(ctx, p.Provider, p.AccountKey)
		if err != nil {
			return nil, err
		}
		if len(options) == 0 {
			continue
		}
		prompt := Prompt{
			ID: p.ID, Provider: p.Provider, WindowLabel: p.WindowLabel, AccountID: p.AccountKey,
			ResetsAt: p.ResetsAt, CreatedAt: p.CreatedAt, Sessions: sessions, Options: options,
		}
		if v, ok := byID[p.AccountKey]; ok {
			prompt.AccountEmail = v.Account.Email
		}
		out = append(out, prompt)
	}
	return out, nil
}

// Switch moves every session that stopped on the prompt's limit to targetID and,
// as each move completes, continues the turn it interrupted. The prompt is
// answered once: a second answer finds it closed.
func (s *Service) Switch(ctx context.Context, id, targetID string) (SwitchResult, error) {
	if s.lister == nil || s.mover == nil {
		return SwitchResult{}, ports.ErrAgentAccountUnknown
	}
	p, found, err := s.store.GetUsageLimitPrompt(ctx, id)
	if err != nil {
		return SwitchResult{}, err
	}
	if !found {
		return SwitchResult{}, ErrPromptNotFound
	}
	if p.State != domain.UsageLimitPromptOpen {
		return SwitchResult{}, ErrPromptAnswered
	}
	options, _, err := s.switchTargets(ctx, p.Provider, p.AccountKey)
	if err != nil {
		return SwitchResult{}, err
	}
	valid := false
	for _, o := range options {
		if o.AccountID == targetID && !o.Full {
			valid = true
		}
	}
	if !valid {
		return SwitchResult{}, ErrPromptTargetInvalid
	}
	sessions, _, err := s.openPromptSessions(ctx, p)
	if err != nil {
		return SwitchResult{}, err
	}
	if claimed, err := s.store.DecideUsageLimitPrompt(ctx, id, domain.UsageLimitPromptSwitched, targetID, s.now()); err != nil {
		return SwitchResult{}, err
	} else if !claimed {
		return SwitchResult{}, ErrPromptAnswered
	}
	var result SwitchResult
	for _, sess := range sessions {
		err := s.mover.MoveToAccount(ctx, sess.ID, targetID)
		switch {
		case err == nil:
			result.Moved = append(result.Moved, sess.ID)
		case errors.Is(err, ports.ErrAgentAccountUnchanged):
			// Already on the account (moved by hand): continue it right away.
			if _, rerr := s.ResumeAfterAccountMove(ctx, sess.ID, targetID); rerr != nil {
				result.Failed = append(result.Failed, SwitchFailure{SessionID: sess.ID, Reason: rerr.Error()})
				continue
			}
			result.Moved = append(result.Moved, sess.ID)
		default:
			result.Failed = append(result.Failed, SwitchFailure{SessionID: sess.ID, Reason: err.Error()})
		}
	}
	if len(result.Moved) == 0 && len(result.Failed) > 0 {
		if err := s.store.ReopenUsageLimitPrompt(ctx, id); err != nil {
			s.log.Warn("reopen usage-limit prompt", "prompt", id, "err", err)
		}
		return result, fmt.Errorf("%w: %s", ErrPromptSwitchFailed, result.Failed[0].Reason)
	}
	return result, nil
}

// Wait answers the prompt with "wait for the reset": nothing moves, and the
// sessions continue after the reset as before.
func (s *Service) Wait(ctx context.Context, id string) error {
	_, found, err := s.store.GetUsageLimitPrompt(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrPromptNotFound
	}
	claimed, err := s.store.DecideUsageLimitPrompt(ctx, id, domain.UsageLimitPromptWaiting, "", s.now())
	if err != nil {
		return err
	}
	if !claimed {
		return ErrPromptAnswered
	}
	return nil
}

// ensurePrompt records the owner prompt of a limit window the first time one of
// its sessions stops.
func (s *Service) ensurePrompt(ctx context.Context, stop domain.UsageLimitStop, rec domain.SessionRecord, now time.Time) error {
	if s.lister == nil || stop.AccountKey == "" {
		return nil
	}
	provider, ok := domain.AgentAccountProviderFor(rec.Harness)
	if !ok {
		return nil
	}
	_, err := s.store.InsertUsageLimitPrompt(ctx, domain.UsageLimitPrompt{
		ID: domain.UsageLimitPromptID(stop.WindowKey), WindowKey: stop.WindowKey, AccountKey: stop.AccountKey,
		Provider: string(provider), WindowLabel: stop.WindowLabel, ResetsAt: stop.ResetsAt, CreatedAt: now,
	})
	return err
}

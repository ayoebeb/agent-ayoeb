package autopilot

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Advisor tells Autopilot how the owner decides, from the choices they made in
// normal mode. It only advises: the hard limits are enforced here, before and
// after anything an Advisor says, and nothing it returns can lift one.
type Advisor interface {
	// Advise returns the owner's profile and the most similar past decisions for
	// a question, as text for the agent; empty when nothing has been learned.
	Advise(ctx context.Context, project, question string, options []string) string
	// Describe turns pair ids into short readable lines; unknown ids are dropped.
	Describe(ids []string) []string
	// Correct records that the owner undid a choice, so the profile learns from it.
	Correct(ctx context.Context, project, session, question, choice string)
}

// Confidence levels an agent states with a decision.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Basis says what a decision was based on: the past decisions of the owner it
// followed (ids from `ao autopilot advise`) and how sure the agent is.
type Basis struct {
	PairIDs    []string
	Confidence string
}

const (
	maxBasisIDs = 5
	// maxAdviceRunes bounds the advice put into an open question's message.
	maxAdviceRunes = 1600
)

func (b Basis) valid() error {
	switch b.Confidence {
	case "", ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
	default:
		return apierr.Invalid("AUTOPILOT_CONFIDENCE_INVALID", "confidence must be high, medium or low", nil)
	}
	if len(b.PairIDs) > maxBasisIDs {
		return apierr.Invalid("AUTOPILOT_BASIS_TOO_LONG", fmt.Sprintf("name at most %d past decisions", maxBasisIDs), nil)
	}
	return nil
}

// basisDetail renders the "learned from" lines that follow a decision's reason in
// the log, so the owner sees which of their own past choices it followed.
func (s *Service) basisDetail(b Basis) string {
	var lines []string
	if s.advisor != nil && len(b.PairIDs) > 0 {
		for _, l := range s.advisor.Describe(b.PairIDs) {
			lines = append(lines, "Based on your past choice: "+l)
		}
	}
	switch b.Confidence {
	case ConfidenceLow:
		lines = append(lines, "Confidence: low. Autopilot took the safest option that can be undone.")
	case ConfidenceMedium, ConfidenceHigh:
		lines = append(lines, "Confidence: "+b.Confidence+".")
	}
	return strings.Join(lines, "\n")
}

// Advise returns what the owner would choose for a question, for the agent of
// an open run. It never decides anything.
func (s *Service) Advise(ctx context.Context, id domain.SessionID, question string, options []string) (string, error) {
	if err := requireAgent(ctx, id); err != nil {
		return "", err
	}
	question = domain.CleanAutopilotText(question, 400)
	if question == "" {
		return "", apierr.Invalid("AUTOPILOT_QUESTION_REQUIRED", "Say what you would ask the owner", nil)
	}
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err != nil {
		return "", fmt.Errorf("autopilot: read run: %w", err)
	}
	if !ok {
		return "", noOpenRun()
	}
	return s.adviceFor(ctx, run.ProjectID, question, options), nil
}

const noAdvice = "Nothing has been learned yet about how the owner decides this. Use your recommended option, record it with --confidence low, and prefer the safest option that can be undone."

func (s *Service) adviceFor(ctx context.Context, project domain.ProjectID, question string, options []string) string {
	if s.advisor == nil {
		return noAdvice
	}
	if text := s.advisor.Advise(ctx, string(project), question, options); text != "" {
		return text
	}
	return noAdvice
}

// correct hands the owner's undo to the advisor as a correction.
func (s *Service) correct(ctx context.Context, run domain.AutopilotRun, ev domain.AutopilotEvent) {
	if s.advisor == nil {
		return
	}
	question, choice, ok := strings.Cut(ev.Summary, " → ")
	if !ok || strings.TrimSpace(question) == "" || strings.TrimSpace(choice) == "" {
		return
	}
	s.advisor.Correct(ctx, string(run.ProjectID), string(run.SessionID), strings.TrimSpace(question), strings.TrimSpace(choice))
}

// digestParked sends one notification for everything parked and still open in a
// run, once the oldest item has waited the digest interval. The agent keeps
// working on everything else meanwhile. The source key names the set of items, so
// an unchanged set never notifies twice, even across a restart.
func (s *Service) digestParked(ctx context.Context, run domain.AutopilotRun, now time.Time) {
	if s.digestEvery <= 0 || s.notifier == nil {
		return
	}
	open, err := s.store.ListOpenParkedAutopilotEvents(ctx, run.ID)
	if err != nil || len(open) == 0 {
		return
	}
	oldest := open[0].CreatedAt
	ids := make([]string, 0, len(open))
	for _, ev := range open {
		if ev.CreatedAt.Before(oldest) {
			oldest = ev.CreatedAt
		}
		ids = append(ids, ev.ID)
	}
	if now.Sub(oldest) < s.digestEvery {
		return
	}
	sort.Strings(ids)
	key := domain.AutopilotParkedSourceKeyPrefix + "digest:" + fingerprintOf(ids...)
	s.mu.Lock()
	if s.digestSent[run.ID] == key {
		s.mu.Unlock()
		return
	}
	s.digestSent[run.ID] = key
	s.mu.Unlock()

	name := ""
	if rec, ok, err := s.store.GetSession(ctx, run.SessionID); err == nil && ok {
		name = rec.DisplayName
	}
	items := make([]string, 0, len(open))
	for _, ev := range open {
		items = append(items, clip(ev.Summary, 80))
	}
	detail := fmt.Sprintf("%d items need you, Autopilot carried on with the rest: %s", len(open), strings.Join(items, "; "))
	// The earlier digest (a smaller set) is replaced, not stacked.
	if err := s.notifier.Resolve(ctx, ports.NotificationResolution{
		Type: domain.NotificationAutopilotParked, SessionID: run.SessionID, ResolvedAt: now,
	}); err != nil {
		s.log.Warn("autopilot: replace the parked digest", "session", run.SessionID, "err", err)
	}
	if err := s.notifier.Notify(ctx, ports.NotificationIntent{
		Type: domain.NotificationAutopilotParked, SessionID: run.SessionID, ProjectID: run.ProjectID,
		SessionDisplayName: name, Detail: clip(detail, 240), SourceKey: key, CreatedAt: now,
	}); err != nil {
		s.mu.Lock()
		delete(s.digestSent, run.ID)
		s.mu.Unlock()
		s.log.Warn("autopilot: notify the parked digest", "run", run.ID, "err", err)
	}
}

// CoversSession reports whether the session is covered by an Autopilot run that
// is running or paused: its own, or its project's (an orchestrator's run covers
// the workers it started). The usage-limit watcher asks this to decide a limit by
// the owner's profile instead of asking the owner.
func (s *Service) CoversSession(ctx context.Context, id domain.SessionID) bool {
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil || !ok {
		return false
	}
	runs, err := s.store.ListActiveAutopilotRunsForProject(ctx, rec.ProjectID)
	return err == nil && len(runs) > 0
}

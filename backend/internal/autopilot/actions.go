package autopilot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
)

// openRunLocked reads the session's run that still drives work (running or paused).
// The caller holds s.mu.
func (s *Service) activeRunLocked(ctx context.Context, id domain.SessionID) (domain.AutopilotRun, error) {
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err != nil {
		return domain.AutopilotRun{}, fmt.Errorf("autopilot: read run: %w", err)
	}
	if !ok || run.State == domain.AutopilotParked {
		return domain.AutopilotRun{}, noOpenRun()
	}
	return run, nil
}

// SetCriteria records the acceptance criteria the agent drafted from the task.
func (s *Service) SetCriteria(ctx context.Context, id domain.SessionID, criteria string) (domain.AutopilotRun, error) {
	if err := requireAgent(ctx, id); err != nil {
		return domain.AutopilotRun{}, err
	}
	criteria = domain.CleanAutopilotText(criteria, domain.AutopilotMaxFieldRunes)
	if criteria == "" {
		return domain.AutopilotRun{}, apierr.Invalid("AUTOPILOT_CRITERIA_REQUIRED", "Write the criteria as a short checklist that says when the task is done", nil)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.activeRunLocked(ctx, id)
	if err != nil {
		return domain.AutopilotRun{}, err
	}
	run.Criteria, run.UpdatedAt = criteria, s.clock()
	updated, err := s.store.UpdateAutopilotRun(ctx, run)
	if err != nil {
		return domain.AutopilotRun{}, fmt.Errorf("autopilot: record criteria: %w", err)
	}
	return updated, nil
}

// Decide records a choice the agent made instead of asking the user. A choice
// that would cross a hard limit is refused: that item is parked, not decided.
func (s *Service) Decide(ctx context.Context, id domain.SessionID, question, choice, why string) (domain.AutopilotEvent, error) {
	return s.DecideWithBasis(ctx, id, question, choice, why, Basis{})
}

// DecideWithBasis is Decide with what the choice was based on: which of the
// owner's past decisions it followed and how confident the agent is. Both are
// written into the log line. The hard-limit check is the same and comes first.
func (s *Service) DecideWithBasis(ctx context.Context, id domain.SessionID, question, choice, why string, basis Basis) (domain.AutopilotEvent, error) {
	if err := requireAgent(ctx, id); err != nil {
		return domain.AutopilotEvent{}, err
	}
	if err := basis.valid(); err != nil {
		return domain.AutopilotEvent{}, err
	}
	question = domain.CleanAutopilotText(question, 400)
	choice = domain.CleanAutopilotText(choice, 400)
	why = domain.CleanAutopilotText(why, 800)
	if question == "" || choice == "" || why == "" {
		return domain.AutopilotEvent{}, apierr.Invalid("AUTOPILOT_DECISION_INCOMPLETE", "A decision needs the question, what you chose, and why", nil)
	}
	if limit, crossed := ClassifyText(choice); crossed {
		return domain.AutopilotEvent{}, apierr.Invalid("AUTOPILOT_HARD_LIMIT",
			fmt.Sprintf("That choice would %s, which Autopilot never does. Choose another option, or park the item with `ao autopilot park --limit %s --item \"...\" --why \"...\"` and carry on with the rest.",
				limit.Description(), limit),
			map[string]any{"limit": string(limit)})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, err := s.activeRunLocked(ctx, id)
	if err != nil {
		return domain.AutopilotEvent{}, err
	}
	detail := why
	if extra := s.basisDetail(basis); extra != "" {
		detail += "\n" + extra
	}
	ev := s.logEventLocked(ctx, run, domain.AutopilotEventDecision, question+" → "+choice, detail)
	if strings.HasPrefix(run.LastGap, openQuestionPrefix) {
		run.LastGap, run.UpdatedAt = "", s.clock()
		if _, err := s.store.UpdateAutopilotRun(ctx, run); err != nil {
			s.log.Warn("autopilot: clear the answered question", "run", run.ID, "err", err)
		}
	}
	if ev.ID == "" {
		return domain.AutopilotEvent{}, fmt.Errorf("autopilot: record decision")
	}
	return ev, nil
}

// Park sets one item aside because it needs a hard limit crossed. The user gets
// one notification for it; the run carries on with everything else. Parking the
// same open item again is a no-op that does not notify again.
func (s *Service) Park(ctx context.Context, id domain.SessionID, limit domain.HardLimit, item, why string) (domain.AutopilotEvent, bool, error) {
	if err := requireAgent(ctx, id); err != nil {
		return domain.AutopilotEvent{}, false, err
	}
	if !limit.Valid() {
		return domain.AutopilotEvent{}, false, apierr.Invalid("AUTOPILOT_LIMIT_INVALID",
			"limit must be one of: "+hardLimitNames(), nil)
	}
	item = domain.CleanAutopilotText(item, 300)
	why = domain.CleanAutopilotText(why, 600)
	if item == "" || why == "" {
		return domain.AutopilotEvent{}, false, apierr.Invalid("AUTOPILOT_PARK_INCOMPLETE", "Say which item you parked and why", nil)
	}
	s.mu.Lock()
	run, err := s.activeRunLocked(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return domain.AutopilotEvent{}, false, err
	}
	return s.parkItem(ctx, run, limit, item, why)
}

// parkItem records one parked item and sends its one notification. It is shared
// by the agent's `park` and by the permission policy, which parks what it denies.
func (s *Service) parkItem(ctx context.Context, run domain.AutopilotRun, limit domain.HardLimit, item, why string) (domain.AutopilotEvent, bool, error) {
	s.mu.Lock()
	open, err := s.store.ListOpenParkedAutopilotEvents(ctx, run.ID)
	if err != nil {
		s.mu.Unlock()
		return domain.AutopilotEvent{}, false, fmt.Errorf("autopilot: read parked items: %w", err)
	}
	for _, existing := range open {
		if strings.EqualFold(existing.Summary, item) {
			s.mu.Unlock()
			if s.digestEvery > 0 {
				return existing, true, nil
			}
			// The notification is idempotent on the item's id, so a retry after a
			// failed notify still reaches the user, and a repeat sends nothing new.
			if err := s.notifyParked(ctx, run, existing); err != nil {
				return existing, true, fmt.Errorf("autopilot: tell the user: %w", err)
			}
			return existing, true, nil
		}
	}
	ev := s.logEventLocked(ctx, run, domain.AutopilotEventParked, item, fmt.Sprintf("%s: %s", limit, why))
	s.mu.Unlock()
	if ev.ID == "" {
		return domain.AutopilotEvent{}, false, fmt.Errorf("autopilot: record parked item")
	}
	if s.digestEvery > 0 {
		// Batched: the maintenance pass sends one notification for the whole set.
		return ev, false, nil
	}
	if err := s.notifyParked(ctx, run, ev); err != nil {
		return ev, false, fmt.Errorf("autopilot: tell the user: %w", err)
	}
	return ev, false, nil
}

// ResolveParked marks a parked item handled, as the user's decision that it no
// longer needs them. It closes the parked notification when nothing else is left.
func (s *Service) ResolveParked(ctx context.Context, eventID string) (domain.AutopilotEvent, error) {
	if err := requireUser(ctx); err != nil {
		return domain.AutopilotEvent{}, err
	}
	ev, ok, err := s.store.GetAutopilotEvent(ctx, eventID)
	if err != nil {
		return domain.AutopilotEvent{}, fmt.Errorf("autopilot: read item: %w", err)
	}
	if !ok || ev.Kind != domain.AutopilotEventParked {
		return domain.AutopilotEvent{}, apierr.NotFound("AUTOPILOT_ITEM_NOT_FOUND", "Unknown parked item")
	}
	s.mu.Lock()
	resolved, changed, err := s.store.ResolveAutopilotEvent(ctx, eventID, s.clock())
	s.mu.Unlock()
	if err != nil {
		return domain.AutopilotEvent{}, fmt.Errorf("autopilot: mark handled: %w", err)
	}
	if !changed {
		return ev, nil
	}
	if run, found, _ := s.store.GetAutopilotRun(ctx, ev.RunID); found {
		s.resolveParkedNotifications(ctx, run)
	}
	return resolved, nil
}

// UndoDecision records that the user wants a decision reversed and asks the
// agent to reverse it. The decision stays in the log, marked undone, so the
// return report still shows what happened. Undoing the same decision again is a
// no-op that does not message the agent twice.
func (s *Service) UndoDecision(ctx context.Context, eventID string) (domain.AutopilotEvent, error) {
	if err := requireUser(ctx); err != nil {
		return domain.AutopilotEvent{}, err
	}
	s.undoMu.Lock()
	defer s.undoMu.Unlock()
	ev, ok, err := s.store.GetAutopilotEvent(ctx, eventID)
	if err != nil {
		return domain.AutopilotEvent{}, fmt.Errorf("autopilot: read decision: %w", err)
	}
	if !ok || ev.Kind != domain.AutopilotEventDecision {
		return domain.AutopilotEvent{}, apierr.NotFound("AUTOPILOT_DECISION_NOT_FOUND", "Unknown decision")
	}
	if !ev.ResolvedAt.IsZero() {
		return ev, nil
	}
	run, found, err := s.store.GetAutopilotRun(ctx, ev.RunID)
	if err != nil {
		return domain.AutopilotEvent{}, fmt.Errorf("autopilot: read run: %w", err)
	}
	if !found {
		return domain.AutopilotEvent{}, apierr.NotFound("AUTOPILOT_DECISION_NOT_FOUND", "Unknown decision")
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := s.messenger.Send(sendCtx, run.SessionID, undoMessage(ev, run.State.Open())); err != nil {
		return domain.AutopilotEvent{}, apierr.Unavailable("AUTOPILOT_UNDO_UNDELIVERED",
			"The agent could not be reached, so the decision was not undone yet. Try again in a moment.")
	}
	s.mu.Lock()
	resolved, changed, err := s.store.ResolveAutopilotEvent(ctx, eventID, s.clock())
	s.mu.Unlock()
	if err != nil {
		return domain.AutopilotEvent{}, fmt.Errorf("autopilot: mark undone: %w", err)
	}
	if !changed {
		return ev, nil
	}
	s.correct(ctx, run, resolved)
	return resolved, nil
}

// OnWaitDeclared is called when an orchestrator declares that it is waiting for
// the user (`ao notify-user --waiting`). While Autopilot runs for it, the user is
// not told: the question becomes an open decision the agent must take itself,
// which the next iteration quotes back. It reports whether it took the wait.
func (s *Service) OnWaitDeclared(ctx context.Context, id domain.SessionID, question string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err != nil {
		return false, fmt.Errorf("autopilot: read run: %w", err)
	}
	if !ok || run.State == domain.AutopilotParked {
		return false, nil
	}
	question = domain.CleanAutopilotText(question, 300)
	s.logEventLocked(ctx, run, domain.AutopilotEventWaitDiverted, "Would have waited for you: "+question, "Autopilot told the agent to decide it itself.")
	run.LastGap = openQuestionPrefix + question + " Autopilot is on, so decide it yourself with your recommended option and record it with `ao autopilot decide`."
	if advice := s.adviceFor(ctx, run.ProjectID, question, nil); advice != noAdvice {
		run.LastGap += "\n" + clip(advice, maxAdviceRunes)
	}
	run.UpdatedAt = s.clock()
	if _, err := s.store.UpdateAutopilotRun(ctx, run); err != nil {
		return true, fmt.Errorf("autopilot: record the open question: %w", err)
	}
	return true, nil
}

// DoneResult is what a done claim came to.
type DoneResult struct {
	// Accepted is true when every gate passed and the run is finished.
	Accepted bool
	// Failures are the gates that did not pass; the agent goes back to work.
	Failures []string
	// Gaps are things recorded but not blocking, such as CI that cannot start.
	Gaps []string
	Run  domain.AutopilotRun
}

// Done is the agent's claim that the task is finished. AO finishes the run only
// when the gates pass; otherwise the failures become what is still open and the
// loop goes on.
func (s *Service) Done(ctx context.Context, id domain.SessionID, evidence, ciCannotRun string) (DoneResult, error) {
	if err := requireAgent(ctx, id); err != nil {
		return DoneResult{}, err
	}
	evidence = domain.CleanAutopilotText(evidence, domain.AutopilotMaxFieldRunes)
	ciCannotRun = domain.CleanAutopilotText(ciCannotRun, 400)
	if evidence == "" {
		return DoneResult{}, apierr.Invalid("AUTOPILOT_EVIDENCE_REQUIRED",
			"Say what you ran to verify the task and what it showed: the test and build commands with their results", nil)
	}
	s.mu.Lock()
	run, err := s.activeRunLocked(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return DoneResult{}, err
	}
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil {
		return DoneResult{}, fmt.Errorf("autopilot: read session: %w", err)
	}
	if !ok {
		return DoneResult{}, sessionNotFound()
	}
	failures, gaps := s.checkGates(ctx, run, rec, evidence, ciCannotRun)

	s.mu.Lock()
	defer s.mu.Unlock()
	cur, found, err := s.store.GetAutopilotRun(ctx, run.ID)
	if err != nil || !found || !cur.State.Open() || cur.State == domain.AutopilotParked {
		return DoneResult{}, noOpenRun()
	}
	cur.Evidence, cur.CIGap = evidence, ciCannotRun
	cur.UpdatedAt = s.clock()
	if len(failures) > 0 {
		cur.LastGap = "Your last done claim failed these checks:\n- " + strings.Join(failures, "\n- ")
		cur, err = s.store.UpdateAutopilotRun(ctx, cur)
		if err != nil {
			return DoneResult{}, fmt.Errorf("autopilot: record the failed claim: %w", err)
		}
		s.logEventLocked(ctx, cur, domain.AutopilotEventGateFailed,
			fmt.Sprintf("Done claim refused: %d check(s) failed", len(failures)), strings.Join(failures, "; "))
		return DoneResult{Failures: failures, Gaps: gaps, Run: cur}, nil
	}
	cur.LastGap = ""
	if _, err := s.store.UpdateAutopilotRun(ctx, cur); err != nil {
		return DoneResult{}, fmt.Errorf("autopilot: record the evidence: %w", err)
	}
	s.finishLocked(ctx, cur.ID, domain.AutopilotDone, domain.AutopilotEndGoalDone, "Done: "+clip(evidence, 200))
	final, _, _ := s.store.GetAutopilotRun(ctx, cur.ID)
	return DoneResult{Accepted: true, Gaps: gaps, Run: final}, nil
}

// checkGates judges a done claim. The gates AO can check itself are the recorded
// criteria and evidence, an approved AO review of each open pull request's exact
// head, and CI. CI that cannot start is a recorded gap, never a pass.
func (s *Service) checkGates(ctx context.Context, run domain.AutopilotRun, rec domain.SessionRecord, evidence, ciCannotRun string) (failures, gaps []string) {
	if strings.TrimSpace(run.Criteria) == "" {
		failures = append(failures, "No acceptance criteria are recorded. Write them with `ao autopilot criteria \"<done when ...>\"`, check each one, and claim done again.")
	}
	if s.verifyTests != nil {
		failures = append(failures, s.verifyTests(ctx, rec, evidence)...)
	}
	owners := append([]domain.SessionRecord{rec}, s.workersInScope(ctx, run, rec)...)
	for _, owner := range owners {
		facts, err := s.store.ListPRFactsForSession(ctx, owner.ID)
		if err != nil {
			failures = append(failures, "AO could not read the pull requests of "+sessionLabel(owner)+"; try again.")
			continue
		}
		var (
			runs        []domain.CurrentHeadReviewRun
			runsLoaded  bool
			runsFailure bool
		)
		for _, f := range facts {
			if f.Merged || f.Closed {
				continue
			}
			if !runsLoaded {
				runsLoaded = true
				if runs, err = s.store.ListCurrentHeadReviewRunsForSession(ctx, owner.ID); err != nil {
					runsFailure = true
					failures = append(failures, "AO could not read the reviews of "+sessionLabel(owner)+"; try again.")
				}
			}
			if runsFailure {
				break
			}
			label := prLabel(f)
			if f.Draft {
				failures = append(failures, label+" is still a draft. Mark it ready for review.")
			}
			if msg := reviewGate(f, runs); msg != "" {
				failures = append(failures, msg)
			}
			switch {
			case f.CI == domain.CIPassing:
			case ciCannotRun != "":
				gaps = append(gaps, fmt.Sprintf("CI for %s is %s; the agent says it cannot run: %s", label, ciWord(f.CI), ciCannotRun))
			default:
				failures = append(failures, fmt.Sprintf("CI for %s is %s, and green is required. If CI cannot start (for example a billing limit), claim done again with `--ci-cannot-run \"<why>\"`: it is recorded as a gap, not a pass.", label, ciWord(f.CI)))
			}
		}
	}
	return failures, gaps
}

// reviewGate requires an approved AO review of the pull request's exact head.
// The runs are already limited to the current head, so a review of an earlier
// commit cannot satisfy it.
func reviewGate(f domain.PRFacts, runs []domain.CurrentHeadReviewRun) string {
	label := prLabel(f)
	var latest *domain.CurrentHeadReviewRun
	for i := range runs {
		r := runs[i]
		if r.PRURL != f.URL || r.Verdict == domain.VerdictNone {
			continue
		}
		if latest == nil || r.CreatedAt.After(latest.CreatedAt) {
			latest = &runs[i]
		}
	}
	switch {
	case latest == nil:
		return label + " has no completed AO review of its current head. Wait for the review of the exact head, then claim done again."
	case latest.Verdict == domain.VerdictChangesRequested:
		return "The AO review of " + label + " asked for changes on its current head. Fix them and wait for a new review."
	}
	return ""
}

func prLabel(f domain.PRFacts) string {
	if f.Number > 0 {
		return "PR #" + strconv.Itoa(f.Number)
	}
	return "The pull request " + f.URL
}

func ciWord(c domain.CIState) string {
	switch c {
	case domain.CIPassing:
		return "passing"
	case domain.CIFailing:
		return "failing"
	case domain.CIPending:
		return "still pending"
	default:
		return "not reporting a result"
	}
}

func sessionLabel(rec domain.SessionRecord) string {
	if rec.DisplayName != "" {
		return rec.DisplayName
	}
	return string(rec.ID)
}

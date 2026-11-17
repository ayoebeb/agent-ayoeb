package autopilot

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ApprovalNotice is one permission request a Chat session is waiting on.
type ApprovalNotice struct {
	RequestID string
	Summary   string
	// Detail is the provider payload (input, tool name, kind) as JSON.
	Detail    []byte
	Decisions []ports.ChatDecisionOption
}

// HandleApproval answers a permission request by policy instead of waiting for
// the user, when the session is covered by a running Autopilot run: the run's
// own session, or a worker the run's orchestrator started after the run began.
// It allows anything inside the session's workspace and denies the hard limits,
// parking a denied hard-limit item with one notification. A request it does not
// cover is left for the user.
//
// Only Chat sessions raise approvals AO can answer. A terminal session keeps its
// prompt inside the terminal; start those in auto or bypass-permissions mode.
func (s *Service) HandleApproval(ctx context.Context, id domain.SessionID, n ApprovalNotice) {
	if s.approvals == nil {
		return
	}
	rec, ok, err := s.store.GetSession(ctx, id)
	if err != nil || !ok {
		return
	}
	run, covered := s.runCovering(ctx, rec)
	if !covered {
		return
	}
	action := ParseApproval(n.Summary, n.Detail, rec.Metadata.WorkspacePath)
	verdict := Evaluate(action)
	if verdict.Merge {
		verdict = s.mergeVerdict(ctx, run, rec, verdict.MergeRef)
	}
	decision, ok := pickDecision(n.Decisions, verdict.Allow)
	if !ok {
		s.log.Warn("autopilot: the provider offered no decision to answer with", "session", id, "request", n.RequestID, "allow", verdict.Allow)
		return
	}
	if err := s.approvals.Resolve(ctx, id, n.RequestID, decision); err != nil {
		// Another client may have answered first; that is fine.
		s.log.Info("autopilot: could not answer an approval", "session", id, "request", n.RequestID, "err", err)
		return
	}

	s.mu.Lock()
	cur, found, err := s.store.GetAutopilotRun(ctx, run.ID)
	if err == nil && found {
		if verdict.Allow {
			cur.PermissionsAllowed++
		} else {
			cur.PermissionsDenied++
		}
		cur.UpdatedAt = s.clock()
		if _, err := s.store.UpdateAutopilotRun(ctx, cur); err != nil {
			s.log.Warn("autopilot: count the approval", "run", run.ID, "err", err)
		}
	}
	s.mu.Unlock()
	if verdict.Allow {
		return
	}
	who := sessionLabel(rec)
	what := clip(firstNonEmpty(action.Command, strings.Join(action.Paths, ", "), action.Summary), 160)
	if verdict.Limit == "" {
		s.mu.Lock()
		s.logEventLocked(ctx, run, domain.AutopilotEventPermissionDenied, fmt.Sprintf("%s was refused: %s", who, what), verdict.Reason)
		s.mu.Unlock()
		return
	}
	item := fmt.Sprintf("%s needed to: %s", who, what)
	if _, _, err := s.parkItem(ctx, run, verdict.Limit, item, verdict.Reason); err != nil {
		s.log.Warn("autopilot: park a denied request", "run", run.ID, "err", err)
	}
}

// runCovering finds the running run whose policy applies to the session.
func (s *Service) runCovering(ctx context.Context, rec domain.SessionRecord) (domain.AutopilotRun, bool) {
	if own, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, rec.ID); err == nil && ok {
		return own, own.State == domain.AutopilotRunning || own.State == domain.AutopilotPausedUsageLimit
	}
	if rec.Kind != domain.KindWorker || rec.ProjectID == "" || rec.IsTaskPreparation {
		return domain.AutopilotRun{}, false
	}
	runs, err := s.store.ListActiveAutopilotRunsForProject(ctx, rec.ProjectID)
	if err != nil {
		return domain.AutopilotRun{}, false
	}
	for _, run := range runs {
		if rec.CreatedAt.Before(run.StartedAt) {
			continue
		}
		owner, ok, err := s.store.GetSession(ctx, run.SessionID)
		if err == nil && ok && owner.Kind == domain.KindOrchestrator {
			return run, true
		}
	}
	return domain.AutopilotRun{}, false
}

// mergeVerdict decides `gh pr merge` against the stored pull-request facts: a
// merge is allowed once the AO review approved the exact head and checks are
// green, which is the rule orchestrators already follow. Autopilot never loosens
// it, and a merge AO cannot match to a tracked pull request is refused.
func (s *Service) mergeVerdict(ctx context.Context, run domain.AutopilotRun, requester domain.SessionRecord, ref string) Verdict {
	owners := []domain.SessionRecord{requester}
	if requester.Kind == domain.KindOrchestrator {
		owners = append(owners, s.workersInScope(ctx, run, requester)...)
	}
	refusal := func(reason string) Verdict {
		return Verdict{Limit: domain.HardLimitMainPush, Reason: reason}
	}
	var (
		match *domain.PRFacts
		owner domain.SessionRecord
		open  []domain.PRFacts
	)
	for _, o := range owners {
		facts, err := s.store.ListPRFactsForSession(ctx, o.ID)
		if err != nil {
			return refusal("AO could not read the pull requests to check the merge.")
		}
		for i := range facts {
			f := facts[i]
			if ref != "" && prMatches(f, ref) {
				match, owner = &facts[i], o
			}
			if ref == "" && o.ID == requester.ID && !f.Merged && !f.Closed {
				open = append(open, f)
			}
		}
	}
	if ref == "" && len(open) == 1 {
		match, owner = &open[0], requester
	}
	if match == nil {
		return refusal("The merge does not match a pull request AO tracks for this run, so AO cannot check its review and checks.")
	}
	if match.Merged || match.Closed {
		return Verdict{Allow: true, Reason: "The pull request is already finished."}
	}
	runs, err := s.store.ListCurrentHeadReviewRunsForSession(ctx, owner.ID)
	if err != nil {
		return refusal("AO could not read the reviews to check the merge.")
	}
	if msg := reviewGate(*match, runs); msg != "" {
		return refusal("Merging " + prLabel(*match) + " is not allowed yet: " + msg)
	}
	if match.CI != domain.CIPassing {
		return refusal(fmt.Sprintf("Merging %s is not allowed yet: CI is %s.", prLabel(*match), ciWord(match.CI)))
	}
	return Verdict{Allow: true, Reason: "The AO review approved the exact head and checks are green."}
}

// prMatches reports whether a merge reference (number, URL or branch) names the PR.
func prMatches(f domain.PRFacts, ref string) bool {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(strings.TrimPrefix(ref, "#")); err == nil {
		return f.Number == n
	}
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		return strings.TrimRight(f.URL, "/") == strings.TrimRight(ref, "/")
	}
	return f.SourceBranch != "" && f.SourceBranch == ref
}

// pickDecision chooses the provider's allow-once or reject-once answer, falling
// back to the always variants and then to well-known ids for providers that do
// not label their decisions.
func pickDecision(options []ports.ChatDecisionOption, allow bool) (ports.ChatDecision, bool) {
	order := []ports.ChatDecisionKind{ports.ChatDecisionAllowOnce, ports.ChatDecisionAllowAlways}
	ids := []string{"allow", "accept", "approve", "yes"}
	if !allow {
		order = []ports.ChatDecisionKind{ports.ChatDecisionRejectOnce, ports.ChatDecisionRejectAlways}
		ids = []string{"reject", "deny", "decline", "cancel", "no"}
	}
	for _, kind := range order {
		for _, o := range options {
			if o.Kind == kind {
				return ports.ChatDecision{ID: o.ID}, true
			}
		}
	}
	for _, want := range ids {
		for _, o := range options {
			if strings.EqualFold(o.ID, want) {
				return ports.ChatDecision{ID: o.ID}, true
			}
		}
	}
	return ports.ChatDecision{}, false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

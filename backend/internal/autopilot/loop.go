package autopilot

import (
	"context"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// SessionTurnEnded is told by the lifecycle manager that a session went idle,
// which is the end of one turn. It never blocks the caller: the judgement runs on
// its own goroutine.
func (s *Service) SessionTurnEnded(rec domain.SessionRecord) {
	id := rec.ID
	s.background(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		s.judge(ctx, id, false)
	})
}

// RunMaintenance is the watchdog. It ends the runs of sessions that are gone,
// picks up a turn end the hook missed, sends again a message the session never
// started a turn for, and resumes a run the usage limit paused. It blocks until
// ctx is cancelled, so callers run it in a goroutine.
func (s *Service) RunMaintenance(ctx context.Context) {
	ticker := time.NewTicker(maintenanceTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maintain(ctx)
		}
	}
}

func (s *Service) maintain(ctx context.Context) {
	runs, err := s.store.ListOpenAutopilotRuns(ctx)
	if err != nil {
		s.log.Warn("autopilot: list open runs", "err", err)
		return
	}
	now := s.clock()
	for _, run := range runs {
		rec, ok, err := s.store.GetSession(ctx, run.SessionID)
		if err != nil {
			s.log.Warn("autopilot: read session", "session", run.SessionID, "err", err)
			continue
		}
		if !ok || rec.IsTerminated {
			s.mu.Lock()
			s.finishLocked(ctx, run.ID, domain.AutopilotStopped, domain.AutopilotEndSessionEnded, "Autopilot ended: the session ended")
			s.mu.Unlock()
			s.resolveParkedNotifications(ctx, run)
			continue
		}
		switch run.State {
		case domain.AutopilotPausedUsageLimit:
			if limited, err := s.limited(ctx, run.SessionID); err == nil && !limited {
				s.resumeFromLimit(ctx, run)
			}
		case domain.AutopilotRunning:
			if who, stuck := s.waitingOnPrompt(ctx, run, rec, now); stuck {
				s.parkRun(ctx, run, domain.AutopilotEndNeedsInput,
					fmt.Sprintf("Autopilot parked the run: %s is waiting on a prompt it cannot answer", sessionLabel(who)),
					"A terminal prompt, a form, or a request the permission policy has no answer for. Answer it, then continue the run.")
				continue
			}
			if rec.Activity.State != domain.ActivityIdle || now.Sub(rec.Activity.LastActivityAt) < s.idleGrace {
				continue
			}
			switch {
			case rec.Activity.LastActivityAt.After(run.LastNudgeAt):
				// A turn ended that the hook never reported.
				s.judge(ctx, run.SessionID, false)
			case now.Sub(run.LastNudgeAt) > stallAfter:
				// The last message never started a turn.
				s.judge(ctx, run.SessionID, true)
			}
		}
	}
}

// waitingOnPrompt finds a session of the run (its own, or a worker it started)
// that has sat on a prompt longer than the policy needs to answer one. Without
// this a form or a terminal prompt would stall the run with no notification,
// because Autopilot suppresses the ordinary "needs your input" one.
func (s *Service) waitingOnPrompt(ctx context.Context, run domain.AutopilotRun, rec domain.SessionRecord, now time.Time) (domain.SessionRecord, bool) {
	for _, r := range append([]domain.SessionRecord{rec}, s.workersInScope(ctx, run, rec)...) {
		if r.Activity.State.NeedsInput() && now.Sub(r.Activity.LastActivityAt) > needsInputGrace {
			return r, true
		}
	}
	return domain.SessionRecord{}, false
}

func (s *Service) limited(ctx context.Context, id domain.SessionID) (bool, error) {
	if s.usageLimited == nil {
		return false, nil
	}
	return s.usageLimited(ctx, id)
}

func (s *Service) resumeFromLimit(ctx context.Context, run domain.AutopilotRun) {
	s.mu.Lock()
	cur, ok, err := s.store.GetAutopilotRun(ctx, run.ID)
	if err != nil || !ok || cur.State != domain.AutopilotPausedUsageLimit {
		s.mu.Unlock()
		return
	}
	cur.State, cur.UpdatedAt = domain.AutopilotRunning, s.clock()
	cur, err = s.store.UpdateAutopilotRun(ctx, cur)
	if err != nil {
		s.mu.Unlock()
		s.log.Warn("autopilot: resume after usage limit", "run", run.ID, "err", err)
		return
	}
	s.logEventLocked(ctx, cur, domain.AutopilotEventResumed, "The usage limit reset: Autopilot continues", "")
	s.mu.Unlock()
	if err := s.sendIteration(ctx, cur, "Your usage limit has reset and Autopilot is continuing."); err != nil {
		s.log.Warn("autopilot: send after usage limit", "run", run.ID, "err", err)
	}
}

// judge looks at a session whose turn ended and either sends the task again,
// pauses the run, or parks it. force skips the "a turn ended since the last
// message" check, for a message that never started a turn.
func (s *Service) judge(ctx context.Context, id domain.SessionID, force bool) {
	s.mu.Lock()
	if s.judging[id] {
		s.mu.Unlock()
		return
	}
	run, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, id)
	if err != nil || !ok || run.State != domain.AutopilotRunning {
		s.mu.Unlock()
		if err != nil {
			s.log.Warn("autopilot: read run on turn end", "session", id, "err", err)
		}
		return
	}
	s.judging[id] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.judging, id)
		s.mu.Unlock()
	}()
	s.judgeRun(ctx, run, force)
}

func (s *Service) judgeRun(ctx context.Context, run domain.AutopilotRun, force bool) {
	rec, ok, err := s.store.GetSession(ctx, run.SessionID)
	if err != nil {
		s.log.Warn("autopilot: read session on turn end", "session", run.SessionID, "err", err)
		return
	}
	if !ok || rec.IsTerminated {
		s.mu.Lock()
		s.finishLocked(ctx, run.ID, domain.AutopilotStopped, domain.AutopilotEndSessionEnded, "Autopilot ended: the session ended")
		s.mu.Unlock()
		return
	}
	// Only the end of a turn is judged: a session that is working, or waiting on
	// an approval the policy answers, is not at a boundary.
	if rec.Activity.State != domain.ActivityIdle {
		return
	}
	now := s.clock()
	if !force && !run.LastNudgeAt.IsZero() && !rec.Activity.LastActivityAt.After(run.LastNudgeAt) {
		return
	}
	if !run.LastNudgeAt.IsZero() && now.Sub(run.LastNudgeAt) < s.minNudgeGap {
		return
	}
	if limited, err := s.limited(ctx, run.SessionID); err == nil && limited {
		s.pause(ctx, run)
		return
	}
	scope := s.workersInScope(ctx, run, rec)
	for _, w := range scope {
		// An orchestrator whose workers are still going is waiting on them, not
		// done with its turn: their reports start its next one.
		if w.Activity.State == domain.ActivityActive || w.Activity.State.NeedsInput() {
			return
		}
	}

	next := run
	next.Iteration++
	fp := s.fingerprint(ctx, run, rec, scope)
	if fp != "" && fp == run.Fingerprint {
		next.Stagnant++
	} else {
		next.Stagnant = 0
	}
	next.Fingerprint = fp
	switch {
	case next.Stagnant >= domain.AutopilotStuckIterations:
		s.parkRun(ctx, next, domain.AutopilotEndStuck,
			fmt.Sprintf("Autopilot parked the run: nothing changed for %d iterations in a row", domain.AutopilotStuckIterations),
			"The workspace, the pull requests and the recorded test result were the same at the end of each of the last iterations.")
		return
	case next.Iteration >= next.MaxIterations:
		s.parkRun(ctx, next, domain.AutopilotEndMaxIterations,
			fmt.Sprintf("Autopilot parked the run: it used all %d iterations", next.MaxIterations),
			"The task is still not verified done. Continue to give it more iterations, or stop it.")
		return
	}

	sent, err := s.deliverIteration(ctx, run, iterationMessage(next, next.Iteration+1))
	if err != nil {
		// Nothing is recorded, so the watchdog tries again.
		s.log.Warn("autopilot: send the next iteration", "session", run.SessionID, "err", err)
		return
	}
	if !sent {
		// The user stopped the run while this iteration was being prepared.
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok, err := s.store.GetAutopilotRun(ctx, run.ID)
	if err != nil || !ok || cur.State != domain.AutopilotRunning {
		// The user stopped the run while the message was on its way.
		return
	}
	cur.Iteration, cur.Stagnant, cur.Fingerprint = next.Iteration, next.Stagnant, next.Fingerprint
	cur.LastNudgeAt, cur.UpdatedAt = now, now
	cur, err = s.store.UpdateAutopilotRun(ctx, cur)
	if err != nil {
		s.log.Warn("autopilot: record the iteration", "run", run.ID, "err", err)
		return
	}
	summary := fmt.Sprintf("Iteration %d of %d: the task was sent again", next.Iteration+1, cur.MaxIterations)
	s.logEventLocked(ctx, cur, domain.AutopilotEventIteration, summary, cur.LastGap)
}

func (s *Service) pause(ctx context.Context, run domain.AutopilotRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok, err := s.store.GetAutopilotRun(ctx, run.ID)
	if err != nil || !ok || cur.State != domain.AutopilotRunning {
		return
	}
	cur.State, cur.UpdatedAt = domain.AutopilotPausedUsageLimit, s.clock()
	cur, err = s.store.UpdateAutopilotRun(ctx, cur)
	if err != nil {
		s.log.Warn("autopilot: pause on usage limit", "run", run.ID, "err", err)
		return
	}
	s.logEventLocked(ctx, cur, domain.AutopilotEventPaused, "Paused: the account hit a usage limit. Autopilot continues after the reset.", "")
}

// parkRun stops the loop and tells the user once. The run stays open: the user
// continues it or stops it.
func (s *Service) parkRun(ctx context.Context, next domain.AutopilotRun, reason domain.AutopilotEndReason, summary, detail string) {
	s.mu.Lock()
	cur, ok, err := s.store.GetAutopilotRun(ctx, next.ID)
	if err != nil || !ok || cur.State != domain.AutopilotRunning {
		s.mu.Unlock()
		return
	}
	cur.Iteration, cur.Stagnant, cur.Fingerprint = next.Iteration, next.Stagnant, next.Fingerprint
	cur.State, cur.EndReason, cur.UpdatedAt = domain.AutopilotParked, reason, s.clock()
	cur, err = s.store.UpdateAutopilotRun(ctx, cur)
	if err != nil {
		s.mu.Unlock()
		s.log.Warn("autopilot: park run", "run", next.ID, "err", err)
		return
	}
	ev := s.logEventLocked(ctx, cur, domain.AutopilotEventRunParked, summary, detail)
	s.mu.Unlock()
	if ev.ID == "" {
		return
	}
	if err := s.notifyParked(ctx, cur, ev); err != nil {
		s.log.Warn("autopilot: tell the user the run was parked", "run", cur.ID, "err", err)
	}
}

// notifyParked sends the one notification for a parked item or parked run. The
// source key is the log line's id, so a retry can never notify twice.
func (s *Service) notifyParked(ctx context.Context, run domain.AutopilotRun, ev domain.AutopilotEvent) error {
	if s.notifier == nil {
		return nil
	}
	name := ""
	if rec, ok, err := s.store.GetSession(ctx, run.SessionID); err == nil && ok {
		name = rec.DisplayName
	}
	detail := ev.Summary
	if ev.Detail != "" {
		detail += ": " + ev.Detail
	}
	return s.notifier.Notify(ctx, ports.NotificationIntent{
		Type:               domain.NotificationAutopilotParked,
		SessionID:          run.SessionID,
		ProjectID:          run.ProjectID,
		SessionDisplayName: name,
		Detail:             clip(detail, 240),
		SourceKey:          domain.AutopilotParkedSourceKeyPrefix + ev.ID,
		CreatedAt:          s.clock(),
	})
}

// resolveParkedNotifications closes the parked notification once nothing is left
// parked for the session: no unhandled item and no parked run.
func (s *Service) resolveParkedNotifications(ctx context.Context, run domain.AutopilotRun) {
	if s.notifier == nil {
		return
	}
	if n, err := s.store.CountOpenParkedAutopilotEventsForSession(ctx, run.SessionID); err != nil || n > 0 {
		return
	}
	if open, ok, err := s.store.GetOpenAutopilotRunForSession(ctx, run.SessionID); err == nil && ok && open.State == domain.AutopilotParked {
		return
	}
	if err := s.notifier.Resolve(ctx, ports.NotificationResolution{
		Type: domain.NotificationAutopilotParked, SessionID: run.SessionID, ResolvedAt: s.clock(),
	}); err != nil {
		s.log.Warn("autopilot: close the parked notification", "session", run.SessionID, "err", err)
	}
}

// workersInScope are the project workers an orchestrator's run covers: the ones
// created after the run began. A worker's own run covers only that worker, and a
// session has no parent link, so "started after the run" is the boundary.
func (s *Service) workersInScope(ctx context.Context, run domain.AutopilotRun, rec domain.SessionRecord) []domain.SessionRecord {
	if rec.Kind != domain.KindOrchestrator || run.ProjectID == "" {
		return nil
	}
	all, err := s.store.ListSessions(ctx, run.ProjectID)
	if err != nil {
		s.log.Warn("autopilot: list project sessions", "project", run.ProjectID, "err", err)
		return nil
	}
	var out []domain.SessionRecord
	for _, w := range all {
		if w.Kind != domain.KindWorker || w.IsTerminated || w.IsTaskPreparation || w.CreatedAt.Before(run.StartedAt) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// fingerprint hashes everything that shows the run moving: the workspaces, the
// branch and pull-request facts, and the recorded test result. An empty result
// means progress cannot be read, which never counts as stuck.
func (s *Service) fingerprint(ctx context.Context, run domain.AutopilotRun, rec domain.SessionRecord, scope []domain.SessionRecord) string {
	if s.progress == nil {
		return ""
	}
	parts := []string{run.Evidence, run.Criteria}
	for _, r := range append([]domain.SessionRecord{rec}, scope...) {
		fp, err := s.progress.Fingerprint(ctx, r)
		if err != nil {
			return ""
		}
		parts = append(parts, string(r.ID), fp)
		if r.BranchState != nil {
			parts = append(parts, fmt.Sprintf("%d/%s/%d", r.BranchState.Commits, r.BranchState.RemoteHead, r.BranchState.Unpushed))
		}
		facts, err := s.store.ListPRFactsForSession(ctx, r.ID)
		if err != nil {
			return ""
		}
		for _, f := range facts {
			parts = append(parts, fmt.Sprintf("%s|%s|%s|%s|%s|%v", f.URL, f.HeadSHA, f.CI, f.Review, f.Mergeability, f.Merged))
		}
	}
	return fingerprintOf(parts...)
}

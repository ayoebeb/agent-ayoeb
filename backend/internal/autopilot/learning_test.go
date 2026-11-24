package autopilot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type fakeAdvisor struct {
	text      string
	asked     []string
	corrects  []string
	described map[string]string
}

func (f *fakeAdvisor) Advise(_ context.Context, project, question string, _ []string) string {
	f.asked = append(f.asked, project+"|"+question)
	return f.text
}

func (f *fakeAdvisor) Describe(ids []string) []string {
	var out []string
	for _, id := range ids {
		if d, ok := f.described[id]; ok {
			out = append(out, d)
		}
	}
	return out
}

func (f *fakeAdvisor) Correct(_ context.Context, project, session, question, choice string) {
	f.corrects = append(f.corrects, project+"|"+session+"|"+question+"|"+choice)
}

func withAdvisor(e *env, a Advisor) { e.svc.advisor = a }

func TestAdviseGivesTheLearnedProfileToTheAgentOnly(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	adv := &fakeAdvisor{text: "- (all projects) Quality over cost for big features.\n- p1: Merge now? → ja"}
	withAdvisor(e, adv)
	agent := asAgent(e.ctx, orch.ID)

	got, err := e.svc.Advise(agent, orch.ID, "Big feature: cheap model or best model?", nil)
	if err != nil || !strings.Contains(got, "Quality over cost") {
		t.Fatalf("advice = %q, %v", got, err)
	}
	if len(adv.asked) != 1 || !strings.HasPrefix(adv.asked[0], "mer|") {
		t.Fatalf("the advisor must be asked in the run's project: %v", adv.asked)
	}
	if _, err := e.svc.Advise(e.ctx, orch.ID, "q", nil); !hasCode(err, "AUTOPILOT_AGENT_ONLY") {
		t.Fatalf("outside call err = %v", err)
	}
	if _, err := e.svc.Advise(agent, orch.ID, " ", nil); !hasCode(err, "AUTOPILOT_QUESTION_REQUIRED") {
		t.Fatalf("empty question err = %v", err)
	}
	// Nothing learned yet: the agent is told to say low and play safe.
	adv.text = ""
	got, _ = e.svc.Advise(agent, orch.ID, "anything", nil)
	if !strings.Contains(got, "confidence low") || !strings.Contains(got, "can be undone") {
		t.Fatalf("empty advice = %q", got)
	}
	withAdvisor(e, nil)
	if got, _ = e.svc.Advise(agent, orch.ID, "anything", nil); got != noAdvice {
		t.Fatalf("no advisor = %q", got)
	}
}

func TestDecideWithBasisRecordsWhichChoicesItFollowedAndTheConfidence(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	withAdvisor(e, &fakeAdvisor{described: map[string]string{"pair1": "Merge small fix? → ja zonder CI wachten"}})
	agent := asAgent(e.ctx, orch.ID)

	ev, err := e.svc.DecideWithBasis(agent, orch.ID, "Merge the small fix?", "Merge it", "same as before", Basis{PairIDs: []string{"pair1", "ghost"}, Confidence: ConfidenceHigh})
	if err != nil || !strings.Contains(ev.Detail, "Based on your past choice: Merge small fix? → ja zonder CI wachten") ||
		!strings.Contains(ev.Detail, "Confidence: high.") || strings.Contains(ev.Detail, "ghost") {
		t.Fatalf("decision = %+v, %v", ev, err)
	}
	low, err := e.svc.DecideWithBasis(agent, orch.ID, "Delete the old branch?", "Keep it for now", "nothing similar", Basis{Confidence: ConfidenceLow})
	if err != nil || !strings.Contains(low.Detail, "Confidence: low. Autopilot took the safest option that can be undone.") {
		t.Fatalf("low-confidence decision = %+v, %v", low, err)
	}
	if _, err := e.svc.DecideWithBasis(agent, orch.ID, "q", "c", "w", Basis{Confidence: "certain"}); !hasCode(err, "AUTOPILOT_CONFIDENCE_INVALID") {
		t.Fatalf("bad confidence err = %v", err)
	}
	if _, err := e.svc.DecideWithBasis(agent, orch.ID, "q", "c", "w", Basis{PairIDs: []string{"1", "2", "3", "4", "5", "6"}}); !hasCode(err, "AUTOPILOT_BASIS_TOO_LONG") {
		t.Fatalf("too many ids err = %v", err)
	}
	// Learning never lifts a hard limit: a choice that spends money is refused
	// however confident and however well based it is.
	if _, err := e.svc.DecideWithBasis(agent, orch.ID, "More CI minutes?", "Upgrade the paid plan", "you always say yes", Basis{PairIDs: []string{"pair1"}, Confidence: ConfidenceHigh}); !hasCode(err, "AUTOPILOT_HARD_LIMIT") {
		t.Fatalf("hard limit err = %v", err)
	}
}

func TestADeclaredWaitQuotesTheAdviceBackToTheAgent(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	withAdvisor(e, &fakeAdvisor{text: "Their profile:\n- (all projects) Workers never on Opus."})
	if handled, err := e.svc.OnWaitDeclared(e.ctx, orch.ID, "Which model for the workers?"); err != nil || !handled {
		t.Fatalf("wait = %v, %v", handled, err)
	}
	gap := e.run(orch.ID).LastGap
	if !strings.Contains(gap, "Which model for the workers?") || !strings.Contains(gap, "Workers never on Opus") {
		t.Fatalf("open question = %q", gap)
	}
	// With nothing learned the wait still works and carries no advice text.
	e2 := newEnv(t)
	o2 := e2.orchestrator()
	e2.start(o2.ID, "task")
	withAdvisor(e2, &fakeAdvisor{})
	_, _ = e2.svc.OnWaitDeclared(e2.ctx, o2.ID, "Ship it?")
	if g := e2.run(o2.ID).LastGap; strings.Contains(g, "Nothing has been learned") {
		t.Fatalf("empty advice must not be added: %q", g)
	}
}

func TestUndoIsFedBackAsACorrection(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	adv := &fakeAdvisor{}
	withAdvisor(e, adv)
	agent := asAgent(e.ctx, orch.ID)
	dec, err := e.svc.Decide(agent, orch.ID, "Merge the small fix?", "Merge without CI", "quick")
	if err != nil {
		t.Fatal(err)
	}
	if len(adv.corrects) != 0 {
		t.Fatal("a decision alone is not a correction")
	}
	if _, err := e.svc.UndoDecision(e.ctx, dec.ID); err != nil {
		t.Fatal(err)
	}
	want := "mer|" + string(orch.ID) + "|Merge the small fix?|Merge without CI"
	if len(adv.corrects) != 1 || adv.corrects[0] != want {
		t.Fatalf("corrections = %v, want [%s]", adv.corrects, want)
	}
	// Undoing again must not teach it twice.
	if _, err := e.svc.UndoDecision(e.ctx, dec.ID); err != nil || len(adv.corrects) != 1 {
		t.Fatalf("second undo: %v corrections=%d", err, len(adv.corrects))
	}
}

func TestParkedItemsAreBatchedIntoOneNotificationAndTheRunCarriesOn(t *testing.T) {
	e := newEnv(t)
	e.svc.digestEvery = 10 * time.Minute
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	agent := asAgent(e.ctx, orch.ID)

	for _, p := range []struct {
		limit domain.HardLimit
		item  string
	}{{domain.HardLimitMoney, "Upgrade the CI plan"}, {domain.HardLimitMessaging, "Email the customer"}, {domain.HardLimitAdmin, "Install the driver as admin"}} {
		if _, _, err := e.svc.Park(agent, orch.ID, p.limit, p.item, "needs a hard limit"); err != nil {
			t.Fatal(err)
		}
	}
	// Parking the same item again changes nothing.
	if _, already, _ := e.svc.Park(agent, orch.ID, domain.HardLimitMoney, "upgrade the ci plan", "again"); !already {
		t.Fatal("repeat park must be a no-op")
	}
	if e.notes.count() != 0 {
		t.Fatalf("notifications = %d, want none until the digest is due", e.notes.count())
	}
	e.now = e.now.Add(5 * time.Minute)
	e.svc.maintain(e.ctx)
	if e.notes.count() != 0 {
		t.Fatalf("notifications = %d before the interval", e.notes.count())
	}
	e.now = e.now.Add(6 * time.Minute)
	e.svc.maintain(e.ctx)
	if e.notes.count() != 1 {
		t.Fatalf("notifications = %d, want one digest", e.notes.count())
	}
	in := e.notes.intents[0]
	if !strings.Contains(in.Detail, "3 items") || !strings.Contains(in.Detail, "Upgrade the CI plan") || !strings.Contains(in.Detail, "Email the customer") ||
		in.Type != domain.NotificationAutopilotParked || !strings.Contains(in.SourceKey, "digest:") {
		t.Fatalf("digest = %+v", in)
	}
	e.svc.maintain(e.ctx)
	if e.notes.count() != 1 {
		t.Fatalf("an unchanged set must not notify again: %d", e.notes.count())
	}
	if got := e.run(orch.ID).State; got != domain.AutopilotRunning {
		t.Fatalf("state = %s: parked items must not stop the run", got)
	}
	// A new item joins the next digest, which replaces the previous one.
	_, _, _ = e.svc.Park(agent, orch.ID, domain.HardLimitCredentials, "Rotate the token", "credentials")
	e.svc.maintain(e.ctx)
	if e.notes.count() != 2 || !strings.Contains(e.notes.intents[1].Detail, "4 items") {
		t.Fatalf("second digest: count=%d", e.notes.count())
	}
}

func TestWithoutADigestIntervalEveryItemStillNotifiesAtOnce(t *testing.T) {
	e := newEnv(t)
	orch := e.orchestrator()
	e.start(orch.ID, "task")
	if _, _, err := e.svc.Park(asAgent(e.ctx, orch.ID), orch.ID, domain.HardLimitMoney, "Upgrade the plan", "money"); err != nil || e.notes.count() != 1 {
		t.Fatalf("err=%v notifications=%d", err, e.notes.count())
	}
}

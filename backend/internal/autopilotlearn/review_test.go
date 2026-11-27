package autopilotlearn

import (
	"context"
	"strings"
	"testing"
	"time"
)

// hookLLM runs a hook while the "model" is busy, the way an owner edits the
// profile during a call that takes minutes.
type hookLLM struct {
	out  string
	hook func()
	last string
}

func (h *hookLLM) Complete(_ context.Context, _, prompt string) (string, error) {
	h.last = prompt
	if h.hook != nil {
		h.hook()
	}
	return h.out, nil
}

func ruleTexts(p Profile) map[string]string {
	out := map[string]string{}
	for _, r := range p.Rules {
		out[r.Text] = r.Origin
	}
	return out
}

func TestOwnerChangesMadeDuringAnUpdateSurvive(t *testing.T) {
	st := newStore(t)
	edited, _ := st.AddRule(ScopeGlobal, "Merge small fixes quickly.", OriginLearned)
	deletedLearned, _ := st.AddRule(ScopeGlobal, "Prefer big PRs.", OriginLearned)
	deletedOwn, _ := st.AddRule(ScopeGlobal, "Work at night.", OriginOwner)
	_, _ = st.AddPairs([]Pair{pair("p", "q", "a", t0)})

	llm := &hookLLM{out: `{"rules":[{"scope":"global","text":"Merge small fixes quickly."},{"scope":"global","text":"Prefer big PRs."},{"scope":"global","text":"Work at night."},{"scope":"global","text":"Test first."}]}`}
	llm.hook = func() {
		if _, err := st.AddRule(ScopeGlobal, "Restart only with my OK.", OriginOwner); err != nil {
			t.Error(err)
		}
		if _, err := st.EditRule(edited.ID, "Merge small fixes only after CI."); err != nil {
			t.Error(err)
		}
		if err := st.DeleteRule(deletedLearned.ID); err != nil {
			t.Error(err)
		}
		if err := st.DeleteRule(deletedOwn.ID); err != nil {
			t.Error(err)
		}
	}
	if _, err := NewLearner(st, llm, func() time.Time { return t0 }).Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	got := ruleTexts(st.Profile())
	want := map[string]string{
		"Restart only with my OK.":         OriginOwner,
		"Merge small fixes only after CI.": OriginOwner,
		"Test first.":                      OriginLearned,
	}
	if len(got) != len(want) {
		t.Fatalf("rules = %v, want %v", got, want)
	}
	for text, origin := range want {
		if got[text] != origin {
			t.Fatalf("rules = %v, want %v", got, want)
		}
	}
	if len(st.Profile().Dismissed) != 2 {
		t.Fatalf("the edited and the deleted learned wording stay dismissed: %v", st.Profile().Dismissed)
	}
}

func TestAnUpdateForOneProjectKeepsTheOthersRules(t *testing.T) {
	st := newStore(t)
	llm := &hookLLM{out: `{"rules":[{"scope":"alpha","text":"Alpha: ship on Fridays."},{"scope":"global","text":"Quality first."}]}`}
	l := NewLearner(st, llm, func() time.Time { return t0 })
	_, _ = st.AddPairs([]Pair{pair("alpha", "ship alpha?", "friday", t0)})
	if _, err := l.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	// The next batch is only about beta; the model says nothing about alpha, or
	// even tries to rewrite it.
	llm.out = `{"rules":[{"scope":"beta","text":"Beta: never on Fridays."},{"scope":"global","text":"Quality first."},{"scope":"alpha","text":"Alpha: whenever."}]}`
	_, _ = st.AddPairs([]Pair{pair("beta", "ship beta?", "not friday", t0.Add(time.Minute))})
	if _, err := l.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	got := ruleTexts(st.Profile())
	if len(got) != 3 || got["Alpha: ship on Fridays."] == "" || got["Beta: never on Fridays."] == "" || got["Quality first."] == "" {
		t.Fatalf("rules = %v", got)
	}
	if strings.Contains(llm.last, "Alpha: ship on Fridays.") {
		t.Fatal("a project outside the batch is not the model's to rewrite")
	}
}

func TestRewritingTheSwitchRuleRevokesSwitching(t *testing.T) {
	st := newStore(t)
	llm := &hookLLM{out: `{"rules":[{"scope":"global","text":"Move to another account when limited.","tag":"usage_limit_switch"}]}`}
	l := NewLearner(st, llm, func() time.Time { return t0 })
	_, _ = st.AddPairs([]Pair{pair("p", "limit hit, switch?", "yes switch", t0)})
	if _, err := l.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	rule := st.Profile().Rules[0]
	// Saving it unchanged keeps the permission the owner just confirmed.
	if _, err := st.EditRule(rule.ID, rule.Text); err != nil || !st.Profile().UsageLimitSwitches() {
		t.Fatalf("an unchanged save keeps it: %v", err)
	}
	if _, err := st.EditRule(rule.ID, "Wait for the reset; never switch accounts."); err != nil {
		t.Fatal(err)
	}
	if st.Profile().UsageLimitSwitches() {
		t.Fatal("rewriting the rule must withdraw the switch permission")
	}
	// Learning cannot turn it back on while the owner's rule stands.
	llm.out = `{"rules":[{"scope":"global","text":"Switch accounts if a limit blocks work.","tag":"usage_limit_switch"}]}`
	_, _ = st.AddPairs([]Pair{pair("p", "limit again, switch?", "ok", t0.Add(time.Minute))})
	if _, err := l.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if st.Profile().UsageLimitSwitches() {
		t.Fatal("a learned rule must not override the owner's rewrite")
	}
	// Deleting their rule lifts the block.
	if err := st.DeleteRule(rule.ID); err != nil {
		t.Fatal(err)
	}
	if !st.Profile().UsageLimitSwitches() {
		t.Fatal("with the owner's rule gone the learned one applies again")
	}
}

func TestAnAnswerWithoutAValidRulesArrayLeavesTheProfileAlone(t *testing.T) {
	for name, out := range map[string]string{
		"missing":         `{}`,
		"null":            `{"rules":null}`,
		"other key":       `{"profile":[{"scope":"global","text":"x"}]}`,
		"not an array":    `{"rules":"none"}`,
		"object":          `{"rules":{}}`,
		"null entry":      `{"rules":[null]}`,
		"empty entry":     `{"rules":[{}]}`,
		"entry sans text": `{"rules":[{"scope":"global","rule":"x"}]}`,
		"strings":         `{"rules":["Test first."]}`,
		"truncated":       `{"rules":[`,
	} {
		t.Run(name, func(t *testing.T) {
			st := newStore(t)
			kept, _ := st.AddRule(ScopeGlobal, "Quality first.", OriginLearned)
			_, _ = st.AddPairs([]Pair{pair("p", "q", "a", t0)})
			l := NewLearner(st, &hookLLM{out: out}, func() time.Time { return t0 })

			if _, err := l.Update(context.Background(), true); err == nil {
				t.Fatalf("%s must be an error", out)
			}
			rules := st.Profile().Rules
			if len(rules) != 1 || rules[0].ID != kept.ID {
				t.Fatalf("profile = %+v, want it untouched", rules)
			}
			if _, unfolded := st.Counts(); unfolded != 1 {
				t.Fatalf("unfolded = %d: the decision must wait for the next update", unfolded)
			}
			if st.Profile().LastError == "" {
				t.Fatal("the failure must be visible")
			}
		})
	}
}

// An explicit empty array is a real answer: nothing is learned any more.
func TestAnEmptyRulesArrayIsAccepted(t *testing.T) {
	st := newStore(t)
	_, _ = st.AddRule(ScopeGlobal, "Quality first.", OriginLearned)
	_, _ = st.AddPairs([]Pair{pair("p", "q", "a", t0)})
	l := NewLearner(st, &hookLLM{out: `{"rules":[]}`}, func() time.Time { return t0 })
	if n, err := l.Update(context.Background(), true); err != nil || n != 1 {
		t.Fatalf("n = %d, err = %v", n, err)
	}
	if rules := st.Profile().Rules; len(rules) != 0 {
		t.Fatalf("rules = %+v", rules)
	}
}

package autopilotlearn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir(), func() time.Time { return t0 })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type fakeLLM struct {
	out   string
	err   error
	calls int
	last  string
	model string
}

func (f *fakeLLM) Complete(_ context.Context, model, prompt string) (string, error) {
	f.calls++
	f.last, f.model = prompt, model
	return f.out, f.err
}

func pair(project, q, a string, at time.Time) Pair {
	return Pair{Project: project, Question: q, Answer: a, At: at}
}

func TestStoreDedupesPersistsAndBounds(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, func() time.Time { return t0 })
	n, err := s.AddPairs([]Pair{pair("p", "Merge now?", "ja", t0), pair("p", "Merge now?", "ja", t0), pair("p", "", "x", t0), pair("p", "q", "", t0)})
	if err != nil || n != 1 {
		t.Fatalf("added %d err %v, want 1", n, err)
	}
	// A second store over the same dir sees the pair: it is on disk.
	s2, _ := Open(dir, nil)
	if got := s2.Pairs(); len(got) != 1 || got[0].Answer != "ja" {
		t.Fatalf("not persisted: %+v", got)
	}
	big := make([]Pair, 0, MaxPairs+10)
	for i := 0; i < MaxPairs+10; i++ {
		big = append(big, pair("p", "question number "+strings.Repeat("x", i%7)+string(rune('a'+i%26))+time.Duration(i).String(), "a", t0.Add(time.Duration(i)*time.Second)))
	}
	if _, err := s2.AddPairs(big); err != nil {
		t.Fatal(err)
	}
	if got := len(s2.Pairs()); got != MaxPairs {
		t.Fatalf("pairs not capped: %d", got)
	}
}

func TestStoreSurvivesACorruptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, DirName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, DirName, fileName), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, nil)
	if err != nil || len(s.Pairs()) != 0 {
		t.Fatalf("open corrupt: %v %d", err, len(s.Pairs()))
	}
}

func TestRetrievalRanksSimilarAndPrefersProjectAndCorrections(t *testing.T) {
	pairs := []Pair{
		pair("a", "Merge the small fix without waiting for CI?", "ja merge maar wel review", t0),
		pair("b", "Which model should the workers use for the big feature?", "Sonnet, never Opus", t0),
		pair("a", "Restart Ayo now to pick up the update?", "nee eerst vragen", t0),
		pair("b", "Merge the small fix without waiting for CI?", "nee wacht op CI", t0),
		{Project: "a", Source: SourceCorrection, Question: "Merge the small fix without waiting for CI?", Answer: "The owner undid this choice, so do not repeat it: merged without CI", At: t0},
	}
	got := SimilarPairs(pairs, "a", "Can I merge this small fix without CI?", nil, 3)
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].Pair.Project != "a" || !strings.Contains(got[0].Pair.Question, "Merge") {
		t.Fatalf("best match wrong: %+v", got[0].Pair)
	}
	for _, g := range got {
		if strings.Contains(g.Pair.Question, "Restart Ayo") {
			t.Fatalf("unrelated pair returned: %+v", g.Pair)
		}
	}
	if len(SimilarPairs(pairs, "a", "zzz qqq", nil, 5)) != 0 {
		t.Fatal("pairs sharing no word must not be returned")
	}
	if SimilarPairs(nil, "a", "merge", nil, 5) != nil {
		t.Fatal("empty store must return nothing")
	}
}

func TestExtractOptionsAndRecommendation(t *testing.T) {
	msg := "Hoe wil je het lek dichten?\n\n1. **Snelle fix** (Recommended)\n2. Volledige refactor\n- Niets doen\n\nJe kunt kiezen."
	opts, rec := ExtractOptions(msg)
	if len(opts) != 3 || opts[0] != "Snelle fix (Recommended)" || rec != "Snelle fix" {
		t.Fatalf("opts %q rec %q", opts, rec)
	}
	_, rec = ExtractOptions("Patch or refactor? I recommend the patch because it is one function.")
	if !strings.Contains(rec, "recommend the patch") {
		t.Fatalf("sentence recommendation not found: %q", rec)
	}
}

// transcript builds a Claude transcript of an orchestrator session.
func transcript(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

const (
	humanOrigin = `"origin":{"kind":"human"}`
)

func asst(text string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + quote(text) + `}]}}`
}
func asstWaiting(text, question string) string {
	return `{"type":"assistant","message":{"content":[{"type":"text","text":` + quote(text) + `},{"type":"tool_use","name":"Bash","input":{"command":` + quote(`ao notify-user --waiting "`+question+`"`) + `}}]}}`
}
func human(text, ts string) string {
	return `{"type":"user",` + humanOrigin + `,"timestamp":"` + ts + `","message":{"content":` + quote(text) + `}}`
}
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return `"` + r.Replace(s) + `"`
}

func TestParseTranscriptIsStrict(t *testing.T) {
	in := transcript(
		`{"type":"api-request-blob","blob":"`+strings.Repeat("x", 5000)+`"}`,
		asstWaiting("Which way?\n1. Patch (Recommended)\n2. Refactor", "Patch or refactor?"),
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`,
		human("patch it", "2026-11-18T10:00:00Z"),
		// not asked: a plain status, then the owner types a new request
		asst("Done, PR is up."),
		human("great, now do X", "2026-11-18T11:00:00Z"),
		// closing question, answered
		asst("Shall I merge now?"),
		human("ja merge", "2026-11-18T12:00:00Z"),
		// AO's injected reports arrive as human origin: never an answer
		asst("Shall I wait for the worker?"),
		human("Reports since your previous turn:\n[done] x", "2026-11-18T13:00:00Z"),
		// a long task description is not an answer
		asst("What next?"),
		human(strings.Repeat("spec ", 400), "2026-11-18T14:00:00Z"),
		// a side-chain answer and a meta record are not the owner
		asst("Restart now?"),
		`{"type":"user","isSidechain":true,`+humanOrigin+`,"message":{"content":"nee"}}`,
		`{"type":"user","isMeta":true,`+humanOrigin+`,"message":{"content":"nee"}}`,
		`{"type":"user","origin":{"kind":"peer"},"message":{"content":"nee"}}`,
		`not json at all`,
	)
	pairs, err := ParseTranscript(context.Background(), strings.NewReader(in), "proj", "sess")
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 2 {
		t.Fatalf("want 2 pairs, got %d: %+v", len(pairs), pairs)
	}
	p := pairs[0]
	if p.Question != "Patch or refactor?" || p.Answer != "patch it" || p.Project != "proj" || p.Source != SourceImport {
		t.Fatalf("pair 0: %+v", p)
	}
	if len(p.Options) != 2 || p.Recommendation != "Patch" || !p.At.Equal(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("pair 0 options/recommendation/time: %+v", p)
	}
	if pairs[1].Question != "Shall I merge now?" || pairs[1].Answer != "ja merge" {
		t.Fatalf("pair 1: %+v", pairs[1])
	}
}

func TestParseTranscriptSkipsOversizedLines(t *testing.T) {
	huge := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("y", maxImportLine+10) + `"}]}}`
	in := transcript(asst("Merge it?"), huge, human("ja", "2026-11-18T10:00:00Z"))
	pairs, err := ParseTranscript(context.Background(), strings.NewReader(in), "p", "s")
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs %d err %v", len(pairs), err)
	}
}

func TestImportScansOnlyOrchestratorFoldersAndDedupes(t *testing.T) {
	home := t.TempDir()
	good := filepath.Join(home, "projects", "C--Users-x--ao-data-worktrees-myproj-orchestrator-myproj-orchestrator")
	worker := filepath.Join(home, "projects", "C--Users-x--ao-data-worktrees-myproj-myproj-12")
	for _, d := range []string{good, worker} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	body := transcript(asst("Merge now?"), human("ja", "2026-11-18T10:00:00Z"))
	for _, f := range []string{filepath.Join(good, "s1.jsonl"), filepath.Join(worker, "s2.jsonl")} {
		if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pairs, res, err := ImportTranscripts(context.Background(), []string{filepath.Join(home, "projects"), filepath.Join(home, "projects")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 1 || len(pairs) != 1 || pairs[0].Project != "myproj" {
		t.Fatalf("res %+v pairs %+v", res, pairs)
	}
	if project, ok := ProjectFromTranscriptDir("C--Users-x--ao-data-worktrees-myproj-myproj-12"); ok || project != "" {
		t.Fatal("a worker folder must not be read")
	}
}

func TestServiceImportStoresPairsOnce(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "C--x--ao-data-worktrees-p-orchestrator-p-orchestrator")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(transcript(asst("Merge now?"), human("ja", "2026-11-18T10:00:00Z"))), 0o600)
	st := newStore(t)
	svc := NewService(ServiceDeps{Store: st, Learner: NewLearner(st, nil, nil), Roots: func() []string { return []string{home} }})
	res, err := svc.Import(context.Background())
	if err != nil || res.Added != 1 {
		t.Fatalf("first import: %+v %v", res, err)
	}
	res, err = svc.Import(context.Background())
	if err != nil || res.Added != 0 || res.Pairs != 1 {
		t.Fatalf("second import must add nothing: %+v %v", res, err)
	}
	if st.ImportedAt().IsZero() {
		t.Fatal("import time not recorded")
	}
}

func TestCaptureRecordsQuestionOptionsAndAnswer(t *testing.T) {
	st := newStore(t)
	svc := NewService(ServiceDeps{
		Store: st, Learner: NewLearner(st, nil, nil), Background: func(f func()) { f() }, Clock: func() time.Time { return t0 },
		LastAgentText: func(context.Context, string) string { return "Patch or refactor?\n1. Patch (Recommended)\n2. Refactor" },
	})
	svc.RecordAnswer(context.Background(), "proj", "orch-1", "Patch or refactor?", "patch")
	svc.RecordAnswer(context.Background(), "proj", "orch-1", "Q?", "Reports since your previous turn: x")
	svc.RecordAnswer(context.Background(), "proj", "orch-1", "Q?", "   ")
	got := st.Pairs()
	if len(got) != 1 {
		t.Fatalf("want 1 pair, got %+v", got)
	}
	p := got[0]
	if p.Source != SourceLive || p.Project != "proj" || p.Answer != "patch" || len(p.Options) != 2 || p.Recommendation != "Patch" || !p.At.Equal(t0) {
		t.Fatalf("captured pair wrong: %+v", p)
	}
}

func TestLearnerFoldsNewPairsIntoProfile(t *testing.T) {
	st := newStore(t)
	llm := &fakeLLM{out: "```json\n{\"rules\":[{\"scope\":\"global\",\"text\":\"Quality over cost for big features.\"},{\"scope\":\"proj\",\"text\":\"Small fixes: merge without waiting for CI.\"},{\"scope\":\"other\",\"text\":\"unknown project is dropped\"},{\"scope\":\"global\",\"text\":\"Move to another account when limited.\",\"tag\":\"usage_limit_switch\"}]}\n```"}
	l := NewLearner(st, llm, func() time.Time { return t0 })
	var batch []Pair
	for i := 0; i < 5; i++ {
		batch = append(batch, pair("proj", "question "+strings.Repeat("a", i+1), "answer", t0.Add(time.Duration(i)*time.Minute)))
	}
	if _, err := st.AddPairs(batch[:4]); err != nil {
		t.Fatal(err)
	}
	if n, err := l.Update(context.Background(), false); err != nil || n != 0 || llm.calls != 0 {
		t.Fatalf("4 pairs must not trigger a model call: n=%d calls=%d err=%v", n, llm.calls, err)
	}
	if _, err := st.AddPairs(batch[4:]); err != nil {
		t.Fatal(err)
	}
	n, err := l.Update(context.Background(), false)
	if err != nil || n != 5 || llm.calls != 1 {
		t.Fatalf("n=%d calls=%d err=%v", n, llm.calls, err)
	}
	if !strings.Contains(llm.model, "sonnet") || strings.Contains(strings.ToLower(llm.model), "opus") {
		t.Fatalf("model must be Sonnet, got %q", llm.model)
	}
	prof := st.Profile()
	if len(prof.Rules) != 3 {
		t.Fatalf("want 3 rules (unknown scope dropped), got %+v", prof.Rules)
	}
	if !prof.UsageLimitSwitches() {
		t.Fatal("the tagged rule must switch the usage-limit policy")
	}
	if total, unfolded := st.Counts(); total != 5 || unfolded != 0 {
		t.Fatalf("counts %d %d", total, unfolded)
	}
	// Nothing new: no further call.
	if _, err := l.Update(context.Background(), false); err != nil || llm.calls != 1 {
		t.Fatalf("calls %d err %v", llm.calls, err)
	}
}

func TestLearnerKeepsOwnerRulesAndDismissedStayGone(t *testing.T) {
	st := newStore(t)
	owner, _ := st.AddRule(ScopeGlobal, "Restart Ayo only with my OK.", OriginOwner)
	learned, _ := st.AddRule(ScopeGlobal, "Workers never on Opus.", OriginLearned)
	if err := st.DeleteRule(learned.ID); err != nil {
		t.Fatal(err)
	}
	llm := &fakeLLM{out: `{"rules":[{"scope":"global","text":"Workers never on Opus."},{"scope":"global","text":"restart ayo only with my ok."},{"scope":"global","text":"Prefer small PRs."}]}`}
	l := NewLearner(st, llm, func() time.Time { return t0 })
	_, _ = st.AddPairs([]Pair{pair("p", "q one", "a", t0)})
	if _, err := l.Update(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(llm.last, "Restart Ayo only with my OK.") || !strings.Contains(llm.last, "Workers never on Opus.") {
		t.Fatal("prompt must carry the pinned and the dismissed rules")
	}
	rules := st.Profile().Rules
	if len(rules) != 2 || rules[0].ID != owner.ID || rules[1].Text != "Prefer small PRs." {
		t.Fatalf("rules: %+v", rules)
	}
}

func TestLearnerFailureKeepsPairsPendingAndBacksOff(t *testing.T) {
	st := newStore(t)
	now := t0
	llm := &fakeLLM{err: errors.New("claude not found")}
	l := NewLearner(st, llm, func() time.Time { return now })
	for i := 0; i < 5; i++ {
		_, _ = st.AddPairs([]Pair{pair("p", "question "+strings.Repeat("z", i+1), "a", t0)})
	}
	if _, err := l.Update(context.Background(), false); err == nil {
		t.Fatal("expected the model error")
	}
	if _, unfolded := st.Counts(); unfolded != 5 {
		t.Fatalf("pairs must stay pending, unfolded=%d", unfolded)
	}
	if st.Profile().LastError == "" {
		t.Fatal("the failure must be visible")
	}
	if _, err := l.Update(context.Background(), false); err != nil || llm.calls != 1 {
		t.Fatalf("must back off after a failure: calls=%d err=%v", llm.calls, err)
	}
	now = now.Add(retryAfter + time.Second)
	llm.err, llm.out = nil, `{"rules":[]}`
	if n, err := l.Update(context.Background(), false); err != nil || n != 5 {
		t.Fatalf("retry after backoff: n=%d err=%v", n, err)
	}
	// Garbage output is an error, not a silent wipe of the profile.
	_, _ = st.AddPairs([]Pair{pair("p", "another different question", "a", t0)})
	llm.out = "I think the owner likes tests."
	if _, err := l.Update(context.Background(), true); err == nil {
		t.Fatal("non-JSON output must be rejected")
	}
}

func TestClaudeCLIRefusesNonSonnet(t *testing.T) {
	if _, err := (ClaudeCLI{}).Complete(context.Background(), "claude-opus-5-5", "hi"); err == nil || !strings.Contains(err.Error(), "only Sonnet") {
		t.Fatalf("opus must be refused: %v", err)
	}
}

func TestCorrectionIsFoldedAndRetrieved(t *testing.T) {
	st := newStore(t)
	llm := &fakeLLM{out: `{"rules":[{"scope":"global","text":"Do not merge small fixes without CI."}]}`}
	l := NewLearner(st, llm, func() time.Time { return t0 })
	l.SetBackground(func(f func()) { f() })
	svc := NewService(ServiceDeps{Store: st, Learner: l, Clock: func() time.Time { return t0 }})
	svc.RecordCorrection(context.Background(), "p", "orch", "Merge the small fix without CI?", "Merge without CI")
	if len(st.Pairs()) != 1 || st.Pairs()[0].Source != SourceCorrection {
		t.Fatalf("correction not stored: %+v", st.Pairs())
	}
	if llm.calls != 1 || !strings.Contains(llm.last, `"source": "correction"`) {
		t.Fatalf("a correction must reach the profile at once: calls=%d", llm.calls)
	}
	if len(st.Profile().Rules) != 1 {
		t.Fatalf("profile: %+v", st.Profile())
	}
	adv := svc.Advise("p", "Merge this small fix without CI?", nil)
	if !strings.Contains(adv.Text, "Do not merge small fixes") || !strings.Contains(adv.Text, "undid this choice") || !strings.Contains(adv.Text, "hard limits always win") {
		t.Fatalf("advice: %q", adv.Text)
	}
}

func TestAdviceEmptyWhenNothingLearnedAndDescribe(t *testing.T) {
	st := newStore(t)
	svc := NewService(ServiceDeps{Store: st, Learner: NewLearner(st, nil, nil)})
	if got := svc.Advise("p", "Merge now?", nil); got.Text != "" {
		t.Fatalf("no profile, no pairs: advice must be empty, got %q", got.Text)
	}
	_, _ = st.AddPairs([]Pair{pair("p", "Merge now?", "ja", t0)})
	id := st.Pairs()[0].ID
	if d := svc.Describe([]string{id, "nope"}); len(d) != 1 || !strings.Contains(d[0], "Merge now?") {
		t.Fatalf("describe: %v", d)
	}
	if svc.UsageLimitSwitches() {
		t.Fatal("default must be to wait")
	}
}

func TestEditAndDeleteRules(t *testing.T) {
	st := newStore(t)
	r, _ := st.AddRule(ScopeGlobal, "Quality over cost.", OriginLearned)
	edited, err := st.EditRule(r.ID, "Quality over cost, always.")
	if err != nil || edited.Origin != OriginOwner {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	if err := st.DeleteRule(r.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRule(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	// Only the learned wording the owner replaced is dismissed; deleting their own
	// rule adds nothing.
	if d := st.Profile().Dismissed; len(d) != 1 || d[0] != "Quality over cost." {
		t.Fatalf("dismissed = %v", d)
	}
	if _, err := st.AddRule(ScopeGlobal, "  ", ""); err == nil {
		t.Fatal("empty rule must be refused")
	}
}

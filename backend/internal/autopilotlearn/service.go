package autopilotlearn

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// adviceSimilar is how many past decisions Autopilot is shown for one question.
const adviceSimilar = 5

// Service is what the rest of AO talks to: capture, import, advice, and the
// owner's edits.
type Service struct {
	store   *Store
	learner *Learner
	// roots returns the transcript folders the import reads.
	roots func() []string
	// lastAgentText returns the orchestrator's latest message, where a question's
	// options and recommendation are taken from. Optional.
	lastAgentText func(ctx context.Context, session string) string
	background    func(func())
	clock         func() time.Time

	importMu sync.Mutex
}

// ServiceDeps configures a Service.
type ServiceDeps struct {
	Store         *Store
	Learner       *Learner
	Roots         func() []string
	LastAgentText func(ctx context.Context, session string) string
	Background    func(func())
	Clock         func() time.Time
}

// NewService builds the service.
func NewService(d ServiceDeps) *Service {
	s := &Service{store: d.Store, learner: d.Learner, roots: d.Roots, lastAgentText: d.LastAgentText, background: d.Background, clock: d.Clock}
	if s.background == nil {
		s.background = func(f func()) { go f() }
	}
	if s.clock == nil {
		s.clock = func() time.Time { return time.Now().UTC() }
	}
	return s
}

// RecordAnswer stores a decision the owner made in normal mode: the question an
// orchestrator declared (`ao notify-user --waiting`) and the owner's reply. The
// options and the recommendation are read from the orchestrator's latest message.
// It never blocks the caller and never fails it.
func (s *Service) RecordAnswer(ctx context.Context, project, session, question, answer string) {
	if s == nil {
		return
	}
	if _, ok := humanTextOf(answer); !ok {
		return
	}
	at := s.clock()
	s.background(func() {
		p := Pair{Project: project, Session: session, At: at, Source: SourceLive, Question: question, Answer: answer}
		if s.lastAgentText != nil {
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			p.Context = s.lastAgentText(bg, session)
			p.Options, p.Recommendation = ExtractOptions(p.Context)
		}
		if n, err := s.store.AddPairs([]Pair{p}); err == nil && n > 0 {
			s.learner.Poke()
		}
	})
}

// RecordCorrection stores that the owner undid a choice Autopilot made: a strong
// signal against that choice. It is folded into the profile soon after.
func (s *Service) RecordCorrection(_ context.Context, project, session, question, choice string) {
	if s == nil {
		return
	}
	p := Pair{
		Project: project, Session: session, At: s.clock(), Source: SourceCorrection, Question: question,
		Answer: "The owner undid this choice, so do not repeat it: " + choice,
	}
	if n, err := s.store.AddPairs([]Pair{p}); err == nil && n > 0 {
		s.learner.Poke()
	}
}

// Import reads past orchestrator transcripts once and stores the pairs found,
// then updates the profile from them. Running it again adds only what is new.
func (s *Service) Import(ctx context.Context) (ImportResult, error) {
	s.importMu.Lock()
	defer s.importMu.Unlock()
	var roots []string
	if s.roots != nil {
		roots = s.roots()
	}
	pairs, res, err := ImportTranscripts(ctx, roots)
	if err != nil {
		return res, err
	}
	added, err := s.store.AddPairs(pairs)
	if err != nil {
		return res, err
	}
	res.Added = added
	if err := s.store.MarkImported(); err != nil {
		return res, err
	}
	if added > 0 {
		s.learner.Poke()
	}
	return res, nil
}

// Advice is what Autopilot is given to decide a question the way the owner would.
type Advice struct {
	Text    string
	Similar []Similar
}

// Advise returns the profile (global rules and the project's) and the few most
// similar past decisions for a question. Text is empty when nothing has been
// learned yet.
func (s *Service) Advise(project, question string, options []string) Advice {
	if s == nil {
		return Advice{}
	}
	profile := s.store.Profile()
	similar := SimilarPairs(s.store.Pairs(), project, question, options, adviceSimilar)
	return Advice{Text: AdviceText(profile, project, similar), Similar: similar}
}

// AdviceText renders advice for the agent.
func AdviceText(p Profile, project string, similar []Similar) string {
	var b strings.Builder
	for _, r := range p.Rules {
		if r.Scope == ScopeGlobal || r.Scope == project {
			label := "all projects"
			if r.Scope != ScopeGlobal {
				label = "this project"
			}
			fmt.Fprintf(&b, "- (%s) %s\n", label, r.Text)
		}
	}
	profile := b.String()
	if profile == "" && len(similar) == 0 {
		return ""
	}
	b.Reset()
	b.WriteString("What the owner would choose, learned from their own past decisions. This is advice about preference only; the hard limits always win and nothing here permits crossing one.\n")
	if profile != "" {
		b.WriteString("Their profile:\n" + profile)
	}
	if len(similar) > 0 {
		b.WriteString("Most similar past decisions (id: question → their answer):\n")
		for _, s := range similar {
			fmt.Fprintf(&b, "- %s: %s → %s\n", s.Pair.ID, clip(s.Pair.Question, 160), clip(s.Pair.Answer, 160))
		}
	}
	b.WriteString("Pick what they would pick. Record it with `ao autopilot decide ... --based-on <ids of the past decisions you used> --confidence high|medium|low`. If you are unsure (nothing similar, or past choices disagree), say low and pick the safest option that can be undone.")
	return b.String()
}

// Describe returns short readable lines for the pair ids a decision was based
// on; unknown ids are dropped.
func (s *Service) Describe(ids []string) []string {
	if s == nil {
		return nil
	}
	byID := map[string]Pair{}
	for _, p := range s.store.Pairs() {
		byID[p.ID] = p
	}
	var out []string
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			out = append(out, fmt.Sprintf("%s → %s", clip(p.Question, 90), clip(p.Answer, 90)))
		}
	}
	return out
}

// UsageLimitSwitches reports whether the profile clearly allows moving work to
// another account on a usage limit; false is the default.
func (s *Service) UsageLimitSwitches() bool {
	if s == nil {
		return false
	}
	return s.store.Profile().UsageLimitSwitches()
}

// View is everything the "What Autopilot learned" screen shows.
type View struct {
	Rules      []Rule
	Pairs      []Pair
	Total      int
	Unfolded   int
	ImportedAt time.Time
	LearnedAt  time.Time
	LastError  string
	// CanLearn reports whether a model is configured to rewrite the profile.
	CanLearn bool
	// Learning is true while a profile update is running.
	Learning bool
}

// View returns the profile and the pairs, newest pairs first.
func (s *Service) View() View {
	profile := s.store.Profile()
	pairs := s.store.Pairs()
	total, unfolded := s.store.Counts()
	for i, j := 0, len(pairs)-1; i < j; i, j = i+1, j-1 {
		pairs[i], pairs[j] = pairs[j], pairs[i]
	}
	return View{
		Rules: profile.Rules, Pairs: pairs, Total: total, Unfolded: unfolded,
		ImportedAt: s.store.ImportedAt(), LearnedAt: profile.LastLearnedAt, LastError: profile.LastError,
		CanLearn: s.learner != nil && s.learner.llm != nil,
		Learning: s.learner != nil && s.learner.busy.Load(),
	}
}

// Relearn updates the profile now, from every pair not yet folded in.
func (s *Service) Relearn(ctx context.Context) (int, error) { return s.learner.Update(ctx, true) }

// RelearnAsync starts the same update in the background: a Sonnet call can take
// minutes, longer than a request should wait. It reports false when an update is
// already running.
func (s *Service) RelearnAsync() bool {
	if s.learner == nil || s.learner.llm == nil || s.learner.busy.Load() {
		return false
	}
	s.learner.busy.Store(true)
	s.background(func() {
		defer s.learner.busy.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 3*callTimeout)
		defer cancel()
		_, _ = s.learner.Update(ctx, true)
	})
	return true
}

// AddRule adds a rule the owner wrote.
func (s *Service) AddRule(scope, text string) (Rule, error) {
	return s.store.AddRule(scope, text, OriginOwner)
}

// EditRule changes a rule's text; it becomes the owner's own.
func (s *Service) EditRule(id, text string) (Rule, error) { return s.store.EditRule(id, text) }

// DeleteRule removes a rule.
func (s *Service) DeleteRule(id string) error { return s.store.DeleteRule(id) }

// DeletePair removes a stored decision.
func (s *Service) DeletePair(id string) error { return s.store.DeletePair(id) }

// humanTextOf applies the same test the import uses to a live reply.
func humanTextOf(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	for _, p := range automationPrefixes {
		if strings.HasPrefix(text, p) {
			return "", false
		}
	}
	return text, true
}

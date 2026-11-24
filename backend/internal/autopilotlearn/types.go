// Package autopilotlearn teaches Autopilot how the owner decides.
//
// It keeps three things, all local under the data dir and never sent anywhere
// except the one Sonnet call that rewrites the profile:
//
//   - pairs: an orchestrator's question (with the options and recommendation it
//     offered) and the answer the owner gave in normal mode, captured live and
//     imported once from past orchestrator transcripts;
//   - a short, human-readable decision profile (global rules and per-project
//     rules) that a Sonnet call folds the new pairs into;
//   - retrieval: the few most similar past decisions for a new question.
//
// Learning only ever advises. The hard limits stay enforced by the autopilot
// package and are never read from, or weakened by, anything stored here.
package autopilotlearn

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// Where a pair came from.
const (
	SourceLive       = "live"
	SourceImport     = "import"
	SourceCorrection = "correction"
)

// Where a profile rule came from.
const (
	OriginLearned    = "learned"
	OriginOwner      = "owner"
	OriginCorrection = "correction"
)

// ScopeGlobal is the scope of a rule that holds for every project.
const ScopeGlobal = "global"

// TagUsageLimitSwitch marks the one rule that lets Autopilot move work to another
// account when a usage limit stops it. Without such a rule it waits for the reset.
const TagUsageLimitSwitch = "usage_limit_switch"

// Bounds. The store is meant to stay small: it is read whole on every change.
const (
	MaxPairs         = 3000
	MaxQuestionRunes = 600
	MaxContextRunes  = 1500
	MaxAnswerRunes   = 1500
	MaxOptionRunes   = 200
	MaxOptions       = 8
	MaxRuleRunes     = 220
	MaxRulesGlobal   = 14
	MaxRulesProject  = 8
)

// Pair is one decision the owner made: what an orchestrator asked, and the
// answer. A correction is a pair too: the owner undid a choice Autopilot made.
type Pair struct {
	ID      string    `json:"id"`
	Project string    `json:"project"`
	Session string    `json:"session,omitempty"`
	At      time.Time `json:"at"`
	Source  string    `json:"source"`
	// Question is the one-line question (the `ao notify-user --waiting` text).
	Question string `json:"question"`
	// Context is the orchestrator's message the question came from, clipped. The
	// options and the recommendation were taken from it.
	Context        string   `json:"context,omitempty"`
	Options        []string `json:"options,omitempty"`
	Recommendation string   `json:"recommendation,omitempty"`
	// Answer is the owner's next message to that orchestrator.
	Answer string `json:"answer"`
	// Folded reports that the profile already took this pair into account.
	Folded bool `json:"folded,omitempty"`
}

// Rule is one line of the decision profile.
type Rule struct {
	ID string `json:"id"`
	// Scope is "global" or a project id.
	Scope     string    `json:"scope"`
	Text      string    `json:"text"`
	Origin    string    `json:"origin"`
	Tag       string    `json:"tag,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Profile is the decision profile: short rules in the owner's own terms.
type Profile struct {
	Rules []Rule `json:"rules"`
	// Dismissed are rule texts the owner deleted; the learner never restates them.
	Dismissed     []string  `json:"dismissed,omitempty"`
	UpdatedAt     time.Time `json:"updatedAt"`
	LastLearnedAt time.Time `json:"lastLearnedAt"`
	// LastError is why the last profile update failed, if it did.
	LastError string `json:"lastError,omitempty"`
}

// UsageLimitSwitches reports whether the profile clearly allows moving work to
// another account on a usage limit. The default, and every doubt, is to wait.
func (p Profile) UsageLimitSwitches() bool {
	for _, r := range p.Rules {
		if r.Tag == TagUsageLimitSwitch && r.Scope == ScopeGlobal {
			return true
		}
	}
	return false
}

// PairID is the stable id of a pair, so the same decision captured live and
// found again in a transcript is stored once.
func PairID(project, question, answer string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(project) + "\x00" + normalizeSpace(question) + "\x00" + normalizeSpace(answer)))
	return hex.EncodeToString(sum[:8])
}

func normalizeSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip shortens s to at most n runes, on a rune boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// Clean bounds a pair's fields and fills its id. It reports false when the pair
// has no question or no answer.
func (p Pair) Clean() (Pair, bool) {
	p.Project = strings.TrimSpace(p.Project)
	p.Question = clip(p.Question, MaxQuestionRunes)
	p.Answer = clip(p.Answer, MaxAnswerRunes)
	p.Context = clip(p.Context, MaxContextRunes)
	p.Recommendation = clip(p.Recommendation, MaxOptionRunes)
	if p.Question == "" || p.Answer == "" {
		return Pair{}, false
	}
	opts := make([]string, 0, len(p.Options))
	for _, o := range p.Options {
		if o = clip(o, MaxOptionRunes); o != "" && len(opts) < MaxOptions {
			opts = append(opts, o)
		}
	}
	p.Options = opts
	if p.Source == "" {
		p.Source = SourceLive
	}
	if p.ID == "" {
		p.ID = PairID(p.Project, p.Question, p.Answer)
	}
	return p, true
}

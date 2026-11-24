package controllers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/autopilot"
	"github.com/aoagents/agent-orchestrator/backend/internal/autopilotlearn"
	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// AutopilotLearningService is what the "What Autopilot learned" routes use.
type AutopilotLearningService interface {
	View() autopilotlearn.View
	Import(ctx context.Context) (autopilotlearn.ImportResult, error)
	RelearnAsync() bool
	AddRule(scope, text string) (autopilotlearn.Rule, error)
	EditRule(id, text string) (autopilotlearn.Rule, error)
	DeleteRule(id string) error
	DeletePair(id string) error
}

// autopilotAdvisorService is the part of the Autopilot service that gives an
// agent the owner's learned preferences and records a decision's basis. The
// controller reaches it by assertion so a service without it still works.
type autopilotAdvisorService interface {
	Advise(ctx context.Context, id domain.SessionID, question string, options []string) (string, error)
	DecideWithBasis(ctx context.Context, id domain.SessionID, question, choice, why string, basis autopilot.Basis) (domain.AutopilotEvent, error)
}

// AutopilotLearningController serves what Autopilot has learned about how the
// owner decides. Only the user reads or edits it: a call from an agent's shell is
// refused, so an agent cannot rewrite its own guidance.
type AutopilotLearningController struct {
	Svc AutopilotLearningService
}

// Register mounts the routes.
func (c *AutopilotLearningController) Register(r chi.Router) {
	r.Get("/autopilot/learning", c.view)
	r.Post("/autopilot/learning/import", c.importHistory)
	r.Post("/autopilot/learning/relearn", c.relearn)
	r.Post("/autopilot/learning/rules", c.addRule)
	r.Patch("/autopilot/learning/rules/{ruleId}", c.editRule)
	r.Delete("/autopilot/learning/rules/{ruleId}", c.deleteRule)
	r.Delete("/autopilot/learning/pairs/{pairId}", c.deletePair)
}

// maxLearningPairsShown bounds the decisions one response carries.
const maxLearningPairsShown = 300

// AutopilotRuleIDParam is the path parameter of a profile rule.
type AutopilotRuleIDParam struct {
	RuleID string `path:"ruleId" description:"Profile rule identifier, e.g. rule_1a2b3c4d."`
}

// AutopilotPairIDParam is the path parameter of a stored decision.
type AutopilotPairIDParam struct {
	PairID string `path:"pairId" description:"Stored decision identifier."`
}

// AutopilotLearningRuleResponse is one line of the decision profile.
type AutopilotLearningRuleResponse struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope" description:"\"global\", or the project the rule is for."`
	Text      string    `json:"text"`
	Origin    string    `json:"origin" enum:"learned,owner,correction" description:"learned: Autopilot wrote it from your decisions. owner: you wrote or edited it; learning never rewrites it."`
	Tag       string    `json:"tag,omitempty" description:"usage_limit_switch marks the rule that lets Autopilot move work to another account on a usage limit."`
	UpdatedAt time.Time `json:"updatedAt"`
}

// AutopilotLearningPairResponse is one decision the owner made.
type AutopilotLearningPairResponse struct {
	ID             string    `json:"id"`
	Project        string    `json:"project"`
	At             time.Time `json:"at"`
	Source         string    `json:"source" enum:"live,import,correction" description:"live: captured as you answered. import: found in a past conversation. correction: you undid a choice Autopilot made."`
	Question       string    `json:"question"`
	Options        []string  `json:"options"`
	Recommendation string    `json:"recommendation"`
	Answer         string    `json:"answer"`
	Folded         bool      `json:"folded" description:"True once the profile took this decision into account."`
}

// AutopilotLearningResponse is the whole "What Autopilot learned" view.
type AutopilotLearningResponse struct {
	Rules         []AutopilotLearningRuleResponse `json:"rules"`
	Pairs         []AutopilotLearningPairResponse `json:"pairs" description:"The newest decisions first, at most 300."`
	TotalPairs    int                             `json:"totalPairs"`
	UnfoldedPairs int                             `json:"unfoldedPairs" description:"Decisions the profile has not taken into account yet."`
	ImportedAt    *time.Time                      `json:"importedAt,omitempty" description:"When the history import last ran."`
	LearnedAt     *time.Time                      `json:"learnedAt,omitempty" description:"When the profile was last rewritten."`
	LastError     string                          `json:"lastError" description:"Why the last profile update failed; empty when it did not."`
	CanLearn      bool                            `json:"canLearn" description:"False when no model can be reached to rewrite the profile."`
	Learning      bool                            `json:"learning" description:"True while a profile update is running."`
}

// AutopilotLearningImportResponse says what the history import found.
type AutopilotLearningImportResponse struct {
	Files   int `json:"files" description:"Orchestrator transcripts read."`
	Pairs   int `json:"pairs" description:"Question and answer pairs found."`
	Added   int `json:"added" description:"Pairs that were new."`
	Skipped int `json:"skipped" description:"Transcripts skipped (too large or unreadable)."`
}

// AutopilotLearningRelearnResponse is the answer to a profile update request.
type AutopilotLearningRelearnResponse struct {
	Started bool `json:"started" description:"False when no model is available or an update is already running."`
}

// AutopilotLearningRuleRequest is the body of POST /api/v1/autopilot/learning/rules.
type AutopilotLearningRuleRequest struct {
	Scope string `json:"scope" description:"\"global\" or a project id; empty means global."`
	Text  string `json:"text"`
}

// AutopilotLearningRuleEditRequest is the body of PATCH /api/v1/autopilot/learning/rules/{ruleId}.
type AutopilotLearningRuleEditRequest struct {
	Text string `json:"text"`
}

// AutopilotAdviceRequest is the body of POST /api/v1/sessions/{sessionId}/autopilot/advice.
type AutopilotAdviceRequest struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// AutopilotAdviceResponse is what the owner would choose, for the agent.
type AutopilotAdviceResponse struct {
	Advice string `json:"advice"`
}

func (c *AutopilotLearningController) ready(w http.ResponseWriter, r *http.Request, method, path string) bool {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, method, path)
		return false
	}
	if caller.From(r.Context()) != "" {
		envelope.WriteError(w, r, apierr.Forbidden("AUTOPILOT_USER_ONLY",
			"Only the user reads or edits what Autopilot learned; an agent gets advice with `ao autopilot advise`"))
		return false
	}
	return true
}

func (c *AutopilotLearningController) view(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "GET", "/api/v1/autopilot/learning") {
		return
	}
	envelope.WriteJSON(w, http.StatusOK, learningResponse(c.Svc.View()))
}

func (c *AutopilotLearningController) importHistory(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/autopilot/learning/import") {
		return
	}
	res, err := c.Svc.Import(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutopilotLearningImportResponse{Files: res.Files, Pairs: res.Pairs, Added: res.Added, Skipped: res.Skipped})
}

func (c *AutopilotLearningController) relearn(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/autopilot/learning/relearn") {
		return
	}
	envelope.WriteJSON(w, http.StatusOK, AutopilotLearningRelearnResponse{Started: c.Svc.RelearnAsync()})
}

func (c *AutopilotLearningController) addRule(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "POST", "/api/v1/autopilot/learning/rules") {
		return
	}
	var req AutopilotLearningRuleRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	rule, err := c.Svc.AddRule(req.Scope, req.Text)
	if err != nil {
		writeLearningError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, learningRule(rule))
}

func (c *AutopilotLearningController) editRule(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "PATCH", "/api/v1/autopilot/learning/rules/{ruleId}") {
		return
	}
	var req AutopilotLearningRuleEditRequest
	if !decodeConversationBody(w, r, &req) {
		return
	}
	rule, err := c.Svc.EditRule(chi.URLParam(r, "ruleId"), req.Text)
	if err != nil {
		writeLearningError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, learningRule(rule))
}

func (c *AutopilotLearningController) deleteRule(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "DELETE", "/api/v1/autopilot/learning/rules/{ruleId}") {
		return
	}
	if err := c.Svc.DeleteRule(chi.URLParam(r, "ruleId")); err != nil {
		writeLearningError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *AutopilotLearningController) deletePair(w http.ResponseWriter, r *http.Request) {
	if !c.ready(w, r, "DELETE", "/api/v1/autopilot/learning/pairs/{pairId}") {
		return
	}
	if err := c.Svc.DeletePair(chi.URLParam(r, "pairId")); err != nil {
		writeLearningError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeLearningError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, autopilotlearn.ErrNotFound) {
		envelope.WriteError(w, r, apierr.NotFound("AUTOPILOT_LEARNING_NOT_FOUND", "Unknown rule or decision"))
		return
	}
	if strings.Contains(err.Error(), "needs text") {
		envelope.WriteError(w, r, apierr.Invalid("AUTOPILOT_RULE_EMPTY", "A rule needs text", nil))
		return
	}
	envelope.WriteError(w, r, err)
}

func learningRule(r autopilotlearn.Rule) AutopilotLearningRuleResponse {
	return AutopilotLearningRuleResponse{ID: r.ID, Scope: r.Scope, Text: r.Text, Origin: r.Origin, Tag: r.Tag, UpdatedAt: r.UpdatedAt}
}

func learningResponse(v autopilotlearn.View) AutopilotLearningResponse {
	out := AutopilotLearningResponse{
		Rules: make([]AutopilotLearningRuleResponse, 0, len(v.Rules)), Pairs: make([]AutopilotLearningPairResponse, 0, len(v.Pairs)),
		TotalPairs: v.Total, UnfoldedPairs: v.Unfolded, ImportedAt: timePtr(v.ImportedAt), LearnedAt: timePtr(v.LearnedAt),
		LastError: v.LastError, CanLearn: v.CanLearn, Learning: v.Learning,
	}
	for _, r := range v.Rules {
		out.Rules = append(out.Rules, learningRule(r))
	}
	for i, p := range v.Pairs {
		if i >= maxLearningPairsShown {
			break
		}
		out.Pairs = append(out.Pairs, AutopilotLearningPairResponse{
			ID: p.ID, Project: p.Project, At: p.At, Source: p.Source, Question: p.Question, Options: nonNil(p.Options),
			Recommendation: p.Recommendation, Answer: p.Answer, Folded: p.Folded,
		})
	}
	return out
}

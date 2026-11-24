package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/autopilot"
	"github.com/aoagents/agent-orchestrator/backend/internal/autopilotlearn"
	"github.com/aoagents/agent-orchestrator/backend/internal/caller"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

func learningFixture(t *testing.T) (http.Handler, *autopilotlearn.Service) {
	t.Helper()
	st, err := autopilotlearn.Open(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := autopilotlearn.NewService(autopilotlearn.ServiceDeps{Store: st, Learner: autopilotlearn.NewLearner(st, nil, nil)})
	r := chi.NewRouter()
	r.Use(caller.Middleware)
	(&AutopilotLearningController{Svc: svc}).Register(r)
	return r, svc
}

func TestLearningViewAddEditDeleteRules(t *testing.T) {
	h, _ := learningFixture(t)
	w := doJSON(h, http.MethodPost, "/autopilot/learning/rules", `{"scope":"global","text":"Restart only with my OK."}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add status = %d: %s", w.Code, w.Body.String())
	}
	var rule AutopilotLearningRuleResponse
	if err := json.Unmarshal(w.Body.Bytes(), &rule); err != nil || rule.Origin != "owner" || rule.ID == "" {
		t.Fatalf("rule = %+v, %v", rule, err)
	}
	if w := doJSON(h, http.MethodPatch, "/autopilot/learning/rules/"+rule.ID, `{"text":"Restart only with my explicit OK."}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "explicit OK") {
		t.Fatalf("edit = %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(h, http.MethodPatch, "/autopilot/learning/rules/nope", `{"text":"x"}`); w.Code != http.StatusNotFound {
		t.Fatalf("edit unknown = %d", w.Code)
	}
	if w := doJSON(h, http.MethodPost, "/autopilot/learning/rules", `{"text":"  "}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty rule = %d: %s", w.Code, w.Body.String())
	}
	var view AutopilotLearningResponse
	w = doJSON(h, http.MethodGet, "/autopilot/learning", "")
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || len(view.Rules) != 1 || view.CanLearn {
		t.Fatalf("view = %+v, %v (no model is configured in this fixture)", view, err)
	}
	if w := doJSON(h, http.MethodDelete, "/autopilot/learning/rules/"+rule.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}
	if w := doJSON(h, http.MethodDelete, "/autopilot/learning/rules/"+rule.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d", w.Code)
	}
}

func TestLearningDeletePairAndRelearnWithoutAModel(t *testing.T) {
	h, svc := learningFixture(t)
	svc.RecordCorrection(context.Background(), "mer", "mer-orchestrator-1", "Merge now?", "Merge without CI")
	var view AutopilotLearningResponse
	_ = json.Unmarshal(doJSON(h, http.MethodGet, "/autopilot/learning", "").Body.Bytes(), &view)
	if len(view.Pairs) != 1 || view.Pairs[0].Source != "correction" || view.TotalPairs != 1 || view.UnfoldedPairs != 1 {
		t.Fatalf("view = %+v", view)
	}
	if w := doJSON(h, http.MethodPost, "/autopilot/learning/relearn", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"started":false`) {
		t.Fatalf("relearn without a model = %d %s", w.Code, w.Body.String())
	}
	if w := doJSON(h, http.MethodDelete, "/autopilot/learning/pairs/"+view.Pairs[0].ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete pair = %d", w.Code)
	}
	if w := doJSON(h, http.MethodPost, "/autopilot/learning/import", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"added":0`) {
		t.Fatalf("import = %d %s", w.Code, w.Body.String())
	}
}

// An agent must not read or rewrite the guidance it is given.
func TestLearningRoutesRefuseAnAgent(t *testing.T) {
	h, _ := learningFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/autopilot/learning", ""},
		{http.MethodPost, "/autopilot/learning/rules", `{"text":"Always merge."}`},
		{http.MethodPost, "/autopilot/learning/import", ""},
		{http.MethodDelete, "/autopilot/learning/pairs/x", ""},
	} {
		w := doJSON(h, c.method, c.path, c.body, caller.Header, "mer-orchestrator-1")
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s from an agent = %d, want 403", c.method, c.path, w.Code)
		}
	}
}

type fakeAdvisorAutopilot struct {
	fakeAutopilot
	asked []string
	basis []autopilot.Basis
}

func (f *fakeAdvisorAutopilot) Advise(_ context.Context, _ domain.SessionID, question string, _ []string) (string, error) {
	f.asked = append(f.asked, question)
	return "Their profile: quality over cost.", nil
}

func (f *fakeAdvisorAutopilot) DecideWithBasis(_ context.Context, _ domain.SessionID, _, _, _ string, b autopilot.Basis) (domain.AutopilotEvent, error) {
	f.basis = append(f.basis, b)
	return domain.AutopilotEvent{ID: "ape_9", Kind: domain.AutopilotEventDecision, Summary: "Q → C", Detail: "why\nBased on your past choice: Merge? → ja\nConfidence: low. Autopilot took the safest option that can be undone."}, nil
}

func TestAdviceAndDecisionBasisRoutes(t *testing.T) {
	svc := &fakeAdvisorAutopilot{}
	h := autopilotRouter(svc)
	w := doJSON(h, http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/advice", `{"question":"Which model?"}`, caller.Header, "mer-orchestrator-1")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "quality over cost") || len(svc.asked) != 1 {
		t.Fatalf("advice = %d %s", w.Code, w.Body.String())
	}
	w = doJSON(h, http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/decisions",
		`{"question":"Q","choice":"C","why":"why","basedOn":["p1","p2"],"confidence":"low"}`, caller.Header, "mer-orchestrator-1")
	if w.Code != http.StatusOK || len(svc.basis) != 1 || svc.basis[0].Confidence != "low" || len(svc.basis[0].PairIDs) != 2 {
		t.Fatalf("decision = %d %s basis=%+v", w.Code, w.Body.String(), svc.basis)
	}
	var out AutopilotDecideResponse
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Event.Confidence != "low" || len(out.Event.BasedOn) != 1 || out.Event.Detail != "why" {
		t.Fatalf("the log line must split why / based-on / confidence: %+v", out.Event)
	}
	// A service without the advisor part still answers, as before.
	plain := &fakeAutopilot{}
	if w := doJSON(autopilotRouter(plain), http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/advice", `{"question":"Q"}`, caller.Header, "mer-orchestrator-1"); w.Code != http.StatusNotImplemented {
		t.Fatalf("advice without an advisor = %d", w.Code)
	}
	if w := doJSON(autopilotRouter(plain), http.MethodPost, "/sessions/mer-orchestrator-1/autopilot/decisions", `{"question":"Q","choice":"C","why":"w","confidence":"high"}`, caller.Header, "mer-orchestrator-1"); w.Code != http.StatusOK {
		t.Fatalf("decision without an advisor = %d", w.Code)
	}
}

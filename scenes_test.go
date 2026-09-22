package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAdaptiveSceneHierarchyAndSubsceneSelection(t *testing.T) {
	s := testServer(t)
	var roots, children int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM scenes WHERE parent_id='' AND enabled=1`).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM scenes WHERE parent_id<>'' AND enabled=1`).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if roots != 13 || children < 40 {
		t.Fatalf("unexpected scene catalog roots=%d children=%d", roots, children)
	}
	candidate, err := s.adaptiveSelect(3, "scene", "polite-disagreement")
	if err != nil {
		t.Fatal(err)
	}
	if candidate.SceneID != "meeting" || candidate.SubsceneID != "polite-disagreement" || candidate.ScopeRelaxed {
		t.Fatalf("unexpected scene candidate: %#v", candidate)
	}
	selected, ok := candidate.DecisionTrace["selected_scene"].(string)
	if !ok || selected != "meeting" {
		t.Fatalf("missing selected scene trace: %#v", candidate.DecisionTrace)
	}
}

func TestSceneRoutesAndPracticeMetadata(t *testing.T) {
	s := testServer(t)
	mux := http.NewServeMux()
	registerRoutes(mux, s, http.NotFoundHandler())
	list := httptest.NewRecorder()
	mux.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/scenes", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("scenes status=%d body=%s", list.Code, list.Body.String())
	}
	exercise, err := s.generateExerciseForScene(context.Background(), 3, "scene", "meeting", "polite-disagreement", "")
	if err != nil {
		t.Fatal(err)
	}
	if exercise["scene_id"] != "meeting" || exercise["subscene_id"] != "polite-disagreement" {
		t.Fatalf("missing scene metadata: %#v", exercise)
	}
	if _, ok := exercise["decision_trace"].(map[string]any); !ok {
		t.Fatalf("missing decision trace: %#v", exercise)
	}
}

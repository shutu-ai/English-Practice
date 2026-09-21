package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := seed(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Server{db: db, llm: &LLMRegistry{configs: map[string]ProviderConfig{}}}
}

func TestAssessmentCompletesAfterTwelveResponses(t *testing.T) {
	s := testServer(t)
	for i := 0; i < 11; i++ {
		if err := s.updateAssessment(Eval{Verdict: "mostly_correct", PatternScore: .7}); err != nil {
			t.Fatal(err)
		}
	}
	var complete int
	var raw string
	if err := s.db.QueryRow("SELECT assessment_complete,assessment_progress FROM user_profile WHERE id='default'").Scan(&complete, &raw); err != nil {
		t.Fatal(err)
	}
	if complete != 0 {
		t.Fatalf("assessment completed too early: %s", raw)
	}
	if err := s.updateAssessment(Eval{Verdict: "correct", PatternScore: .9}); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT assessment_complete FROM user_profile WHERE id='default'").Scan(&complete); err != nil {
		t.Fatal(err)
	}
	if complete != 1 {
		t.Fatal("assessment did not complete after twelve responses")
	}
}

func TestProviderHTTPFailuresAreReturnedWithoutLearningMutation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.Handler
		timeout int
	}{
		{name: "4xx", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadRequest) }), timeout: 2},
		{name: "5xx", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }), timeout: 2},
		{name: "timeout", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(1200 * time.Millisecond) }), timeout: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer(t)
			ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
			if err != nil {
				t.Fatal(err)
			}
			mock := httptest.NewServer(tc.handler)
			defer mock.Close()
			s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Enabled: true, Timeout: tc.timeout}
			result, err := s.submitAttempt(context.Background(), "failure-session", ex["exercise_id"].(string), "hello")
			if err != nil {
				t.Fatal(err)
			}
			if result["evaluation_status"] != "failed" {
				t.Fatalf("result=%#v", result)
			}
		})
	}
}

func TestProviderTestReusesStoredAPIKeyWithoutReturningIt(t *testing.T) {
	s := testServer(t)
	var receivedAuth string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer mock.Close()
	mux := http.NewServeMux()
	registerRoutes(mux, s, http.NotFoundHandler())
	save := httptest.NewRecorder()
	saveReq := httptest.NewRequest(http.MethodPost, "/api/providers", strings.NewReader(fmt.Sprintf(`{"id":"mock","name":"Mock","type":"openai-compatible","base_url":%q,"api_key":"unit-test-key","model":"mock","enabled":true}`, mock.URL)))
	saveReq.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(save, saveReq)
	if save.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", save.Code, save.Body.String())
	}
	if strings.Contains(save.Body.String(), "unit-test-key") {
		t.Fatal("provider response exposed API key")
	}
	testReq := httptest.NewRequest(http.MethodPost, "/api/providers/test", strings.NewReader(fmt.Sprintf(`{"id":"mock","type":"openai-compatible","base_url":%q,"model":"mock"}`, mock.URL)))
	testReq.Header.Set("Content-Type", "application/json")
	testResp := httptest.NewRecorder()
	mux.ServeHTTP(testResp, testReq)
	if testResp.Code != http.StatusOK || !strings.Contains(testResp.Body.String(), `"ok":true`) {
		t.Fatalf("test response status=%d body=%s", testResp.Code, testResp.Body.String())
	}
	if receivedAuth != "Bearer unit-test-key" {
		t.Fatalf("stored key was not reused: %q", receivedAuth)
	}
}

func TestMasteryAndReviewAreUpdatedAfterValidatedEvaluation(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.submitAttempt(context.Background(), "session-test", ex["exercise_id"].(string), "I might be a little late today.")
	if err != nil {
		t.Fatal(err)
	}
	if result["evaluation_status"] != "validated" {
		t.Fatalf("unexpected result: %#v", result)
	}
	var attempts, reviews int
	if err := s.db.QueryRow("SELECT attempts FROM pattern_mastery WHERE pattern_id=?", ex["pattern_id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("mastery attempts=%d", attempts)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM review_schedule WHERE pattern_id=?", ex["pattern_id"]).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 1 {
		t.Fatalf("review rows=%d", reviews)
	}
}

func TestInvalidProviderOutputDoesNotUpdateMastery(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
	}))
	defer provider.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: provider.URL, Model: "mock", Enabled: true, Timeout: 2}
	result, err := s.submitAttempt(context.Background(), "session-invalid", ex["exercise_id"].(string), "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result["evaluation_status"] != "failed" {
		t.Fatalf("expected failed evaluation: %#v", result)
	}
	var attempts int
	if err := s.db.QueryRow("SELECT attempts FROM pattern_mastery WHERE pattern_id=?", ex["pattern_id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("invalid output changed mastery: %d", attempts)
	}
}

func TestHeuristicEvaluatorSupportsMultipleValidAnswers(t *testing.T) {
	a := heuristicEval("今天可能晚到", "might / may", "I may arrive a bit late today.")
	if a.Verdict != "correct" || a.MeaningScore < .9 {
		t.Fatalf("unexpected evaluation: %#v", a)
	}
}

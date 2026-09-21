package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
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
		{name: "429", handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) }), timeout: 2},
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

func TestProvider401PersistsSafeDiagnostics(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer mock.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Enabled: true, Timeout: 2}
	result, err := s.submitAttempt(context.Background(), "status-session", ex["exercise_id"].(string), "I didn't go because I didn't feel well.")
	if err != nil {
		t.Fatal(err)
	}
	diag, ok := result["diagnostics"].(EvaluationDiagnostics)
	if !ok {
		t.Fatalf("missing diagnostics: %#v", result)
	}
	if diag.HTTPStatus != http.StatusUnauthorized || diag.ErrorCategory != "provider_4xx" || diag.FailureStage != "provider_http" {
		t.Fatalf("unexpected diagnostics: %#v", diag)
	}
	var raw string
	if err := s.db.QueryRow("SELECT evaluation_diagnostics_json FROM attempts WHERE id=?", result["attempt_id"]).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "provider_4xx") || strings.Contains(raw, "Authorization") || strings.Contains(raw, "unit-test") {
		t.Fatalf("unsafe or incomplete diagnostics: %s", raw)
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

func validEvaluationJSON() string {
	return `{"verdict":"correct","meaning_score":0.91,"grammar_score":0.88,"naturalness_score":0.84,"pattern_score":0.72,"errors":[],"suggested_answer":"I didn't go because I didn't feel well.","explanation_zh":"表达自然，意思和语法都正确。"}`
}

func TestStructuredOutputNormalization(t *testing.T) {
	eval, err := normalizeEvalContent("```json\n{\"verdict\":\"Mostly Correct\",\"meaning_score\":85,\"grammarScore\":\"0.8\",\"naturalness_score\":\"72%\",\"pattern_score\":0.65,\"errors\":null,\"suggestedAnswer\":\"I didn't go because I didn't feel well.\",\"explanationZh\":\"可以理解。\"}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if eval.Verdict != "mostly_correct" || eval.MeaningScore != .85 || eval.GrammarScore != .8 || eval.NaturalnessScore != .72 || len(eval.Errors) != 0 {
		t.Fatalf("normalization failed: %#v", eval)
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":1.5,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("ambiguous score accepted")
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[{"type":"unknown","severity":"minor","explanation":"x"}],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("unknown error type accepted")
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[],"suggested_answer":"x"}`); err == nil {
		t.Fatal("missing explanation accepted")
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[{"type":"meaning","severity":"minor","explanation":"x",}],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("trailing comma accepted")
	}
	if _, err := normalizeEvalContent("Here is the JSON:\n" + validEvaluationJSON()); err == nil {
		t.Fatal("prose outside JSON accepted")
	}
}

func TestInvalidStructuredOutputRetriesWithRepairInstruction(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	repairSeen := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Repair instruction") {
			repairSeen = true
		}
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not json"}}]}`))
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, validEvaluationJSON())))
	}))
	defer mock.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Enabled: true, Timeout: 2}
	result, err := s.submitAttempt(context.Background(), "retry-session", ex["exercise_id"].(string), "I didn't go because I didn't feel well.")
	if err != nil {
		t.Fatal(err)
	}
	if result["evaluation_status"] != "validated" || calls != 2 || !repairSeen {
		t.Fatalf("retry failed: result=%#v calls=%d repair=%v", result, calls, repairSeen)
	}
	var attempts int
	if err := s.db.QueryRow("SELECT attempts FROM pattern_mastery WHERE pattern_id=?", ex["pattern_id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("mastery attempts=%d", attempts)
	}
}

func TestProviderWithoutJSONModeFallsBackToPromptEnforcedJSON(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	jsonModeSeen := false
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"response_format"`) {
			jsonModeSeen = true
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, validEvaluationJSON())))
	}))
	defer mock.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Enabled: true, Timeout: 2}
	result, err := s.submitAttempt(context.Background(), "json-fallback-session", ex["exercise_id"].(string), "I didn't go because I didn't feel well.")
	if err != nil {
		t.Fatal(err)
	}
	if result["evaluation_status"] != "validated" || calls != 2 || !jsonModeSeen {
		t.Fatalf("JSON capability fallback failed: result=%#v calls=%d json_mode=%v", result, calls, jsonModeSeen)
	}
}

func TestFailedAttemptReevaluationUpdatesMasteryExactlyOnce(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls <= 2 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"bad"}}]}`))
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, validEvaluationJSON())))
	}))
	defer mock.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Enabled: true, Timeout: 2}
	first, err := s.submitAttempt(context.Background(), "reeval-session", ex["exercise_id"].(string), "I didn't go because I didn't feel well.")
	if err != nil {
		t.Fatal(err)
	}
	if first["evaluation_status"] != "failed" {
		t.Fatalf("expected failed: %#v", first)
	}
	attemptID := first["attempt_id"].(string)
	var attempts int
	if err := s.db.QueryRow("SELECT attempts FROM pattern_mastery WHERE pattern_id=?", ex["pattern_id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 0 {
		t.Fatalf("failed evaluation mutated mastery: %d", attempts)
	}
	second, err := s.reevaluateAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if second["evaluation_status"] != "validated" {
		t.Fatalf("reevaluation failed: %#v", second)
	}
	third, err := s.reevaluateAttempt(context.Background(), attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if third["evaluation_status"] != "validated" {
		t.Fatalf("idempotent reevaluation failed: %#v", third)
	}
	if err := s.db.QueryRow("SELECT attempts FROM pattern_mastery WHERE pattern_id=?", ex["pattern_id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 {
		t.Fatalf("reevaluation mutated mastery more than once: %d", attempts)
	}
}

func TestFailedReevaluationDoesNotWriteEvaluationOrMastery(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"still invalid"}}]}`))
	}))
	defer mock.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Enabled: true, Timeout: 2}
	first, err := s.submitAttempt(context.Background(), "failed-reeval-session", ex["exercise_id"].(string), "I didn't go because I didn't feel well.")
	if err != nil || first["evaluation_status"] != "failed" {
		t.Fatalf("expected failed initial evaluation: result=%#v err=%v", first, err)
	}
	second, err := s.reevaluateAttempt(context.Background(), first["attempt_id"].(string))
	if err != nil || second["evaluation_status"] != "failed" {
		t.Fatalf("expected failed re-evaluation: result=%#v err=%v", second, err)
	}
	var evaluations, attempts int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM evaluations").Scan(&evaluations); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT attempts FROM pattern_mastery WHERE pattern_id=?", ex["pattern_id"]).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if evaluations != 0 || attempts != 0 {
		t.Fatalf("failed re-evaluation mutated state: evaluations=%d attempts=%d", evaluations, attempts)
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

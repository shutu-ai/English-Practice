package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestV26PracticeNextCreatesPreferenceSessionForFirstRequest(t *testing.T) {
	s := testServer(t)
	mux := http.NewServeMux()
	registerRoutes(mux, s, http.NotFoundHandler())
	request := httptest.NewRequest(http.MethodPost, "/api/practice/next", bytes.NewBufferString(`{"difficulty_mode":"fixed","fixed_difficulty":4,"training_focus":"pattern"}`))
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("fixed first request returned %d: %s", response.Code, response.Body.String())
	}
	var fixed map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &fixed); err != nil {
		t.Fatal(err)
	}
	if fixed["session_id"] == "" || fixed["difficulty_mode"] != DifficultyModeFixed || fixed["fixed_difficulty"] != float64(4) || fixed["difficulty"] != float64(4) || fixed["curriculum_level"] != float64(4) {
		t.Fatalf("first fixed request did not bind its session and exercise: %#v", fixed)
	}
	if difficulty := fixed["difficulty"].(float64); difficulty < 3.7 || difficulty > 4.3 {
		t.Fatalf("fixed D4 exercise outside its band: %.2f", difficulty)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/practice/next", bytes.NewBufferString(`{"difficulty_mode":"fixed","fixed_difficulty":4,"training_focus":"free_expression"}`))
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("free first request returned %d: %s", response.Code, response.Body.String())
	}
	var free map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &free); err != nil {
		t.Fatal(err)
	}
	if free["session_id"] == "" || free["training_focus"] != TrainingFocusFree || free["target_pattern_present"] != false || free["pattern_id"] != nil || free["target_pattern"] != nil || free["curriculum_level"] != float64(4) {
		t.Fatalf("first free request did not bind its session or leaked pattern context: %#v", free)
	}
}

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

func TestOneHundredAdaptiveQAsWithNoisyProvider(t *testing.T) {
	s := testServer(t)
	exercises := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
		if err != nil {
			t.Fatal(err)
		}
		exercises = append(exercises, ex["exercise_id"].(string))
	}
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		content := validEvaluationJSON()
		switch {
		case calls <= 2:
			content = "not json"
		case calls%3 == 0:
			content = "```json\n" + validEvaluationJSON() + "\n```"
		case calls%3 == 1:
			content = "Here is the JSON:\n" + validEvaluationJSON() + "\nDone."
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, content)))
	}))
	defer provider.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: provider.URL, Model: "mock", Enabled: true, Timeout: 2}

	for i, exerciseID := range exercises {
		result, err := s.submitAttempt(context.Background(), "simulation-100", exerciseID, "I may arrive a little late today.")
		if err != nil {
			t.Fatalf("question %d returned error: %v", i+1, err)
		}
		if result["evaluation_status"] != "validated" {
			t.Fatalf("question %d was not validated: %#v", i+1, result)
		}
	}
	if calls != 102 {
		t.Fatalf("expected 100 evaluations plus 2 automatic retries, provider calls=%d", calls)
	}
	var validated, failed int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM attempts WHERE evaluation_status='validated'").Scan(&validated); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM attempts WHERE evaluation_status='failed'").Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if validated != 100 || failed != 0 {
		t.Fatalf("100-question simulation persisted validated=%d failed=%d", validated, failed)
	}
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
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
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
	if eval, err := normalizeEvalContent(strings.Replace(validEvaluationJSON(), `"verdict":"correct"`, `"verdict":"partially_correct"`, 1)); err != nil || eval.Verdict != "mostly_correct" {
		t.Fatalf("provider verdict alias was not normalized: %#v err=%v", eval, err)
	}
	for _, tc := range []struct {
		provider, canonical string
	}{
		{"good", "correct"}, {"excellent", "correct"}, {"okay", "mostly_correct"}, {"needs work", "needs_improvement"}, {"wrong", "incorrect"},
	} {
		input := strings.Replace(validEvaluationJSON(), `"verdict":"correct"`, fmt.Sprintf(`"verdict":%q`, tc.provider), 1)
		eval, err := normalizeEvalContent(input)
		if err != nil || eval.Verdict != tc.canonical {
			t.Fatalf("provider verdict %q was not normalized to %q: %#v err=%v", tc.provider, tc.canonical, eval, err)
		}
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":1.5,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("ambiguous score accepted")
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[{"type":"unknown","severity":"minor","explanation":"x"}],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("unknown error type accepted")
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"target_pattern_match":"literal_only","target_pattern_score":.8,"errors":[],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("unknown target pattern match accepted")
	}
	if eval, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":0.8,"grammar_score":0.8,"naturalness_score":0.8,"pattern_score":0.8,"target_pattern_match":"semantic_equivalent","target_pattern_score":0.9,"errors":null,"suggested_answer":"x","explanation_zh":"x"}`); err != nil || eval.TargetPatternMatch != TargetPatternSemanticEquivalent || eval.TargetPatternScore != .9 || len(eval.Errors) != 0 {
		t.Fatalf("semantic target pattern fields were not normalized: %#v err=%v", eval, err)
	}
	aliased, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":0.8,"grammar_score":0.8,"naturalness_score":0.8,"pattern_score":0.8,"errors":[{"type":"meaning_mismatch","severity":"low","explanation":"x"}],"suggested_answer":"x","explanation_zh":"x"}`)
	if err != nil || len(aliased.Errors) != 1 || aliased.Errors[0]["type"] != "meaning" || aliased.Errors[0]["severity"] != "minor" {
		t.Fatalf("known provider aliases were not normalized: %#v err=%v", aliased, err)
	}
	spelling, err := normalizeEvalContent(`{"verdict":"needs_improvement","meaning_score":0.8,"grammar_score":0.7,"naturalness_score":0.6,"pattern_score":0.5,"errors":[{"type":"spelling","severity":"minor","explanation":"Check the spelling."}],"suggested_answer":"x","explanation_zh":"x"}`)
	if err != nil || len(spelling.Errors) != 1 || spelling.Errors[0]["type"] != "word_choice" {
		t.Fatalf("spelling provider error was not normalized: %#v err=%v", spelling, err)
	}
	for _, tc := range []struct{ provider, canonical string }{
		{"irrelevant_content", "meaning"}, {"irrelevant_response", "meaning"},
		{"task_completion", "meaning"}, {"task_fulfillment", "meaning"},
		{"task_relevance", "meaning"}, {"task_response", "meaning"},
		{"communication_intent", "meaning"}, {"communication_intent_not_fulfilled", "meaning"},
		{"content", "other"}, {"pragmatics", "other"},
	} {
		input := strings.Replace(validEvaluationJSON(), `"errors":[]`, fmt.Sprintf(`"errors":[{"type":%q,"severity":"minor","explanation":"x"}]`, tc.provider), 1)
		eval, err := normalizeEvalContent(input)
		if err != nil || len(eval.Errors) != 1 || eval.Errors[0]["type"] != tc.canonical {
			t.Errorf("provider error type %q was not normalized to %q: %#v err=%v", tc.provider, tc.canonical, eval, err)
		}
	}
	if eval, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":0.8,"grammar_score":0.8,"naturalness_score":0.8,"pattern_score":0.8,"errors":[{"type":"meaning","level":"low","description":"x"}],"suggested_answer":"x","explanation_zh":"x"}`); err != nil || eval.Errors[0]["explanation"] != "x" {
		t.Fatalf("error field aliases were not normalized: %#v err=%v", eval, err)
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[],"suggested_answer":"x"}`); err == nil {
		t.Fatal("missing explanation accepted")
	}
	if _, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":.8,"grammar_score":.8,"naturalness_score":.8,"pattern_score":.8,"errors":[{"type":"meaning","severity":"minor","explanation":"x",}],"suggested_answer":"x","explanation_zh":"x"}`); err == nil {
		t.Fatal("trailing comma accepted")
	}
	if eval, err := normalizeEvalContent("Here is the JSON:\n" + validEvaluationJSON() + "\nHope this helps."); err != nil || eval.Verdict != "correct" {
		t.Fatalf("provider prose wrapper was not normalized: %#v err=%v", eval, err)
	}
	if eval, err := normalizeEvalContent("```json\n{" + `"evaluation":` + validEvaluationJSON() + "}\n```"); err != nil || eval.Verdict != "correct" {
		t.Fatalf("provider evaluation wrapper was not normalized: %#v err=%v", eval, err)
	}
	if _, err := normalizeEvalContent(validEvaluationJSON() + "\n" + validEvaluationJSON()); err == nil {
		t.Fatal("multiple JSON objects accepted")
	}
}

func TestSemanticTargetPatternClassification(t *testing.T) {
	cases := []struct {
		name, pattern, target, answer, want string
	}{
		{"having-said-that exact", "having-said-that", "Having said that, ...", "Having said that, I still think we should wait.", TargetPatternExact},
		{"having-said-that equivalent", "having-said-that", "Having said that, ...", "That said, I still think we should wait.", TargetPatternSemanticEquivalent},
		{"having-said-that equivalent even so", "having-said-that", "Having said that, ...", "Even so, I still think we should wait.", TargetPatternSemanticEquivalent},
		{"having-said-that partial", "having-said-that", "Having said that, ...", "On the other hand, I still think we should wait.", TargetPatternPartial},
		{"having-said-that non-equivalent", "having-said-that", "Having said that, ...", "I disagree with the plan.", TargetPatternNotMatched},
		{"clarification equivalent", "clarification", "What I mean is ...", "What I'm trying to say is that we should wait.", TargetPatternSemanticEquivalent},
		{"suggestion equivalent", "professional-suggestion", "I'd like to suggest ...", "I suggest that we wait.", TargetPatternSemanticEquivalent},
		{"disagreement equivalent", "disagreement", "I see your point, but ...", "I understand your point, but we should wait.", TargetPatternSemanticEquivalent},
		{"uncertainty equivalent", "formal-opinion", "I'm not entirely convinced that ...", "I'm not completely convinced that we should proceed.", TargetPatternSemanticEquivalent},
		{"mixed conditional guard", "mixed-conditional", "If I had ..., I would ...", "I didn't know, so I made a mistake.", TargetPatternNotMatched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, score, supported := classifyTargetPattern(tc.pattern, tc.target, tc.answer)
			if !supported || got != tc.want || score <= 0 {
				t.Fatalf("classification got=%q score=%.2f supported=%v want=%q", got, score, supported, tc.want)
			}
		})
	}
}

func TestSemanticPatternReconciliationSeparatesMasteryEvidence(t *testing.T) {
	exact := Eval{Verdict: "incorrect", MeaningScore: .9, GrammarScore: .9, NaturalnessScore: .9, PatternScore: .05, Errors: []map[string]any{{"type": "target_pattern_missing", "severity": "major"}}}
	reconcileTargetPattern(&exact, "having-said-that", "Having said that, ...", "Having said that, I still think we should wait.")
	if exact.Verdict != "correct" || exact.TargetPatternMatch != TargetPatternExact || exact.TargetPatternScore != 1 || len(exact.Errors) != 0 {
		t.Fatalf("exact reconciliation failed: %#v", exact)
	}
	equivalent := Eval{Verdict: "incorrect", MeaningScore: .9, GrammarScore: .9, NaturalnessScore: .9, PatternScore: .05, Errors: []map[string]any{{"type": "target_pattern_missing", "severity": "major"}}}
	reconcileTargetPattern(&equivalent, "having-said-that", "Having said that, ...", "That said, I still think we should wait.")
	if equivalent.Verdict != "correct" || equivalent.TargetPatternMatch != TargetPatternSemanticEquivalent || equivalent.TargetPatternScore >= exact.TargetPatternScore || equivalent.PatternScore >= exact.PatternScore {
		t.Fatalf("semantic reconciliation did not preserve graded evidence: %#v", equivalent)
	}
	nonEquivalent := Eval{Verdict: "correct", MeaningScore: .9, GrammarScore: .9, NaturalnessScore: .9, PatternScore: .9}
	reconcileTargetPattern(&nonEquivalent, "mixed-conditional", "If I had ..., I would ...", "I didn't know, so I made a mistake.")
	if nonEquivalent.TargetPatternMatch != TargetPatternNotMatched || nonEquivalent.Verdict == "correct" {
		t.Fatalf("non-equivalent guard failed: %#v", nonEquivalent)
	}
}

func TestEvaluationSemanticColumnsMigrateWithV231Schema(t *testing.T) {
	db, err := sql.Open("sqlite", "file:v231-migration?mode=memory&cache=private")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_meta (version INTEGER NOT NULL); INSERT INTO schema_meta(version) VALUES(8); CREATE TABLE evaluations (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL UNIQUE, verdict TEXT NOT NULL, meaning_score REAL NOT NULL, grammar_score REAL NOT NULL, naturalness_score REAL NOT NULL, pattern_score REAL NOT NULL, errors_json TEXT NOT NULL, suggested_answer TEXT NOT NULL, more_natural TEXT NOT NULL DEFAULT '', more_natural_needed INTEGER NOT NULL DEFAULT 0, alternative TEXT NOT NULL DEFAULT '', explanation_zh TEXT NOT NULL, validated INTEGER NOT NULL, created_at TEXT NOT NULL);`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var match, definition string
	if err := db.QueryRow("SELECT name,type FROM pragma_table_info('evaluations') WHERE name='target_pattern_match'").Scan(&match, &definition); err != nil || match != "target_pattern_match" {
		t.Fatalf("target_pattern_match migration missing: name=%q type=%q err=%v", match, definition, err)
	}
	if err := db.QueryRow("SELECT name,type FROM pragma_table_info('evaluations') WHERE name='target_pattern_score'").Scan(&match, &definition); err != nil || match != "target_pattern_score" {
		t.Fatalf("target_pattern_score migration missing: name=%q type=%q err=%v", match, definition, err)
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
	targetPatternSeen := false
	var targetPattern string
	if err := s.db.QueryRow("SELECT pattern FROM sentence_patterns WHERE id=?", ex["pattern_id"]).Scan(&targetPattern); err != nil {
		t.Fatal(err)
	}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "Repair instruction") {
			repairSeen = true
		}
		if strings.Contains(string(body), "Target pattern expression: "+targetPattern) {
			targetPatternSeen = true
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
	if result["evaluation_status"] != "validated" || calls != 2 || !repairSeen || !targetPatternSeen {
		t.Fatalf("retry failed: result=%#v calls=%d repair=%v target_pattern=%v", result, calls, repairSeen, targetPatternSeen)
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
		if calls <= 3 {
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

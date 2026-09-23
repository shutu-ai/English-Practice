package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func createPracticeSession(t *testing.T, s *Server, p PracticePreferences) string {
	t.Helper()
	p, err := normalizePracticePreferences(p)
	if err != nil {
		t.Fatal(err)
	}
	var ability float64
	_ = s.db.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&ability)
	center, lower, upper := ability, 0.0, 0.0
	if p.DifficultyMode == DifficultyModeFixed {
		center = p.FixedDifficulty
		lower, upper = fixedDifficultyBand(center, s.adaptiveConfig())
	} else {
		lower, upper = sessionBand(center, s.adaptiveConfig())
	}
	session := id("v25-session")
	_, err = s.db.Exec(`INSERT INTO sessions(id,mode,started_at,start_global_difficulty,end_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count,difficulty_mode,fixed_difficulty,training_focus) VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?)`, session, "adaptive", "2026-01-01T00:00:00Z", ability, ability, center, lower, upper, 0, p.DifficultyMode, p.FixedDifficulty, p.TrainingFocus)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func TestV25PracticePreferenceDefaultsAndFixedValidation(t *testing.T) {
	p, err := normalizePracticePreferences(PracticePreferences{})
	if err != nil || p.DifficultyMode != DifficultyModeAdaptive || p.TrainingFocus != TrainingFocusPattern || !p.TargetPatternEnabled {
		t.Fatalf("unexpected defaults: %#v err=%v", p, err)
	}
	if _, err := normalizePracticePreferences(PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 0, TrainingFocus: TrainingFocusPattern}); err == nil {
		t.Fatal("invalid fixed difficulty was accepted")
	}
	if _, err := normalizePracticePreferences(PracticePreferences{DifficultyMode: DifficultyModeAdaptive, FixedDifficulty: 4, TrainingFocus: TrainingFocusFree}); err != nil {
		t.Fatal(err)
	}
	if !IsPatternEligibleForDifficulty(4.3, 4) || IsPatternEligibleForDifficulty(4.5, 4) {
		t.Fatal("fixed difficulty overlap band is incorrect")
	}
}

func TestV25FixedPatternNeverLeavesBandOrMovesSessionCenter(t *testing.T) {
	s := testServer(t)
	session := createPracticeSession(t, s, PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 4, TrainingFocus: TrainingFocusPattern})
	for i := 0; i < 8; i++ {
		ex, err := s.generateExerciseForScene(context.Background(), 4, "adaptive", "", "", session)
		if err != nil {
			t.Fatal(err)
		}
		if got := ex["difficulty"].(float64); got < 3.7 || got > 4.3 {
			t.Fatalf("out-of-band fixed exercise: %.3f", got)
		}
		if ex["training_focus"] != TrainingFocusPattern || ex["target_pattern_present"] != true {
			t.Fatalf("fixed pattern snapshot missing: %#v", ex)
		}
		if _, err := s.submitAttempt(context.Background(), session, ex["exercise_id"].(string), "I see your point, but I need another option."); err != nil {
			t.Fatal(err)
		}
	}
	var center, lower, upper float64
	if err := s.db.QueryRow(`SELECT session_difficulty_center,session_band_lower,session_band_upper FROM sessions WHERE id=?`, session).Scan(&center, &lower, &upper); err != nil {
		t.Fatal(err)
	}
	if center != 4 || lower != 3.7 || upper != 4.3 {
		t.Fatalf("fixed session moved or band changed: center=%.2f band=%.2f..%.2f", center, lower, upper)
	}
	var outOfBand int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM exercises WHERE difficulty<3.7 OR difficulty>4.3`).Scan(&outOfBand); err != nil {
		t.Fatal(err)
	}
	if outOfBand != 0 {
		t.Fatalf("fixed mode generated %d out-of-band exercises", outOfBand)
	}
}

func TestV25FreeExpressionHasNoPatternPenaltyOrMutation(t *testing.T) {
	s := testServer(t)
	session := createPracticeSession(t, s, PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 4, TrainingFocus: TrainingFocusFree})
	var before int
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(attempts),0) FROM pattern_mastery`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	ex, err := s.generateExerciseForScene(context.Background(), 4, "adaptive", "", "", session)
	if err != nil {
		t.Fatal(err)
	}
	if ex["target_pattern"] != nil || ex["pattern_id"] != nil || ex["target_pattern_present"] != false {
		t.Fatalf("free exercise leaked target pattern: %#v", ex)
	}
	result, err := s.submitAttempt(context.Background(), session, ex["exercise_id"].(string), "I understand your point, but I think we should consider another option.")
	if err != nil || result["evaluation_status"] != "validated" {
		t.Fatalf("free expression did not validate: %#v err=%v", result, err)
	}
	eval := result["evaluation"].(Eval)
	if eval.TargetPatternMatch != TargetPatternNotApplicable {
		t.Fatalf("free evaluation target match=%q", eval.TargetPatternMatch)
	}
	if len(eval.Errors) > 0 {
		for _, item := range eval.Errors {
			if item["type"] == "target_pattern_missing" {
				t.Fatal("free expression received a target pattern penalty")
			}
		}
	}
	var after int
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(attempts),0) FROM pattern_mastery`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("free expression mutated pattern mastery: before=%d after=%d", before, after)
	}
	var focus, match string
	if err := s.db.QueryRow(`SELECT a.training_focus,v.target_pattern_match FROM attempts a JOIN evaluations v ON v.attempt_id=a.id WHERE a.exercise_id=?`, ex["exercise_id"]).Scan(&focus, &match); err != nil {
		t.Fatal(err)
	}
	if focus != TrainingFocusFree || match != TargetPatternNotApplicable {
		t.Fatalf("free state snapshot mismatch focus=%q match=%q", focus, match)
	}
}

func TestV25FreeEvaluatorPromptDoesNotContainTargetPattern(t *testing.T) {
	s := testServer(t)
	var body string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"verdict\":\"correct\",\"meaning_score\":0.9,\"grammar_score\":0.85,\"naturalness_score\":0.9,\"pattern_score\":0.5,\"target_pattern_match\":\"not_applicable\",\"target_pattern_score\":0,\"errors\":[],\"suggested_answer\":\"I understand your point, but I disagree.\",\"explanation_zh\":\"表达自然。\"}"}}]}`))
	}))
	defer provider.Close()
	s.llm.configs["v25"] = ProviderConfig{ID: "v25", Type: "openai-compatible", BaseURL: provider.URL, Model: "v25", Enabled: true, Timeout: 2, MaxTokens: 256}
	if _, _, _, _, err := s.evaluateWithProviderOptionsSpec(context.Background(), "请礼貌表达不同意见", "", "I understand your point, but I disagree.", "", ProviderRequestOptions{}, EvaluationSpec{TargetPatternMode: "none", Intent: "disagreement"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(body), "target pattern expression") || strings.Contains(strings.ToLower(body), "target_pattern_id") || strings.Contains(body, "Having said") {
		t.Fatalf("free evaluator prompt leaked target pattern context: %s", body)
	}
}

func TestV25SimulationModeMatrixMetrics(t *testing.T) {
	runner := &SimulationRunner{}
	base := DefaultSimulationConfig()
	base.Attempts = 50
	base.AttemptsExplicit = true
	base.SessionSize = 10
	base.DifficultyMode = DifficultyModeFixed
	base.FixedDifficulty = 4
	base.TrainingFocus = TrainingFocusPattern
	pattern, err := runner.Run(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if pattern.Metrics.FixedOutOfBandRate != 0 || pattern.Metrics.FixedPatternStarvationRate != 0 || pattern.Metrics.FixedLevelCoverage != 1 {
		t.Fatalf("fixed pattern invariants failed: %#v", pattern.Metrics)
	}
	base.TrainingFocus = TrainingFocusFree
	free, err := runner.Run(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if free.Metrics.FreeExercises != 50 || free.Metrics.FreeTargetPatternGeneratedCount != 0 || free.Metrics.FreeTargetPatternPenaltyCount != 0 || free.Metrics.FreePatternMasteryMutations != 0 {
		t.Fatalf("fixed free invariants failed: %#v", free.Metrics)
	}
	base.DifficultyMode = DifficultyModeAdaptive
	adaptive, err := runner.Run(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if adaptive.Metrics.FreeExercises != 50 || adaptive.Metrics.ModeMatrixScenario != "adaptive-free_expression" {
		t.Fatalf("adaptive free matrix scenario failed: %#v", adaptive.Metrics)
	}
}

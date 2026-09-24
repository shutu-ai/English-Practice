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
	if _, err := normalizePracticePreferences(PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 4.5, TrainingFocus: TrainingFocusPattern}); err == nil {
		t.Fatal("non-curriculum fixed difficulty was accepted")
	}
	if !IsPatternEligibleForDifficulty(4.3, 4) || IsPatternEligibleForDifficulty(4.5, 4) {
		t.Fatal("fixed difficulty overlap band is incorrect")
	}
}

func TestV26FixedDifficultyMasterySeparatesCoverageAndMastery(t *testing.T) {
	s := testServer(t)
	rows, err := s.db.Query(`SELECT p.id FROM sentence_patterns p JOIN curriculum_patterns cp ON cp.pattern_id=p.id WHERE cp.app_level_min<=4 AND p.catalog_difficulty BETWEEN 3.7 AND 4.3 ORDER BY p.id`)
	if err != nil {
		t.Fatal(err)
	}
	var patterns []string
	for rows.Next() {
		var pattern string
		if err := rows.Scan(&pattern); err != nil {
			t.Fatal(err)
		}
		patterns = append(patterns, pattern)
	}
	rows.Close()
	if len(patterns) == 0 {
		t.Fatal("expected eligible D4 curriculum patterns")
	}
	for i, pattern := range patterns {
		if i == 0 {
			_, err = s.db.Exec(`UPDATE learner_skill_state SET attempt_count=3,success_count=3,mastery=.95,state='MASTERED',next_review_at=NULL WHERE user_id='default' AND pattern_id=?`, pattern)
		} else {
			_, err = s.db.Exec(`UPDATE learner_skill_state SET attempt_count=0,success_count=0,mastery=.25,state='UNKNOWN',next_review_at=NULL WHERE user_id='default' AND pattern_id=?`, pattern)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	progress := s.fixedDifficultyMastery(4)
	if progress["eligible"] != len(patterns) || progress["mastered"] != 1 || progress["covered"] != 1 || progress["coverage_rate"] != 1/float64(len(patterns)) || progress["mastery_rate"] != 1/float64(len(patterns)) || progress["unseen"] != len(patterns)-1 || progress["weak"] != 0 || progress["completed"] != false {
		t.Fatalf("coverage and mastery aggregation mismatch: %#v", progress)
	}
}

func TestV26FixedLevelAggregationUsesSeparateCoverageAndMastery(t *testing.T) {
	states := map[string]int{"MASTERED": 7, "LEARNING": 2, "UNSEEN": 1, "WEAK": 0, "REVIEW_DUE": 0, "BLOCKED": 0}
	progress := aggregateFixedLevelMastery(4, 3.7, 4.3, 10, 9, 7, 0, 0, states, false)
	if progress["coverage_rate"] != .9 || progress["mastery_rate"] != .7 || progress["completed"] != false {
		t.Fatalf("coverage and mastery aggregation mismatch: %#v", progress)
	}
	reviewStates := map[string]int{"MASTERED": 8, "LEARNING": 0, "UNSEEN": 0, "WEAK": 0, "REVIEW_DUE": 2, "BLOCKED": 0}
	completed := aggregateFixedLevelMastery(4, 3.7, 4.3, 10, 10, 8, 2, 0, reviewStates, false)
	if completed["completed"] != true || completed["needs_review"] != true {
		t.Fatalf("review due must preserve historical completion: %#v", completed)
	}
	blockedStates := map[string]int{"MASTERED": 1, "LEARNING": 0, "UNSEEN": 0, "WEAK": 0, "REVIEW_DUE": 0, "BLOCKED": 1}
	blocked := aggregateFixedLevelMastery(4, 3.7, 4.3, 2, 1, 1, 0, 1, blockedStates, false)
	if blocked["completed"] != false || blocked["mastery_rate"] != .5 {
		t.Fatalf("blocked skills must remain in the completion denominator: %#v", blocked)
	}
}

func TestV26FixedD4KeepsLevelForOneHundredAttempts(t *testing.T) {
	s := testServer(t)
	session := createPracticeSession(t, s, PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 4, TrainingFocus: TrainingFocusPattern})
	for i := 0; i < 100; i++ {
		ex, err := s.generateExerciseForScene(context.Background(), 4, "adaptive", "", "", session)
		if err != nil {
			t.Fatal(err)
		}
		if level, ok := ex["curriculum_level"].(int); !ok || level > 4 {
			t.Fatalf("fixed D4 selected a curriculum-ineligible pattern: %#v", ex)
		}
		if ex["fixed_difficulty"] != float64(4) {
			t.Fatalf("fixed D4 snapshot changed: %#v", ex)
		}
		if got := ex["difficulty"].(float64); got < 3.7 || got > 4.3 {
			t.Fatalf("fixed D4 exercise escaped numeric band: %.2f", got)
		}
		if _, err := s.submitAttempt(context.Background(), session, ex["exercise_id"].(string), "I see your point, but I think we should consider the cost."); err != nil {
			t.Fatalf("attempt %d: %v", i+1, err)
		}
	}
	var mode string
	var fixed, center float64
	if err := s.db.QueryRow(`SELECT difficulty_mode,fixed_difficulty,session_difficulty_center FROM sessions WHERE id=?`, session).Scan(&mode, &fixed, &center); err != nil {
		t.Fatal(err)
	}
	if mode != DifficultyModeFixed || fixed != 4 || center != 4 {
		t.Fatalf("fixed D4 moved after 100 attempts: mode=%s fixed=%.1f center=%.1f", mode, fixed, center)
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

package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDifficultyV22SeparatesCenterTargetAndRealized(t *testing.T) {
	s := testServer(t)
	ex, err := s.generateExercise(context.Background(), 5, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	target := ex["target_difficulty"].(float64)
	realized := ex["realized_difficulty"].(float64)
	if target <= 0 || realized <= 0 || math.Abs(target-realized) > .60 {
		t.Fatalf("invalid target/realized contract: %#v", ex)
	}
	trace, ok := ex["decision_trace"].(map[string]any)
	if !ok || trace["learner_ability"] == nil || trace["session_center"] == nil || trace["pattern_ability"] == nil {
		t.Fatalf("incomplete decision trace: %#v", ex["decision_trace"])
	}
	if _, err := s.submitAttempt(context.Background(), "v22-session", ex["exercise_id"].(string), "I might be a little late today."); err != nil {
		t.Fatal(err)
	}
	var center, lower, upper, attemptTarget, attemptRealized float64
	if err := s.db.QueryRow(`SELECT session_difficulty_center,session_band_lower,session_band_upper FROM sessions WHERE id='v22-session'`).Scan(&center, &lower, &upper); err != nil {
		t.Fatal(err)
	}
	if center <= 0 || lower <= 0 || upper <= center {
		t.Fatalf("session center not initialized: center=%.2f band=%.2f..%.2f", center, lower, upper)
	}
	if err := s.db.QueryRow(`SELECT target_difficulty,realized_difficulty FROM attempts WHERE session_id='v22-session'`).Scan(&attemptTarget, &attemptRealized); err != nil {
		t.Fatal(err)
	}
	if attemptTarget != target || attemptRealized != realized {
		t.Fatalf("attempt difficulty trace target=%.2f/%.2f realized=%.2f/%.2f", attemptTarget, target, attemptRealized, realized)
	}
}

func TestDifficultyV22DeadbandAndBoundedSteps(t *testing.T) {
	cfg := defaultAdaptiveConfig()
	if next, reason := boundedControllerStep(5, .78, cfg, cfg.MaxSessionCenterStep); next != 5 || reason != "deadband_hold" {
		t.Fatalf("deadband moved center: %.2f %s", next, reason)
	}
	if next, _ := boundedControllerStep(5, 1, cfg, cfg.MaxSessionCenterStep); next-5 > cfg.MaxSessionCenterStep {
		t.Fatalf("high step exceeded bound: %.2f", next-5)
	}
	if next, _ := boundedControllerStep(5, 0, cfg, cfg.MaxSessionCenterStep); 5-next > cfg.MaxSessionCenterStep {
		t.Fatalf("low step exceeded bound: %.2f", 5-next)
	}
	weak, _ := targetDifficulty(5, 5.2, 4.2, 4.5, "weak_skill", false, false, 0, cfg)
	if weak < 4.3 || weak > 5.45 {
		t.Fatalf("weak target escaped productive session band: %.2f", weak)
	}
	probe, reason := targetDifficulty(5, 5.2, 4.2, 4.5, "probe", false, true, 0, cfg)
	if probe <= 5 || reason != "probe_isolated_from_session_envelope" {
		t.Fatalf("probe was not isolated: %.2f %s", probe, reason)
	}
}

func TestDifficultyV22MismatchIsRejectedAndRecorded(t *testing.T) {
	s := testServer(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"chinese_prompt\":\"请表达这个意思\",\"target_pattern\":\"because\",\"estimated_difficulty\":8.0,\"reference_answers\":[]}"}}]}`))
	}))
	defer provider.Close()
	s.llm.configs["mismatch"] = ProviderConfig{ID: "mismatch", Type: "openai-compatible", BaseURL: provider.URL, Model: "mock", Enabled: true, Timeout: 2}
	ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	if ex["generated_by"] != "fallback" || ex["difficulty_validation_status"] != "fallback_after_validation_failure" {
		t.Fatalf("mismatch did not fall back: %#v", ex)
	}
	var failures int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM difficulty_validation_events WHERE status='difficulty_validation_failed'`).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures == 0 {
		t.Fatal("difficulty mismatch was not recorded")
	}
}

func TestDifficultyV22TraceReplayAndSimulationAreDeterministic(t *testing.T) {
	s := testServer(t)
	for i := 0; i < 5; i++ {
		ex, err := s.generateExercise(context.Background(), 4.5, "adaptive", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.submitAttempt(context.Background(), "replay-session", ex["exercise_id"].(string), "I might be a little late today."); err != nil {
			t.Fatal(err)
		}
	}
	trace, err := s.difficultyTrace("all", "", 50)
	if err != nil || len(trace) != 5 {
		t.Fatalf("trace err=%v len=%d", err, len(trace))
	}
	replay, err := s.calibrationReplay("all", `{"changes":{"max_session_center_step":0.05}}`)
	if err != nil {
		t.Fatal(err)
	}
	if replay["before"] == nil || replay["after"] == nil || replay["trace"] == nil {
		t.Fatalf("replay is not executable: %#v", replay)
	}
	a := runDifficultySimulation("stable_intermediate", 500, defaultAdaptiveConfig())
	b := runDifficultySimulation("stable_intermediate", 500, defaultAdaptiveConfig())
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	if string(left) != string(right) || a.MaxSingleStepChange > math.Max(defaultAdaptiveConfig().MaxSessionCenterStep, defaultAdaptiveConfig().ProbeDelta)+1e-9 {
		t.Fatalf("simulation is not deterministic/bounded: %#v", a)
	}
}

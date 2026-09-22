package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func simulationTestConfig(persona string, attempts int) SimulationConfig {
	c := DefaultSimulationConfig()
	c.Persona, c.Attempts, c.SessionSize, c.Seed = persona, attempts, 20, 42
	return c
}

func TestSimulationDeterminismIncludesTrajectoryAndMetrics(t *testing.T) {
	a, err := RunSimulation(context.Background(), simulationTestConfig("stable-intermediate", 120))
	if err != nil {
		t.Fatal(err)
	}
	b, err := RunSimulation(context.Background(), simulationTestConfig("stable-intermediate", 120))
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(struct {
		Metrics SimulationMetrics
		Trace   []SimulationAttempt
		Unlocks []SkillUnlockEvent
	}{a.Metrics, a.AttemptsTrace, a.UnlockEvents})
	right, _ := json.Marshal(struct {
		Metrics SimulationMetrics
		Trace   []SimulationAttempt
		Unlocks []SkillUnlockEvent
	}{b.Metrics, b.AttemptsTrace, b.UnlockEvents})
	if string(left) != string(right) {
		t.Fatal("same persona and seed produced different simulation results")
	}
	if a.Seed != 42 || a.Attempts != 120 || a.Sessions != 6 {
		t.Fatalf("unexpected run metadata: %#v", a)
	}
}

func TestSimulationUsesIsolatedStoreAndRejectsProductionPath(t *testing.T) {
	prod := t.TempDir()
	t.Setenv("ENGLISH_PRACTICE_DATA", prod)
	c := simulationTestConfig("beginner", 10)
	if _, err := RunSimulation(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if _, err := RunSimulation(context.Background(), SimulationConfig{Mode: SimulationModeAlgorithm, Persona: "beginner", Attempts: 1, SessionSize: 1, Seed: 1, TimeProfile: SimulationTimeDaily, MaxTokens: 1, Timeout: time.Second, SimulationDBPath: filepath.Join(prod, "english-practice.db")}); err == nil {
		t.Fatal("production database path was accepted")
	}
}

func TestSimulationVirtualTimeProfiles(t *testing.T) {
	for _, tc := range []struct {
		profile string
		want    float64
	}{{SimulationTimeSameDay, 0.4}, {SimulationTimeDaily, 3.4}, {SimulationTimeWeekly, 21.4}} {
		c := simulationTestConfig("stable-intermediate", 40)
		c.SessionSize = 10
		c.TimeProfile = tc.profile
		r, err := RunSimulation(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		if r.VirtualDays < tc.want-0.05 || r.VirtualDays > tc.want+0.05 {
			t.Fatalf("profile %s: got %.2f days, want %.2f", tc.profile, r.VirtualDays, tc.want)
		}
	}
}

func TestSimulationPersonaRelativeBehavior(t *testing.T) {
	beginner, err := RunSimulation(context.Background(), simulationTestConfig("beginner", 300))
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := RunSimulation(context.Background(), simulationTestConfig("advanced-uneven", 300))
	if err != nil {
		t.Fatal(err)
	}
	if beginner.Metrics.FoundationExposureRatio <= advanced.Metrics.FoundationExposureRatio {
		t.Fatalf("beginner should see more foundation material: beginner %.3f advanced %.3f", beginner.Metrics.FoundationExposureRatio, advanced.Metrics.FoundationExposureRatio)
	}
	if advanced.Metrics.AccuracyByScene["phone"] >= advanced.Metrics.AccuracyByScene["daily"] {
		t.Fatalf("advanced uneven phone weakness not visible: %#v", advanced.Metrics.AccuracyByScene)
	}
	if beginner.Metrics.MaxConsecutiveSamePattern > 2 {
		t.Fatalf("interleaving policy exceeded: %d", beginner.Metrics.MaxConsecutiveSamePattern)
	}
}

func TestSimulationMemoryAndTransferMetrics(t *testing.T) {
	c := simulationTestConfig("forgetful", 240)
	c.TimeProfile = SimulationTimeWeekly
	c.SessionSize = 20
	r, err := RunSimulation(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Metrics.RetentionTrajectory) != r.Attempts {
		t.Fatalf("retention trajectory length=%d attempts=%d", len(r.Metrics.RetentionTrajectory), r.Attempts)
	}
	if r.Metrics.TransferEvents == 0 {
		t.Fatal("expected cross-scene transfer events")
	}
	maxInterval := 0.0
	for _, interval := range r.Metrics.MemoryIntervalAfter {
		if interval > maxInterval {
			maxInterval = interval
		}
	}
	if maxInterval <= r.Metrics.MemoryIntervalAfter[0] {
		t.Fatal("successful review interval did not expand")
	}
}

type recordingChatClient struct {
	request  ChatRequest
	response string
	err      error
}

func (c *recordingChatClient) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	c.request = req
	if c.err != nil {
		return nil, c.err
	}
	return &ChatResponse{Content: c.response, Provider: "fake", Model: "fake-model"}, nil
}

func TestLLMLearnerAdapterSeparatesPromptAndHandlesFailures(t *testing.T) {
	client := &recordingChatClient{response: "I would call tomorrow."}
	adapter := LLMLearnerSimulator{Client: client, MaxTokens: 64}
	answer, err := adapter.Answer(context.Background(), SimulationExercise{ChinesePrompt: "我明天会打电话。", SceneID: "phone", DifficultyBand: "appropriate"}, SimulatedLearnerState{PersonaID: "stable-intermediate", HistorySummary: "one previous attempt"})
	if err != nil || answer.Text == "" {
		t.Fatalf("unexpected learner result: %#v %v", answer, err)
	}
	joined := client.request.Messages[1].Content
	if strings.Contains(strings.ToLower(joined), "reference") || strings.Contains(strings.ToLower(joined), "mastery formula") {
		t.Fatalf("learner prompt leaked evaluator information: %s", joined)
	}
	client.err = errors.New("provider 500")
	if _, err := adapter.Answer(context.Background(), SimulationExercise{}, SimulatedLearnerState{}); err == nil {
		t.Fatal("provider error was swallowed")
	}
	if _, err := (FakeLearner{}).Answer(context.Background(), SimulationExercise{}, SimulatedLearnerState{}); err == nil {
		t.Fatal("empty fake answer was accepted")
	}
}

func TestDryRunReportsCallsWithoutProvider(t *testing.T) {
	c := simulationTestConfig("stable-intermediate", 50)
	c.Mode = SimulationModeLLMLearner
	c.DryRun = true
	c.LearnerProvider = "openai"
	c.LearnerModel = "example"
	r, err := RunSimulation(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.DryRun || r.AI.TotalCalls != 100 || !strings.Contains(r.AI.Status, "not called") {
		t.Fatalf("bad dry-run report: %#v", r.AI)
	}
}

func TestAIAdaptersKeepProviderFailuresOutOfLearnerAccuracy(t *testing.T) {
	c := simulationTestConfig("stable-intermediate", 6)
	c.Mode = SimulationModeLLMLearner
	runner := &SimulationRunner{Learner: FakeLearner{AnswerText: "I would call tomorrow."}, Evaluator: FakeEvaluator{Result: SimulationEvaluation{Correct: true}}}
	r, err := runner.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.AI.LearnerCalls != 6 || r.AI.EvaluatorCalls != 6 || r.Metrics.OverallAccuracy != 1 {
		t.Fatalf("unexpected AI run: %#v", r)
	}
	failed := &SimulationRunner{Learner: FakeLearner{Err: errors.New("provider 500")}, Evaluator: FakeEvaluator{Result: SimulationEvaluation{Correct: true}}}
	r, err = failed.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.OverallAccuracy != 0 {
		t.Fatalf("provider failures became learner errors: %.2f", r.Metrics.OverallAccuracy)
	}
	for _, attempt := range r.AttemptsTrace {
		if attempt.Verdict != "system_failure" || attempt.ErrorKind != "provider_error" {
			t.Fatalf("bad failure classification: %#v", attempt)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

type sequenceChatClient struct {
	responses []string
	errors    []error
	requests  []ChatRequest
}

func (c *sequenceChatClient) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	c.requests = append(c.requests, req)
	index := len(c.requests) - 1
	if index < len(c.errors) && c.errors[index] != nil {
		return nil, c.errors[index]
	}
	if index >= len(c.responses) {
		return nil, errors.New("sequence client exhausted")
	}
	return &ChatResponse{Content: c.responses[index], Provider: "fake", Model: "fake-model"}, nil
}

type acceptanceFakeGenerator struct {
	calls int
}

func (g *acceptanceFakeGenerator) Generate(_ context.Context, ex SimulationExercise) (SimulationExercise, error) {
	g.calls++
	ex.ChinesePrompt = "话虽如此，我认为我们应该等待。"
	return ex, nil
}

func (c *recordingChatClient) Chat(_ context.Context, req ChatRequest) (*ChatResponse, error) {
	c.request = req
	if c.err != nil {
		return nil, c.err
	}
	return &ChatResponse{Content: c.response, Provider: "fake", Model: "fake-model"}, nil
}

func validGeneratorJSON(prompt string) string {
	return fmt.Sprintf(`{"chinese_prompt":%q}`, prompt)
}

func generatorTestExercise() SimulationExercise {
	return SimulationExercise{ID: "generator-test", ChinesePrompt: "", PatternID: "having-said-that", Pattern: "Having said that, ...", SceneID: "meeting", SubsceneID: "weekly-review", Intent: "contrast", DifficultyBand: "challenging", Difficulty: 6.5}
}

func TestGeneratorReliabilityRepairsMalformedAndRetriesEmpty(t *testing.T) {
	malformed := &sequenceChatClient{responses: []string{`{"chinese_prompt":`, validGeneratorJSON("项目会议中的修复后保留意见练习")}}
	g := LLMExerciseGenerator{Client: malformed, MaxTokens: 64}
	exercise, diag, err := g.GenerateDetailed(context.Background(), generatorTestExercise())
	if err != nil || exercise.ChinesePrompt != "项目会议中的修复后保留意见练习" {
		t.Fatalf("malformed response was not repaired: exercise=%#v diag=%#v err=%v", exercise, diag, err)
	}
	if diag.ProviderCalls != 2 || diag.RepairAttempts != 1 || diag.FinalSource != "repaired" || diag.FailureKinds[0] != GeneratorFailureTruncatedJSON {
		t.Fatalf("unexpected repair diagnostics: %#v", diag)
	}

	empty := &sequenceChatClient{responses: []string{"", validGeneratorJSON("项目会议中的新的保留意见练习")}}
	g = LLMExerciseGenerator{Client: empty, MaxTokens: 64}
	exercise, diag, err = g.GenerateDetailed(context.Background(), generatorTestExercise())
	if err != nil || exercise.ChinesePrompt != "项目会议中的新的保留意见练习" {
		t.Fatalf("empty response was not fresh-retried: exercise=%#v diag=%#v err=%v", exercise, diag, err)
	}
	if diag.ProviderCalls != 2 || diag.RepairAttempts != 0 || diag.FreshRetries != 1 || diag.FinalSource != "regenerated" || diag.FailureKinds[0] != GeneratorFailureEmptyResponse {
		t.Fatalf("unexpected empty-response diagnostics: %#v", diag)
	}
}

func TestGeneratorReliabilityBindsApplicationMetadataAndIgnoresLegacyFields(t *testing.T) {
	client := &sequenceChatClient{responses: []string{
		`{"chinese_prompt":"项目会议中的真实保留意见练习","scene":"travel","intent":"wrong","pattern":"wrong","target_difficulty":1}`,
	}}
	g := LLMExerciseGenerator{Client: client, MaxTokens: 64}
	exercise, diag, err := g.GenerateDetailed(context.Background(), generatorTestExercise())
	if err != nil || exercise.SceneID != "meeting" || exercise.Intent != "contrast" || exercise.PatternID != "having-said-that" || exercise.Difficulty != 6.5 || exercise.ChinesePrompt != "项目会议中的真实保留意见练习" {
		t.Fatalf("application metadata was not authoritative: exercise=%#v diag=%#v err=%v", exercise, diag, err)
	}
	if diag.ProviderCalls != 1 || !diag.InitialSuccess || diag.FinalSource != "real" || diag.ContractVersion != generatorContractVersion {
		t.Fatalf("legacy metadata should not force a retry: %#v", diag)
	}
}

func TestGeneratorStructuredOutputAcceptsFenceAndClassifiesSchema(t *testing.T) {
	ex := generatorTestExercise()
	fenced := "leading text\n```json\n" + validGeneratorJSON("项目会议中的带代码围栏保留意见练习") + "\n```\ntrailing text"
	parsed, err := parseGeneratedExercise(fenced, ex)
	if err != nil || parsed.ChinesePrompt != "项目会议中的带代码围栏保留意见练习" {
		t.Fatalf("safe fenced extraction failed: %#v %v", parsed, err)
	}
	parsed, err = parseGeneratedExercise(`{"chinese_prompt":"项目会议中的尾随逗号保留意见练习",}`, ex)
	if err != nil || parsed.ChinesePrompt != "项目会议中的尾随逗号保留意见练习" {
		t.Fatalf("deterministic trailing-comma repair failed: %#v %v", parsed, err)
	}
	_, err = parseGeneratedExercise(`{"scene":"meeting"}`, ex)
	if generatorFailureKind(err) != GeneratorFailureSchemaInvalid {
		t.Fatalf("missing required field was not schema_invalid: %v", err)
	}
	_, err = parseGeneratedExercise(`{"chinese_prompt":}`, ex)
	if generatorFailureKind(err) != GeneratorFailureMalformedJSON {
		t.Fatalf("complete malformed JSON was not malformed_json: %v", err)
	}
	_, err = parseGeneratedExercise(`{"chinese_prompt":"truncated"`, ex)
	if generatorFailureKind(err) != GeneratorFailureTruncatedJSON {
		t.Fatalf("truncated JSON was not truncated_json: %v", err)
	}
}

func TestMinimalGeneratorContractBindsMetadataAndReferenceAnswers(t *testing.T) {
	if !promptSupportsIntent("contrast", "项目会议中的保留意见") {
		t.Fatal("contrast intent hints were not recognized")
	}
	ex := generatorTestExercise()
	parsed, err := parseGeneratedExercise(`{"chinese_prompt":"请在项目会议中委婉表达你的保留意见。","reference_answers":["I see your point, but I still have some concerns."]}`, ex)
	if err != nil {
		t.Fatalf("minimal contract was rejected: %v", err)
	}
	if parsed.SceneID != ex.SceneID || parsed.SubsceneID != ex.SubsceneID || parsed.Intent != ex.Intent || parsed.PatternID != ex.PatternID || parsed.Difficulty != ex.Difficulty {
		t.Fatalf("application-owned metadata was not bound from the spec: %#v", parsed)
	}
	if len(parsed.ReferenceAnswers) != 1 || parsed.ReferenceAnswers[0] == "" {
		t.Fatalf("reference answer was not retained: %#v", parsed.ReferenceAnswers)
	}
	if _, err := parseGeneratedExercise(`{"chinese_prompt":"会议中的平衡意见练习","scene":"travel","intent":"travel","pattern":"wrong","target_difficulty":1}`, ex); err != nil {
		t.Fatalf("legacy provider metadata should be ignored by the minimal contract: %v", err)
	}
	if _, err := parseGeneratedExercise(`{"chinese_prompt":"会议练习，使用 Having said that, ..."}`, ex); generatorFailureKind(err) != GeneratorFailureConstraintViolation {
		t.Fatalf("answer leakage was not rejected semantically: %v", err)
	}
}

func TestDeepSeekCompatibleAssistantContentExtraction(t *testing.T) {
	finalJSON := `{"chinese_prompt":"请在会议中表达一个平衡意见。"}`
	encodedFinal, _ := json.Marshal(finalJSON)
	finalContent := string(encodedFinal)
	tests := []struct {
		name       string
		body       string
		wantSource string
		wantKind   string
		wantReason bool
	}{
		{name: "normal content", body: `{"choices":[{"message":{"role":"assistant","content":` + finalContent + `},"finish_reason":"stop"}]}`, wantSource: "choices.message.content"},
		{name: "content plus reasoning", body: `{"choices":[{"message":{"role":"assistant","content":` + finalContent + `,"reasoning_content":"internal reasoning"},"finish_reason":"stop"}]}`, wantSource: "choices.message.content", wantReason: true},
		{name: "reasoning only", body: `{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"internal reasoning"},"finish_reason":"length"}]}`, wantKind: "reasoning_only", wantReason: true},
		{name: "provider empty", body: `{"choices":[{"message":{"role":"assistant","content":null},"finish_reason":"stop"}]}`, wantKind: "provider_empty"},
		{name: "wrong field", body: `{"choices":[{"message":{"role":"assistant","answer":` + finalContent + `},"finish_reason":"stop"}]}`, wantKind: "valid_content_wrong_field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, meta, err := extractAssistantContentDetailed([]byte(tt.body))
			if tt.wantKind != "" {
				var extractionErr *AssistantContentError
				if err == nil || !errors.As(err, &extractionErr) || extractionErr.Category != tt.wantKind {
					t.Fatalf("unexpected extraction result: content=%q meta=%#v err=%v", content, meta, err)
				}
			} else if err != nil || content == "" || meta.ContentSource != tt.wantSource {
				t.Fatalf("unexpected successful extraction: content=%q meta=%#v err=%v", content, meta, err)
			}
			if meta.ReasoningPresent != tt.wantReason {
				t.Fatalf("reasoning presence mismatch: got=%v want=%v", meta.ReasoningPresent, tt.wantReason)
			}
		})
	}
}

func TestGeneratorProviderRetryPolicyDoesNotBlindRetry4xx(t *testing.T) {
	client := &sequenceChatClient{errors: []error{&ProviderError{Category: "provider_4xx", HTTPStatus: 400, Err: errors.New("bad request")}}}
	g := LLMExerciseGenerator{Client: client, MaxTokens: 64}
	_, diag, err := g.GenerateDetailed(context.Background(), generatorTestExercise())
	if generatorFailureKind(err) != GeneratorFailureProviderError || diag.ProviderCalls != 1 || diag.FreshRetries != 0 || diag.RepairAttempts != 0 {
		t.Fatalf("4xx provider error was retried blindly: diag=%#v err=%v", diag, err)
	}
}

type exhaustedDetailedGenerator struct{}

func (exhaustedDetailedGenerator) Generate(context.Context, SimulationExercise) (SimulationExercise, error) {
	return SimulationExercise{}, errors.New("unused basic generator path")
}

func (exhaustedDetailedGenerator) GenerateDetailed(context.Context, SimulationExercise) (SimulationExercise, GenerationDiagnostics, error) {
	d := GenerationDiagnostics{GenerationID: "exhausted", InitialCalls: 1, ProviderCalls: 3, FailureKinds: []GeneratorFailureKind{GeneratorFailureTruncatedJSON, GeneratorFailureSchemaInvalid}}
	d.LastFailureKind = GeneratorFailureSchemaInvalid
	return SimulationExercise{}, d, generatorFailure(GeneratorFailureSchemaInvalid, errors.New("all retries exhausted"))
}

func TestGeneratorFallbackDoesNotCreateLearnerFailureOrDoubleUpdate(t *testing.T) {
	runner := &SimulationRunner{
		Generator: exhaustedDetailedGenerator{},
		Learner:   FakeLearner{AnswerText: "Having said that, we should wait."},
		Evaluator: FakeEvaluator{Result: SimulationEvaluation{Correct: true, Verdict: "correct", Meaning: .9, Grammar: .9, Naturalness: .9, Pattern: .9, TargetPatternMatch: TargetPatternExact, TargetPatternScore: 1}},
	}
	c := simulationTestConfig("stable-intermediate", 1)
	c.Mode, c.Generator, c.Scene = SimulationModeFullAI, "real-generator", "meeting"
	r, err := runner.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.GeneratorFallbackCount != 1 || r.Metrics.GeneratorFinalDeliveryCount != 1 || r.Metrics.SystemFailures != 0 {
		t.Fatalf("fallback polluted delivery/system metrics: %#v", r.Metrics)
	}
	if r.Metrics.AdaptiveStateUpdates != 1 || r.AI.LearnerCalls != 1 || r.AI.EvaluatorCalls != 1 {
		t.Fatalf("fallback caused duplicate or missing state transition: usage=%#v metrics=%#v", r.AI, r.Metrics)
	}
	if r.AttemptsTrace[0].Generation.FinalSource != "fallback" || r.AttemptsTrace[0].Generation.FallbackUsed != true {
		t.Fatalf("fallback source was not recorded: %#v", r.AttemptsTrace[0].Generation)
	}
}

func TestGeneratorFallbackLongRunPreservesDeliveryAndReplanIntegrity(t *testing.T) {
	runner := &SimulationRunner{
		Generator: exhaustedDetailedGenerator{},
		Learner:   FakeLearner{AnswerText: "Having said that, we should wait."},
		Evaluator: FakeEvaluator{Result: SimulationEvaluation{Correct: true, Verdict: "correct", Meaning: .9, Grammar: .9, Naturalness: .9, Pattern: .9, TargetPatternMatch: TargetPatternExact, TargetPatternScore: 1}},
	}
	c := simulationTestConfig("advanced-uneven", 30)
	c.Mode, c.Generator, c.Scene, c.SessionSize = SimulationModeFullAI, "real-generator", "meeting", 15
	r, err := runner.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.GeneratorFinalDeliveryRate != 1 || r.Metrics.GeneratorFallbackCount != 30 || r.Metrics.SystemFailures != 0 {
		t.Fatalf("long fallback run did not deliver safely: %#v", r.Metrics)
	}
	if r.Metrics.AdaptiveStateUpdates != 30 || r.Metrics.AdaptiveReplanEligible != 29 || r.Metrics.AdaptiveNextExerciseReplans != 29 || r.Metrics.AdaptiveReplanMisses != 0 {
		t.Fatalf("long fallback run changed state/replan semantics: %#v", r.Metrics)
	}
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
	client.err = context.DeadlineExceeded
	if _, err := adapter.Answer(context.Background(), SimulationExercise{}, SimulatedLearnerState{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error was not preserved: %v", err)
	}
	if _, err := (FakeLearner{}).Answer(context.Background(), SimulationExercise{}, SimulatedLearnerState{}); err == nil {
		t.Fatal("empty fake answer was accepted")
	}
}

func TestLLMLearnerBoundedRetryPreservesPersonaAndDoesNotLeakAnswers(t *testing.T) {
	client := &sequenceChatClient{
		responses: []string{"", "I would call tomorrow."},
	}
	adapter := LLMLearnerSimulator{Client: client, MaxTokens: 64}
	answer, err := adapter.Answer(context.Background(), SimulationExercise{ChinesePrompt: "请明天给客户打电话。", SceneID: "phone", DifficultyBand: "appropriate"}, SimulatedLearnerState{PersonaID: "stable-intermediate", HistorySummary: "previous answer"})
	if err != nil || answer.Text != "I would call tomorrow." || !answer.Reliability.Retry || answer.Reliability.ProviderCalls != 2 {
		t.Fatalf("learner retry did not recover: answer=%#v err=%v", answer, err)
	}
	if len(client.requests) != 2 || client.requests[0].Messages[1].Content != client.requests[1].Messages[1].Content {
		t.Fatal("learner retry changed the exercise/persona context")
	}
	for _, req := range client.requests {
		content := strings.ToLower(req.Messages[1].Content)
		if strings.Contains(content, "reference answer") || strings.Contains(content, "correct answer") || strings.Contains(content, "evaluation") {
			t.Fatalf("learner retry leaked evaluator information: %s", content)
		}
	}
}

func TestLLMLearnerFailureInjectionIsBoundedAndTyped(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "reasoning-only", err: &ProviderError{Category: "reasoning_only", Err: errors.New("reasoning only")}, want: LearnerFailureReasoningOnly},
		{name: "timeout", err: context.DeadlineExceeded, want: LearnerFailureProviderTimeout},
		{name: "http-500", err: &ProviderError{Category: "provider_5xx", HTTPStatus: 500, Err: errors.New("500")}, want: LearnerFailureProviderHTTPError},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &sequenceChatClient{responses: []string{"", "valid after retry"}, errors: []error{tc.err}}
			answer, err := (LLMLearnerSimulator{Client: client, MaxTokens: 64}).Answer(context.Background(), SimulationExercise{ChinesePrompt: "练习", SceneID: "daily"}, SimulatedLearnerState{PersonaID: "beginner"})
			if err != nil || answer.Text == "" || !answer.Reliability.Retry || len(client.requests) != 2 {
				t.Fatalf("%s did not recover: answer=%#v err=%v calls=%d", tc.name, answer, err, len(client.requests))
			}
		})
	}
	client := &sequenceChatClient{errors: []error{errors.New("provider 500"), errors.New("provider 500")}}
	_, err := (LLMLearnerSimulator{Client: client}).Answer(context.Background(), SimulationExercise{ChinesePrompt: "练习"}, SimulatedLearnerState{PersonaID: "beginner"})
	var providerErr *SimulationProviderError
	if err == nil || !errors.As(err, &providerErr) || providerErr.ProviderCalls != 2 || len(client.requests) != 2 {
		t.Fatalf("learner retry budget was not bounded: err=%v calls=%d", err, len(client.requests))
	}
}

func TestProductionEvaluatorRetriesRepairAndFreshFailureWithoutUnboundedCalls(t *testing.T) {
	valid := validEvaluationJSON()
	for _, tc := range []struct {
		name       string
		first      string
		wantRepair bool
		wantRetry  bool
	}{
		{name: "schema repair", first: `{"verdict":"correct"`, wantRepair: true},
		{name: "reasoning-only fresh retry", first: `{"choices":[{"message":{"content":"","reasoning_content":"internal"},"finish_reason":"length"}]}`, wantRetry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if calls == 1 && strings.HasPrefix(tc.first, "{") && strings.Contains(tc.first, "choices") {
					_, _ = w.Write([]byte(tc.first))
					return
				}
				content := tc.first
				if calls > 1 {
					content = valid
				}
				_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)
			}))
			defer server.Close()
			provider := ProviderConfig{ID: "eval", Type: "openai-compatible", BaseURL: server.URL, Model: "mock", Timeout: 2, Enabled: true}
			s := &Server{llm: &LLMRegistry{configs: map[string]ProviderConfig{"eval": provider}}}
			result, err := (ProductionSimulationEvaluator{Server: s, Provider: "eval"}).Evaluate(context.Background(), SimulationExercise{ChinesePrompt: "练习", PatternID: "having-said-that"}, SimulatedAnswer{Text: "That said, we should wait."})
			if err != nil || calls != 2 || result.Reliability.ProviderCalls != 2 || result.Reliability.Repair != tc.wantRepair || result.Reliability.Retry != tc.wantRetry {
				t.Fatalf("unexpected evaluator recovery: result=%#v err=%v calls=%d", result, err, calls)
			}
		})
	}

	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, `{"verdict":"not-a-verdict"}`)
	}))
	defer server.Close()
	provider := ProviderConfig{ID: "eval", Type: "openai-compatible", BaseURL: server.URL, Model: "mock", Timeout: 2, Enabled: true}
	s := &Server{llm: &LLMRegistry{configs: map[string]ProviderConfig{"eval": provider}}}
	_, err := (ProductionSimulationEvaluator{Server: s, Provider: "eval"}).Evaluate(context.Background(), SimulationExercise{ChinesePrompt: "练习", PatternID: "having-said-that"}, SimulatedAnswer{Text: "That said, we should wait."})
	var providerErr *SimulationProviderError
	if err == nil || !errors.As(err, &providerErr) || providerErr.ProviderCalls != 3 || calls != 3 || providerErr.Kind != EvaluatorFailureInvalidEnum {
		t.Fatalf("evaluator retry budget/classification failed: err=%v calls=%d", err, calls)
	}
}

func TestFullAIAdaptersClassifyGeneratorAndEvaluatorTimeouts(t *testing.T) {
	client := &recordingChatClient{err: context.DeadlineExceeded}
	generator := LLMExerciseGenerator{Client: client}
	if _, err := generator.Generate(context.Background(), SimulationExercise{Pattern: "Having said that, ...", SceneID: "meeting", Intent: "contrast", DifficultyBand: "challenging"}); classifySimulationFailure(err) != "timeout" {
		t.Fatalf("generator timeout was not classified: %v", err)
	}

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer mock.Close()
	provider := ProviderConfig{ID: "evaluator-timeout", Name: "Evaluator Timeout", Type: "openai-compatible", BaseURL: mock.URL, Model: "mock", Timeout: 1, Enabled: true}
	server := &Server{llm: &LLMRegistry{configs: map[string]ProviderConfig{provider.ID: provider}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := (ProductionSimulationEvaluator{Server: server, Provider: provider.ID}).Evaluate(ctx, SimulationExercise{ChinesePrompt: "话虽如此，我仍然认为我们应该等待。", PatternID: "having-said-that", Pattern: "Having said that, ...", SceneID: "meeting"}, SimulatedAnswer{Text: "That said, I still think we should wait."})
	if classifySimulationFailure(err) != "timeout" {
		t.Fatalf("evaluator timeout was not classified: %v", err)
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

func TestFullAIChainRunsGeneratorLearnerEvaluatorAndStateUpdate(t *testing.T) {
	generator := &acceptanceFakeGenerator{}
	runner := &SimulationRunner{
		Generator: generator,
		Learner:   FakeLearner{AnswerText: "That said, I still think we should wait."},
		Evaluator: FakeEvaluator{Result: SimulationEvaluation{Correct: true, Verdict: "correct", Meaning: .95, Grammar: .95, Naturalness: .9, Pattern: .9, TargetPatternMatch: TargetPatternSemanticEquivalent, TargetPatternScore: .9}},
	}
	c := simulationTestConfig("advanced-uneven", 4)
	c.Mode, c.Generator, c.SessionSize, c.Scene = SimulationModeFullAI, "real-generator", 2, "meeting"
	r, err := runner.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 4 || r.AI.GeneratorCalls != 4 || r.AI.LearnerCalls != 4 || r.AI.EvaluatorCalls != 4 || r.AI.TotalCalls != 12 {
		t.Fatalf("full-AI call chain incomplete: generator=%d usage=%#v", generator.calls, r.AI)
	}
	if r.Metrics.AdaptiveStateUpdates != 4 || r.Metrics.AdaptiveNextExerciseReplans == 0 || r.Metrics.PatternMatchCounts[TargetPatternSemanticEquivalent] != 4 {
		t.Fatalf("full-AI adaptive state or semantic evidence not recorded: %#v", r.Metrics)
	}
	if r.Metrics.AdaptiveReplanEligible != 3 || r.Metrics.AdaptiveNextExerciseReplans != 3 || r.Metrics.AdaptiveReplanMisses != 0 {
		t.Fatalf("replan counters are ambiguous or inconsistent: %#v", r.Metrics)
	}
}

func TestLocalAcceptanceRunsCoverRunAAndRunBWithoutExternalCalls(t *testing.T) {
	learner := FakeLearner{AnswerText: "I would follow up tomorrow."}
	evaluator := FakeEvaluator{Result: SimulationEvaluation{Correct: true, Verdict: "correct", Meaning: 1, Grammar: .9, Naturalness: .85, Pattern: .9}}
	runner := &SimulationRunner{Learner: learner, Evaluator: evaluator}

	runA := simulationTestConfig("stable-intermediate", 20)
	runA.Mode, runA.SessionSize, runA.TimeProfile = SimulationModeLLMLearner, 10, SimulationTimeDaily
	a, err := runner.Run(context.Background(), runA)
	if err != nil {
		t.Fatal(err)
	}
	if a.Attempts != 20 || a.Sessions != 2 || a.AI.TotalCalls != 40 || a.Metrics.SystemFailures != 0 {
		t.Fatalf("Run A local acceptance incomplete: %#v", a)
	}
	for _, attempt := range a.AttemptsTrace {
		if strings.HasPrefix(attempt.Exercise.ChinesePrompt, "Express ") {
			t.Fatalf("fixture prompt is not a Chinese learner exercise: %q", attempt.Exercise.ChinesePrompt)
		}
	}

	runB := simulationTestConfig("advanced-uneven", 20)
	runB.Mode, runB.SessionSize, runB.Scene, runB.TimeProfile = SimulationModeLLMLearner, 10, "meeting", SimulationTimeDaily
	b, err := runner.Run(context.Background(), runB)
	if err != nil {
		t.Fatal(err)
	}
	if b.Attempts != 20 || b.Sessions != 2 || b.AI.TotalCalls != 40 || b.Metrics.SystemFailures != 0 {
		t.Fatalf("Run B local acceptance incomplete: %#v", b)
	}
	if b.Metrics.SceneCoverage["meeting"] != 1 || b.Metrics.UniquePatterns < 2 || b.Metrics.MaxConsecutiveSamePattern > 2 {
		t.Fatalf("Run B scene diversity/stability failed: %#v", b.Metrics)
	}
}

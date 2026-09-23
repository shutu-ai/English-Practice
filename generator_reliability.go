package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GeneratorFailureKind is intentionally small and operational. It is used by
// diagnostics and retry policy, not as a second evaluator taxonomy.
type GeneratorFailureKind string

const (
	GeneratorFailureEmptyResponse       GeneratorFailureKind = "empty_response"
	GeneratorFailureMalformedJSON       GeneratorFailureKind = "malformed_json"
	GeneratorFailureTruncatedJSON       GeneratorFailureKind = "truncated_json"
	GeneratorFailureSchemaInvalid       GeneratorFailureKind = "schema_invalid"
	GeneratorFailureConstraintViolation GeneratorFailureKind = "semantic_constraint_violation"
	GeneratorFailureInvalidDifficulty   GeneratorFailureKind = "invalid_difficulty"
	GeneratorFailureSceneMismatch       GeneratorFailureKind = "scene_mismatch"
	GeneratorFailurePatternMismatch     GeneratorFailureKind = "pattern_mismatch"
	GeneratorFailureIntentMismatch      GeneratorFailureKind = "intent_mismatch"
	GeneratorFailureTimeout             GeneratorFailureKind = "timeout"
	GeneratorFailureProviderError       GeneratorFailureKind = "provider_error"
	GeneratorFailureUnknown             GeneratorFailureKind = "unknown"
)

type GeneratorFailure struct {
	Kind GeneratorFailureKind
	Err  error
}

func (e *GeneratorFailure) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

func (e *GeneratorFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func generatorFailure(kind GeneratorFailureKind, err error) error {
	if err == nil {
		err = errors.New(string(kind))
	}
	return &GeneratorFailure{Kind: kind, Err: err}
}

func generatorFailureKind(err error) GeneratorFailureKind {
	var failure *GeneratorFailure
	if errors.As(err, &failure) && failure.Kind != "" {
		return failure.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(errorText(err)), "deadline") || strings.Contains(strings.ToLower(errorText(err)), "timeout") {
		return GeneratorFailureTimeout
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		return GeneratorFailureProviderError
	}
	return GeneratorFailureUnknown
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// GenerationDiagnostics is stored per exercise. It deliberately excludes raw
// provider content and secrets while preserving the retry decision trace.
type GenerationDiagnostics struct {
	GenerationID    string                 `json:"generation_id"`
	InitialCalls    int                    `json:"initial_calls"`
	RepairAttempts  int                    `json:"repair_attempts"`
	FreshRetries    int                    `json:"fresh_retries"`
	ProviderCalls   int                    `json:"provider_calls"`
	InitialSuccess  bool                   `json:"initial_success"`
	RepairUsed      bool                   `json:"repair_used"`
	FreshRetryUsed  bool                   `json:"fresh_retry_used"`
	FallbackUsed    bool                   `json:"fallback_used"`
	FinalSource     string                 `json:"final_source"`
	FailureKinds    []GeneratorFailureKind `json:"failure_kinds,omitempty"`
	LastFailureKind GeneratorFailureKind   `json:"last_failure_kind,omitempty"`
}

type detailedExerciseGenerator interface {
	GenerateDetailed(context.Context, SimulationExercise) (SimulationExercise, GenerationDiagnostics, error)
}

type generatedExercisePayload struct {
	ChinesePrompt       string   `json:"chinese_prompt"`
	Scene               string   `json:"scene"`
	Subscene            string   `json:"subscene"`
	Intent              string   `json:"intent"`
	Pattern             string   `json:"pattern"`
	TargetPattern       string   `json:"target_pattern"`
	TargetDifficulty    *float64 `json:"target_difficulty"`
	EstimatedDifficulty *float64 `json:"estimated_difficulty"`
	RealizedDifficulty  *float64 `json:"realized_difficulty"`
	ReferenceAnswers    []string `json:"reference_answers"`
}

const generatorMaxProviderCalls = 3

func (g LLMExerciseGenerator) GenerateDetailed(ctx context.Context, ex SimulationExercise) (SimulationExercise, GenerationDiagnostics, error) {
	diag := GenerationDiagnostics{GenerationID: fmt.Sprintf("generation-%s-%d", ex.ID, time.Now().UnixNano())}
	if g.Client == nil {
		err := generatorFailure(GeneratorFailureProviderError, errors.New("exercise generator provider is not configured"))
		diag.recordFailure(err)
		return SimulationExercise{}, diag, err
	}

	generated, raw, err := g.generateOnce(ctx, ex, false)
	diag.InitialCalls++
	diag.ProviderCalls++
	if err == nil {
		diag.InitialSuccess = true
		diag.FinalSource = "real"
		return generated, diag, nil
	}
	diag.recordFailure(err)

	// Empty responses and provider timeouts are not repairable locally. A fresh
	// bounded call is safer than asking a repair prompt to invent missing data.
	kind := generatorFailureKind(err)
	if kind == GeneratorFailureProviderError && !generatorTransientProviderError(err) {
		return SimulationExercise{}, diag, err
	}
	if kind == GeneratorFailureEmptyResponse || kind == GeneratorFailureTimeout || kind == GeneratorFailureProviderError || !generatorRetryableProviderError(err) {
		return g.freshRetry(ctx, ex, &diag)
	}
	if kind == GeneratorFailureSceneMismatch || kind == GeneratorFailurePatternMismatch || kind == GeneratorFailureIntentMismatch || kind == GeneratorFailureConstraintViolation || kind == GeneratorFailureInvalidDifficulty {
		return g.freshRetry(ctx, ex, &diag)
	}

	// Markdown fences, leading prose, and a single malformed/truncated object
	// get one strict repair request. The repair input is bounded and sanitized.
	if diag.ProviderCalls < generatorMaxProviderCalls {
		diag.RepairUsed = true
		diag.RepairAttempts++
		repaired, repairErr := g.repairOnce(ctx, ex, raw, err)
		diag.ProviderCalls++
		if repairErr == nil {
			diag.FinalSource = "repaired"
			return repaired, diag, nil
		}
		diag.recordFailure(repairErr)
		if generatorFailureKind(repairErr) == GeneratorFailureProviderError && !generatorTransientProviderError(repairErr) {
			return SimulationExercise{}, diag, repairErr
		}
	}
	return g.freshRetry(ctx, ex, &diag)
}

func (d *GenerationDiagnostics) recordFailure(err error) {
	if d == nil || err == nil {
		return
	}
	kind := generatorFailureKind(err)
	d.LastFailureKind = kind
	d.FailureKinds = append(d.FailureKinds, kind)
}

func (g LLMExerciseGenerator) freshRetry(ctx context.Context, ex SimulationExercise, diag *GenerationDiagnostics) (SimulationExercise, GenerationDiagnostics, error) {
	if diag.ProviderCalls >= generatorMaxProviderCalls {
		return SimulationExercise{}, *diag, generatorFailure(GeneratorFailureUnknown, errors.New("generator retry budget exhausted"))
	}
	diag.FreshRetryUsed = true
	diag.FreshRetries++
	if err := generatorBackoff(ctx, diag.FreshRetries); err != nil {
		diag.recordFailure(generatorFailure(GeneratorFailureTimeout, err))
		return SimulationExercise{}, *diag, generatorFailure(GeneratorFailureTimeout, err)
	}
	generated, _, err := g.generateOnce(ctx, ex, true)
	diag.ProviderCalls++
	if err != nil {
		diag.recordFailure(err)
		return SimulationExercise{}, *diag, err
	}
	diag.FinalSource = "regenerated"
	return generated, *diag, nil
}

func (g LLMExerciseGenerator) generateOnce(ctx context.Context, ex SimulationExercise, fresh bool) (SimulationExercise, string, error) {
	maxTokens := g.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	system := "Return one JSON object only. Generate a natural, complete, adult-appropriate Chinese English-practice prompt and optional reference_answers. The program already owns scene, subscene, intent, pattern, and target difficulty; do not invent internal IDs. If you return metadata, it must exactly match the requested specification. Keep any estimated_difficulty and realized_difficulty numeric and within 1 to 8. Do not include markdown or prose outside the JSON object."
	if fresh {
		system += " This is a fresh generation after a failed response; preserve the requested scene, pattern, intent, and difficulty."
	}
	user := fmt.Sprintf("Requested scene: %s\nRequested subscene: %s\nRequested intent: %s\nRequested pattern: %s\nTarget difficulty: %.3f\nDifficulty band: %s", ex.SceneID, ex.SubsceneID, ex.Intent, ex.Pattern, ex.Difficulty, ex.DifficultyBand)
	resp, err := g.Client.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}, MaxTokens: maxTokens, JSONMode: true})
	if err != nil {
		return SimulationExercise{}, "", classifyGeneratorProviderError(err)
	}
	generated, err := parseGeneratedExercise(resp.Content, ex)
	return generated, resp.Content, err
}

func (g LLMExerciseGenerator) repairOnce(ctx context.Context, ex SimulationExercise, raw string, validationErr error) (SimulationExercise, error) {
	maxTokens := g.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	if len(raw) > 6000 {
		raw = raw[:6000]
	}
	expected := `{"chinese_prompt":"...","reference_answers":["..."]}`
	repairSystem := "Return corrected JSON only. Do not change the intended scene, subscene, intent, pattern, or target difficulty. Do not include markdown, explanations, internal IDs, or multiple JSON objects."
	repairUser := fmt.Sprintf("Invalid response:\n%s\n\nExpected schema:\n%s\n\nValidation error: %s", raw, expected, validationErr)
	resp, err := g.Client.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: repairSystem}, {Role: "user", Content: repairUser}}, MaxTokens: maxTokens, JSONMode: true})
	if err != nil {
		return SimulationExercise{}, classifyGeneratorProviderError(err)
	}
	generated, err := parseGeneratedExercise(resp.Content, ex)
	return generated, err
}

func classifyGeneratorProviderError(err error) error {
	if err == nil {
		return nil
	}
	kind := generatorFailureKind(err)
	if kind == GeneratorFailureTimeout {
		return generatorFailure(GeneratorFailureTimeout, err)
	}
	if kind == GeneratorFailureProviderError {
		return generatorFailure(GeneratorFailureProviderError, err)
	}
	return err
}

func generatorTransientProviderError(err error) bool {
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		return true
	}
	return providerErr.HTTPStatus == 0 || providerErr.HTTPStatus >= 500 || providerErr.Category == "timeout"
}

func generatorRetryableProviderError(err error) bool {
	kind := generatorFailureKind(err)
	return kind == GeneratorFailureMalformedJSON || kind == GeneratorFailureTruncatedJSON || kind == GeneratorFailureSchemaInvalid || kind == GeneratorFailureConstraintViolation || kind == GeneratorFailureInvalidDifficulty || kind == GeneratorFailureSceneMismatch || kind == GeneratorFailurePatternMismatch || kind == GeneratorFailureIntentMismatch || kind == GeneratorFailureTimeout || kind == GeneratorFailureProviderError
}

func generatorBackoff(ctx context.Context, retry int) error {
	delay := time.Duration(retry*25) * time.Millisecond
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseGeneratedExercise(raw string, ex SimulationExercise) (SimulationExercise, error) {
	if strings.TrimSpace(raw) == "" {
		return SimulationExercise{}, generatorFailure(GeneratorFailureEmptyResponse, errors.New("generator response was empty"))
	}
	clean, err := stripGeneratorJSON(raw)
	if err != nil {
		kind := GeneratorFailureMalformedJSON
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "empty") {
			kind = GeneratorFailureEmptyResponse
		} else if strings.Contains(lower, "fence") || (strings.Contains(lower, "complete") && !strings.HasSuffix(strings.TrimSpace(raw), "}")) {
			kind = GeneratorFailureTruncatedJSON
		}
		return SimulationExercise{}, generatorFailure(kind, err)
	}
	var payload generatedExercisePayload
	if err := json.Unmarshal([]byte(clean), &payload); err != nil {
		return SimulationExercise{}, generatorFailure(GeneratorFailureMalformedJSON, err)
	}
	if strings.TrimSpace(payload.ChinesePrompt) == "" {
		return SimulationExercise{}, generatorFailure(GeneratorFailureSchemaInvalid, errors.New("missing required chinese_prompt"))
	}
	if payload.Scene != "" && payload.Scene != ex.SceneID {
		return SimulationExercise{}, generatorFailure(GeneratorFailureSceneMismatch, fmt.Errorf("got %s, want %s", payload.Scene, ex.SceneID))
	}
	if payload.Subscene != "" && payload.Subscene != ex.SubsceneID {
		return SimulationExercise{}, generatorFailure(GeneratorFailureConstraintViolation, fmt.Errorf("subscene mismatch: got %s, want %s", payload.Subscene, ex.SubsceneID))
	}
	if payload.Intent != "" && payload.Intent != ex.Intent {
		return SimulationExercise{}, generatorFailure(GeneratorFailureIntentMismatch, fmt.Errorf("got %s, want %s", payload.Intent, ex.Intent))
	}
	pattern := strings.TrimSpace(payload.Pattern)
	if pattern == "" {
		pattern = strings.TrimSpace(payload.TargetPattern)
	}
	if pattern != "" && !generatorPatternMatches(pattern, ex) {
		return SimulationExercise{}, generatorFailure(GeneratorFailurePatternMismatch, fmt.Errorf("got %s, want %s", pattern, ex.Pattern))
	}
	for name, value := range map[string]*float64{"target_difficulty": payload.TargetDifficulty, "estimated_difficulty": payload.EstimatedDifficulty, "realized_difficulty": payload.RealizedDifficulty} {
		if value == nil {
			continue
		}
		if *value < 1 || *value > 8 {
			return SimulationExercise{}, generatorFailure(GeneratorFailureInvalidDifficulty, fmt.Errorf("%s %.3f outside 1..8", name, *value))
		}
		if name == "target_difficulty" && mathAbs(*value-ex.Difficulty) > .75 {
			return SimulationExercise{}, generatorFailure(GeneratorFailureConstraintViolation, fmt.Errorf("target difficulty %.3f differs from %.3f", *value, ex.Difficulty))
		}
	}
	// Selection metadata and target difficulty are authoritative in the
	// adaptive engine. The provider contributes learner-facing content only.
	return SimulationExercise{ID: ex.ID, ChinesePrompt: strings.TrimSpace(payload.ChinesePrompt), PatternID: ex.PatternID, Pattern: ex.Pattern, SceneID: ex.SceneID, SubsceneID: ex.SubsceneID, Intent: ex.Intent, DifficultyBand: ex.DifficultyBand, Difficulty: ex.Difficulty}, nil
}

func stripGeneratorJSON(raw string) (string, error) {
	clean, err := stripJSONFence(raw)
	if err == nil {
		return clean, nil
	}
	// A trailing comma is a deterministic, non-semantic formatting defect. We
	// repair only that exact pattern and run the same single-object extractor;
	// we never guess between multiple JSON candidates or invent fields.
	repaired := removeTrailingJSONCommas(raw)
	if repaired != raw {
		if clean, repairErr := stripJSONFence(repaired); repairErr == nil {
			return clean, nil
		}
	}
	return "", err
}

func removeTrailingJSONCommas(raw string) string {
	var out strings.Builder
	out.Grow(len(raw))
	inString, escaped := false, false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
			} else if ch == '\\' {
				escaped = true
			} else if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			continue
		}
		if ch == ',' {
			j := i + 1
			for j < len(raw) && (raw[j] == ' ' || raw[j] == '\n' || raw[j] == '\r' || raw[j] == '\t') {
				j++
			}
			if j < len(raw) && (raw[j] == '}' || raw[j] == ']') {
				continue
			}
		}
		out.WriteByte(ch)
	}
	return out.String()
}

func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func generatorPatternMatches(value string, ex SimulationExercise) bool {
	normalize := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.TrimSuffix(s, "...")
		s = strings.TrimSuffix(s, "…")
		s = strings.TrimSpace(strings.TrimRight(s, ".,;:!?"))
		return strings.Join(strings.Fields(s), " ")
	}
	got := normalize(value)
	want := normalize(ex.Pattern)
	return got == normalize(ex.PatternID) || got == want || (strings.HasSuffix(want, " ...") && got == strings.TrimSpace(strings.TrimSuffix(want, " ...")))
}

func fallbackSimulationExercise(ex SimulationExercise) SimulationExercise {
	if strings.TrimSpace(ex.ChinesePrompt) != "" {
		return ex
	}
	for _, ptn := range patternCatalog() {
		if ptn.id == ex.PatternID {
			ex.ChinesePrompt = simulationChinesePrompt(ptn, ex.SceneID)
			return ex
		}
	}
	ex.ChinesePrompt = fmt.Sprintf("请在%s场景中自然表达：%s。", ex.SceneID, ex.Pattern)
	return ex
}

func generateSimulationExercise(ctx context.Context, generator ExerciseGenerator, ex SimulationExercise) (SimulationExercise, GenerationDiagnostics, error) {
	if detailed, ok := generator.(detailedExerciseGenerator); ok {
		return detailed.GenerateDetailed(ctx, ex)
	}
	diag := GenerationDiagnostics{GenerationID: fmt.Sprintf("generation-%s-%d", ex.ID, time.Now().UnixNano()), InitialCalls: 1, ProviderCalls: 1}
	generated, err := generator.Generate(ctx, ex)
	if err != nil {
		diag.recordFailure(err)
		return SimulationExercise{}, diag, err
	}
	diag.InitialSuccess = true
	diag.FinalSource = "real"
	return generated, diag, nil
}

func recordGeneratorFailureMetric(metrics *SimulationMetrics, kind GeneratorFailureKind) {
	if metrics == nil {
		return
	}
	switch kind {
	case GeneratorFailureEmptyResponse:
		metrics.GeneratorEmptyResponseCount++
	case GeneratorFailureMalformedJSON:
		metrics.GeneratorMalformedJSONCount++
	case GeneratorFailureTruncatedJSON:
		metrics.GeneratorTruncatedJSONCount++
	case GeneratorFailureTimeout:
		metrics.GeneratorTimeoutCount++
	case GeneratorFailureSchemaInvalid:
		metrics.GeneratorSchemaInvalidCount++
	case GeneratorFailureConstraintViolation, GeneratorFailureInvalidDifficulty, GeneratorFailureSceneMismatch, GeneratorFailurePatternMismatch, GeneratorFailureIntentMismatch:
		metrics.GeneratorConstraintViolationCount++
		if kind == GeneratorFailureInvalidDifficulty {
			metrics.GeneratorDifficultyRejects++
		}
		if kind == GeneratorFailureSceneMismatch {
			metrics.GeneratorSceneMismatchCount++
		}
	}
}

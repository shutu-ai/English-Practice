package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// GeneratorFailureKind is intentionally small and operational. It is used by
// diagnostics and retry policy, not as a second evaluator taxonomy.
type GeneratorFailureKind string

const (
	GeneratorFailureEmptyResponse          GeneratorFailureKind = "empty_response"
	GeneratorFailureProviderEmpty          GeneratorFailureKind = "provider_empty"
	GeneratorFailureReasoningOnly          GeneratorFailureKind = "reasoning_only"
	GeneratorFailureAdapterEmpty           GeneratorFailureKind = "adapter_extraction_empty"
	GeneratorFailureValidContentWrongField GeneratorFailureKind = "valid_content_wrong_field"
	GeneratorFailureMalformedJSON          GeneratorFailureKind = "malformed_json"
	GeneratorFailureTruncatedJSON          GeneratorFailureKind = "truncated_json"
	GeneratorFailureSchemaInvalid          GeneratorFailureKind = "schema_invalid"
	GeneratorFailureConstraintViolation    GeneratorFailureKind = "semantic_constraint_violation"
	GeneratorFailureInvalidDifficulty      GeneratorFailureKind = "invalid_difficulty"
	GeneratorFailureSceneMismatch          GeneratorFailureKind = "scene_mismatch"
	GeneratorFailurePatternMismatch        GeneratorFailureKind = "pattern_mismatch"
	GeneratorFailureIntentMismatch         GeneratorFailureKind = "intent_mismatch"
	GeneratorFailureTimeout                GeneratorFailureKind = "timeout"
	GeneratorFailureProviderError          GeneratorFailureKind = "provider_error"
	GeneratorFailureUnknown                GeneratorFailureKind = "unknown"
)

type GeneratorFailure struct {
	Kind  GeneratorFailureKind
	Stage string
	Err   error
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
	return &GeneratorFailure{Kind: kind, Stage: generatorFailureStage(kind), Err: err}
}

func generatorFailureStage(kind GeneratorFailureKind) string {
	switch kind {
	case GeneratorFailureProviderEmpty, GeneratorFailureReasoningOnly, GeneratorFailureProviderError, GeneratorFailureTimeout:
		return "provider"
	case GeneratorFailureAdapterEmpty, GeneratorFailureValidContentWrongField:
		return "adapter_extraction"
	case GeneratorFailureMalformedJSON, GeneratorFailureTruncatedJSON, GeneratorFailureSchemaInvalid, GeneratorFailureEmptyResponse:
		return "structural"
	case GeneratorFailureConstraintViolation, GeneratorFailureInvalidDifficulty, GeneratorFailureSceneMismatch, GeneratorFailurePatternMismatch, GeneratorFailureIntentMismatch:
		return "semantic"
	default:
		return "unknown"
	}
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
		switch providerErr.Category {
		case "provider_empty":
			return GeneratorFailureProviderEmpty
		case "reasoning_only":
			return GeneratorFailureReasoningOnly
		case "adapter_extraction_empty":
			return GeneratorFailureAdapterEmpty
		case "valid_content_wrong_field":
			return GeneratorFailureValidContentWrongField
		}
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
	GenerationID         string                         `json:"generation_id"`
	ContractVersion      string                         `json:"contract_version"`
	Spec                 GenerationSpec                 `json:"spec"`
	InitialCalls         int                            `json:"initial_calls"`
	RepairAttempts       int                            `json:"repair_attempts"`
	FreshRetries         int                            `json:"fresh_retries"`
	ProviderCalls        int                            `json:"provider_calls"`
	InitialSuccess       bool                           `json:"initial_success"`
	RepairUsed           bool                           `json:"repair_used"`
	FreshRetryUsed       bool                           `json:"fresh_retry_used"`
	FallbackUsed         bool                           `json:"fallback_used"`
	FinalSource          string                         `json:"final_source"`
	FailureKinds         []GeneratorFailureKind         `json:"failure_kinds,omitempty"`
	FailureStages        []string                       `json:"failure_stages,omitempty"`
	LastFailureKind      GeneratorFailureKind           `json:"last_failure_kind,omitempty"`
	LastFailureStage     string                         `json:"last_failure_stage,omitempty"`
	StructuralValidation string                         `json:"structural_validation,omitempty"`
	SemanticValidation   string                         `json:"semantic_validation,omitempty"`
	Responses            []GeneratorResponseDiagnostics `json:"responses,omitempty"`
}

type GeneratorResponseDiagnostics struct {
	Attempt             int                  `json:"attempt"`
	ContentSource       string               `json:"content_source,omitempty"`
	FinishReason        string               `json:"finish_reason,omitempty"`
	ReasoningPresent    bool                 `json:"reasoning_present,omitempty"`
	ResponseBytes       int                  `json:"response_bytes,omitempty"`
	PromptBytes         int                  `json:"prompt_bytes,omitempty"`
	DeterministicRepair bool                 `json:"deterministic_repair,omitempty"`
	FailureKind         GeneratorFailureKind `json:"failure_kind,omitempty"`
	ValidationStage     string               `json:"validation_stage,omitempty"`
}

type detailedExerciseGenerator interface {
	GenerateDetailed(context.Context, SimulationExercise) (SimulationExercise, GenerationDiagnostics, error)
}

type generatedExercisePayload struct {
	ChinesePrompt      string   `json:"chinese_prompt"`
	ReferenceAnswers   []string `json:"reference_answers,omitempty"`
	AlternativeAnswers []string `json:"alternative_answers,omitempty"`
}

const generatorContractVersion = "generator-contract-v2"
const generatorMaxProviderCalls = 3

func (g LLMExerciseGenerator) GenerateDetailed(ctx context.Context, ex SimulationExercise) (SimulationExercise, GenerationDiagnostics, error) {
	diag := GenerationDiagnostics{GenerationID: fmt.Sprintf("generation-%s-%d", ex.ID, time.Now().UnixNano()), ContractVersion: generatorContractVersion, Spec: generationSpecFromExercise(ex)}
	if g.Client == nil {
		err := generatorFailure(GeneratorFailureProviderError, errors.New("exercise generator provider is not configured"))
		diag.recordFailure(err)
		return SimulationExercise{}, diag, err
	}

	generated, raw, responseDiag, err := g.generateOnce(ctx, ex, false)
	diag.recordResponse(responseDiag)
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
		repaired, repairDiag, repairErr := g.repairOnce(ctx, ex, raw, err)
		diag.recordResponse(repairDiag)
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
	d.LastFailureStage = generatorFailureStage(kind)
	d.FailureKinds = append(d.FailureKinds, kind)
	d.FailureStages = append(d.FailureStages, generatorFailureStage(kind))
}

func (d *GenerationDiagnostics) recordResponse(response GeneratorResponseDiagnostics) {
	if d == nil || (response.ContentSource == "" && response.ResponseBytes == 0 && response.FailureKind == "") {
		return
	}
	response.Attempt = len(d.Responses) + 1
	d.Responses = append(d.Responses, response)
	switch response.ValidationStage {
	case "structural":
		d.StructuralValidation = "failed"
	case "semantic":
		d.StructuralValidation = "passed"
		d.SemanticValidation = "failed"
	case "accepted":
		d.StructuralValidation = "passed"
		d.SemanticValidation = "passed"
	}
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
	generated, _, responseDiag, err := g.generateOnce(ctx, ex, true)
	diag.recordResponse(responseDiag)
	diag.ProviderCalls++
	if err != nil {
		diag.recordFailure(err)
		return SimulationExercise{}, *diag, err
	}
	diag.FinalSource = "regenerated"
	return generated, *diag, nil
}

func (g LLMExerciseGenerator) generateOnce(ctx context.Context, ex SimulationExercise, fresh bool) (SimulationExercise, string, GeneratorResponseDiagnostics, error) {
	maxTokens := g.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	system := "Generate one natural, complete, adult-appropriate Chinese English-practice exercise. The application already selected the scene, intent, target pattern, and target difficulty. Use that context, but do not repeat internal IDs or difficulty fields in your response. Return JSON only with this minimal schema: {\"chinese_prompt\":\"...\",\"reference_answers\":[\"one natural English answer\"]}. reference_answers may contain one primary answer and should not contain analysis. Do not include markdown, reasoning, or prose outside the JSON object. The Chinese prompt must not reveal the exact English answer or target expression."
	if fresh {
		system += " This is a fresh generation after a failed response; preserve the requested scene, pattern, intent, and difficulty."
	}
	spec := generationSpecFromExercise(ex)
	user := fmt.Sprintf("Training context\nScene: %s\nSubscene: %s\nCommunication intent: %s\nTarget pattern skill: %s\nTarget difficulty: %.3f (%s)\n\nGeneration requirements\n- Write a realistic adult situation in Chinese that naturally calls for the target skill.\n- Make the intent clear without giving an English answer hint.\n- Keep the prompt concise and complete.\n- Write one high-quality English reference answer that fulfills the situation and demonstrates the target skill.\n\nOutput contract\nReturn exactly one JSON object with chinese_prompt and reference_answers.", spec.SceneID, spec.SubsceneID, spec.Intent, spec.Pattern, spec.TargetDifficulty, spec.DifficultyBand)
	messages := []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}
	resp, err := g.Client.Chat(ctx, ChatRequest{Messages: messages, MaxTokens: maxTokens, JSONMode: true})
	responseDiag := GeneratorResponseDiagnostics{PromptBytes: chatPromptBytes(messages)}
	if err != nil {
		responseDiag = generatorResponseDiagnosticsFromError(responseDiag, err)
		return SimulationExercise{}, "", responseDiag, classifyGeneratorProviderError(err)
	}
	responseDiag.ContentSource = resp.ContentSource
	if responseDiag.ContentSource == "" {
		responseDiag.ContentSource = "chat_response.content"
	}
	responseDiag.FinishReason = resp.FinishReason
	responseDiag.ReasoningPresent = resp.ReasoningPresent
	responseDiag.ResponseBytes = resp.ResponseBytes
	if responseDiag.ResponseBytes == 0 {
		responseDiag.ResponseBytes = len(resp.Content)
	}
	responseDiag.DeterministicRepair = generatorDeterministicRepairUsed(resp.Content)
	generated, err := parseGeneratedExercise(resp.Content, ex)
	if err != nil {
		err = classifyGeneratorFinishReason(err, resp.FinishReason)
	}
	if err != nil {
		responseDiag.FailureKind = generatorFailureKind(err)
		responseDiag.ValidationStage = generatorFailureStage(responseDiag.FailureKind)
	} else {
		responseDiag.ValidationStage = "accepted"
	}
	return generated, resp.Content, responseDiag, err
}

func (g LLMExerciseGenerator) repairOnce(ctx context.Context, ex SimulationExercise, raw string, validationErr error) (SimulationExercise, GeneratorResponseDiagnostics, error) {
	maxTokens := g.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	if len(raw) > 6000 {
		raw = raw[:6000]
	}
	expected := `{"chinese_prompt":"...","reference_answers":["..."]}`
	repairSystem := "Return one corrected JSON object only using the minimal exercise contract. Do not include metadata, markdown, reasoning, explanations, or multiple objects."
	repairUser := fmt.Sprintf("Invalid response:\n%s\n\nExpected schema:\n%s\n\nValidation error: %s\nTraining context remains authoritative: scene=%s, subscene=%s, intent=%s, target pattern=%s, target difficulty=%.3f.", raw, expected, validationErr, ex.SceneID, ex.SubsceneID, ex.Intent, ex.Pattern, ex.Difficulty)
	messages := []ChatMessage{{Role: "system", Content: repairSystem}, {Role: "user", Content: repairUser}}
	resp, err := g.Client.Chat(ctx, ChatRequest{Messages: messages, MaxTokens: maxTokens, JSONMode: true})
	responseDiag := GeneratorResponseDiagnostics{PromptBytes: chatPromptBytes(messages)}
	if err != nil {
		responseDiag = generatorResponseDiagnosticsFromError(responseDiag, err)
		return SimulationExercise{}, responseDiag, classifyGeneratorProviderError(err)
	}
	responseDiag.ContentSource = resp.ContentSource
	if responseDiag.ContentSource == "" {
		responseDiag.ContentSource = "chat_response.content"
	}
	responseDiag.FinishReason = resp.FinishReason
	responseDiag.ReasoningPresent = resp.ReasoningPresent
	responseDiag.ResponseBytes = resp.ResponseBytes
	if responseDiag.ResponseBytes == 0 {
		responseDiag.ResponseBytes = len(resp.Content)
	}
	responseDiag.DeterministicRepair = generatorDeterministicRepairUsed(resp.Content)
	generated, err := parseGeneratedExercise(resp.Content, ex)
	if err != nil {
		err = classifyGeneratorFinishReason(err, resp.FinishReason)
	}
	if err != nil {
		responseDiag.FailureKind = generatorFailureKind(err)
		responseDiag.ValidationStage = generatorFailureStage(responseDiag.FailureKind)
	} else {
		responseDiag.ValidationStage = "accepted"
	}
	return generated, responseDiag, err
}

func chatPromptBytes(messages []ChatMessage) int {
	total := 0
	for _, message := range messages {
		total += len(message.Role) + len(message.Content)
	}
	return total
}

func generatorResponseDiagnosticsFromError(diag GeneratorResponseDiagnostics, err error) GeneratorResponseDiagnostics {
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		diag.ContentSource = providerErr.ContentSource
		diag.FinishReason = providerErr.FinishReason
		diag.ReasoningPresent = providerErr.ReasoningPresent
		diag.ResponseBytes = providerErr.ResponseBytes
	}
	diag.FailureKind = generatorFailureKind(err)
	diag.ValidationStage = generatorFailureStage(diag.FailureKind)
	return diag
}

func classifyGeneratorFinishReason(err error, finishReason string) error {
	if strings.EqualFold(strings.TrimSpace(finishReason), "length") {
		kind := generatorFailureKind(err)
		if kind == GeneratorFailureMalformedJSON || kind == GeneratorFailureSchemaInvalid || kind == GeneratorFailureTruncatedJSON {
			return generatorFailure(GeneratorFailureTruncatedJSON, fmt.Errorf("finish_reason=length: %w", err))
		}
	}
	return err
}

func generatorDeterministicRepairUsed(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}
	if clean, err := stripJSONFence(raw); err == nil {
		return clean != trimmed
	}
	repaired := removeTrailingJSONCommas(raw)
	if repaired == raw {
		return false
	}
	_, err := stripJSONFence(repaired)
	return err == nil
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
	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(clean), &object); err != nil {
		return SimulationExercise{}, generatorFailure(GeneratorFailureMalformedJSON, err)
	}
	var payload generatedExercisePayload
	if err := json.Unmarshal([]byte(clean), &payload); err != nil {
		return SimulationExercise{}, generatorFailure(GeneratorFailureSchemaInvalid, err)
	}
	if strings.TrimSpace(payload.ChinesePrompt) == "" {
		return SimulationExercise{}, generatorFailure(GeneratorFailureSchemaInvalid, errors.New("missing required chinese_prompt"))
	}
	prompt := strings.TrimSpace(payload.ChinesePrompt)
	if len(prompt) > 2000 {
		return SimulationExercise{}, generatorFailure(GeneratorFailureSchemaInvalid, errors.New("chinese_prompt exceeds 2000 bytes"))
	}
	if !containsHan(prompt) {
		return SimulationExercise{}, generatorFailure(GeneratorFailureConstraintViolation, errors.New("chinese_prompt is not a Chinese learner-facing situation"))
	}
	patternHint := strings.TrimSpace(strings.TrimSuffix(ex.Pattern, "..."))
	if len(patternHint) >= 8 && strings.Contains(strings.ToLower(prompt), strings.ToLower(patternHint)) {
		return SimulationExercise{}, generatorFailure(GeneratorFailureConstraintViolation, errors.New("chinese_prompt leaks the target English expression"))
	}
	answers := make([]string, 0, len(payload.ReferenceAnswers))
	for _, answer := range payload.ReferenceAnswers {
		answer = strings.TrimSpace(answer)
		if answer == "" {
			return SimulationExercise{}, generatorFailure(GeneratorFailureSchemaInvalid, errors.New("reference_answers contains an empty answer"))
		}
		if len(answer) > 1200 {
			return SimulationExercise{}, generatorFailure(GeneratorFailureSchemaInvalid, errors.New("reference answer exceeds 1200 bytes"))
		}
		answers = append(answers, answer)
	}
	if err := validateGeneratedSemantics(ex, prompt, answers); err != nil {
		return SimulationExercise{}, err
	}
	// The application-owned GenerationSpec is authoritative. The provider
	// contributes only linguistic fields; metadata from older providers is
	// intentionally ignored instead of being revalidated as a duplicate.
	return SimulationExercise{ID: ex.ID, ChinesePrompt: prompt, PatternID: ex.PatternID, Pattern: ex.Pattern, SceneID: ex.SceneID, SubsceneID: ex.SubsceneID, Intent: ex.Intent, DifficultyBand: ex.DifficultyBand, Difficulty: ex.Difficulty, ReferenceAnswers: answers}, nil
}

func containsHan(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

func validateGeneratedSemantics(ex SimulationExercise, prompt string, answers []string) error {
	lowerPrompt := strings.ToLower(prompt)
	if ex.SceneID == "meeting" || ex.SceneID == "work" {
		if !containsAnyTerm(lowerPrompt, "会议", "项目", "同事", "团队", "经理", "老板", "客户", "方案", "上线", "预算", "截止", "工作") {
			return generatorFailure(GeneratorFailureSceneMismatch, fmt.Errorf("prompt has no %s/work context", ex.SceneID))
		}
	}
	if !promptSupportsIntent(ex.Intent, lowerPrompt) {
		return generatorFailure(GeneratorFailureIntentMismatch, fmt.Errorf("prompt does not express intent %s", ex.Intent))
	}
	if len(answers) == 0 {
		return nil
	}
	answer := strings.ToLower(answers[0])
	if !referenceAnswerSupportsPattern(ex.PatternID, answer) {
		return generatorFailure(GeneratorFailurePatternMismatch, fmt.Errorf("reference answer does not demonstrate target pattern %s", ex.PatternID))
	}
	return nil
}

func promptSupportsIntent(intent, prompt string) bool {
	switch intent {
	case "condition":
		return containsAnyTerm(prompt, "\u5982\u679c", "\u9664\u975e", "\u524d\u63d0", "\u60c5\u51b5\u4e0b", "\u6761\u4ef6")
	case "disagreement":
		return containsAnyTerm(prompt, "\u4e0d\u540c\u610f", "\u987e\u8651", "\u62c5\u5fc3", "\u98ce\u9669", "\u4fdd\u7559", "\u53cd\u5bf9", "\u770b\u6cd5")
	case "contrast":
		return containsAnyTerm(prompt, "\u4f46", "\u4e0d\u8fc7", "\u7136\u800c", "\u867d\u7136", "\u8ba4\u53ef", "\u540c\u610f", "\u8f6c\u6298", "\u987e\u8651", "\u4fdd\u7559", "\u5e73\u8861")
	case "opinion":
		return containsAnyTerm(prompt, "\u770b\u6cd5", "\u89c2\u70b9", "\u610f\u89c1", "\u8ba4\u4e3a", "\u7591\u8651", "\u4fe1\u670d", "\u65b9\u6848")
	case "negotiation":
		return containsAnyTerm(prompt, "\u534f\u5546", "\u8c08\u5224", "\u80fd\u5426", "\u662f\u5426\u53ef\u4ee5", "\u8c03\u6574", "\u4ef7\u683c")
	default:
		return true
	}
}

func containsAnyTerm(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, strings.ToLower(term)) {
			return true
		}
	}
	return false
}

func referenceAnswerSupportsPattern(patternID, answer string) bool {
	answer = strings.ToLower(answer)
	switch patternID {
	case "conditional", "if-first", "if-second", "unless", "mixed-conditional":
		return (strings.Contains(answer, "if ") || strings.Contains(answer, "unless ")) && containsAnyTerm(answer, "would", "will", "could", "might")
	case "disagreement":
		return strings.Contains(answer, " but ") && containsAnyTerm(answer, "point", "understand", "concern", "agree", "worry")
	case "having-said-that":
		return strings.Contains(answer, "having said that") || strings.Contains(answer, "that said") || (strings.Contains(answer, " but ") && containsAnyTerm(answer, "point", "concern", "agree"))
	case "formal-opinion":
		return strings.Contains(answer, "not entirely convinced") || strings.Contains(answer, "not convinced") || strings.Contains(answer, "not sure")
	case "professional-suggestion":
		return containsAnyTerm(answer, "suggest", "recommend", "could we", "we should")
	default:
		return true
	}
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
	diag := GenerationDiagnostics{GenerationID: fmt.Sprintf("generation-%s-%d", ex.ID, time.Now().UnixNano()), ContractVersion: generatorContractVersion, Spec: generationSpecFromExercise(ex), InitialCalls: 1, ProviderCalls: 1}
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
		metrics.GeneratorStructuralFailures++
	case GeneratorFailureProviderEmpty:
		metrics.GeneratorProviderEmptyCount++
	case GeneratorFailureReasoningOnly:
		metrics.GeneratorReasoningOnlyCount++
	case GeneratorFailureAdapterEmpty, GeneratorFailureValidContentWrongField:
		metrics.GeneratorAdapterExtractionFailures++
	case GeneratorFailureMalformedJSON:
		metrics.GeneratorMalformedJSONCount++
		metrics.GeneratorStructuralFailures++
	case GeneratorFailureTruncatedJSON:
		metrics.GeneratorTruncatedJSONCount++
		metrics.GeneratorStructuralFailures++
	case GeneratorFailureTimeout:
		metrics.GeneratorTimeoutCount++
	case GeneratorFailureSchemaInvalid:
		metrics.GeneratorSchemaInvalidCount++
		metrics.GeneratorStructuralFailures++
	case GeneratorFailureConstraintViolation, GeneratorFailureInvalidDifficulty, GeneratorFailureSceneMismatch, GeneratorFailurePatternMismatch, GeneratorFailureIntentMismatch:
		metrics.GeneratorConstraintViolationCount++
		metrics.GeneratorSemanticFailures++
		if kind == GeneratorFailureInvalidDifficulty {
			metrics.GeneratorDifficultyRejects++
		}
		if kind == GeneratorFailureSceneMismatch {
			metrics.GeneratorSceneMismatchCount++
		}
	}
}

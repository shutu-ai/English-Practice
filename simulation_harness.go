package main

// V2.3.1 simulation and acceptance harness.
//
// The algorithm mode is deliberately self contained: it owns a synthetic
// learner state and an in-memory SQLite run store, but reuses the production
// difficulty controller (targetDifficulty/sessionBand/difficultyConfig). It
// never opens ENGLISH_PRACTICE_DATA and therefore cannot mutate real history.
// AI mode uses the interfaces in this file so learner, evaluator, and reviewer
// contexts stay separate. A live provider is an explicit integration concern;
// the CLI is dry-run by default for AI modes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	simulationVersion           = "v2.3.4"
	SimulationModeAlgorithm     = "algorithm"
	SimulationModeDeterministic = "deterministic"
	SimulationModeLLMLearner    = "llm-learner"
	SimulationModeFullAI        = "full-ai"
	SimulationTimeSameDay       = "same-day"
	SimulationTimeDaily         = "daily"
	SimulationTimeIrregular     = "irregular"
	SimulationTimeWeekly        = "weekly"
)

type LearnerPersona struct {
	ID                string             `json:"id"`
	Version           string             `json:"version"`
	Description       string             `json:"description"`
	BaseAbility       float64            `json:"base_ability"`
	PatternStrengths  map[string]float64 `json:"pattern_strengths,omitempty"`
	PatternWeaknesses map[string]float64 `json:"pattern_weaknesses,omitempty"`
	SceneStrengths    map[string]float64 `json:"scene_strengths,omitempty"`
	SceneWeaknesses   map[string]float64 `json:"scene_weaknesses,omitempty"`
	ForgettingRate    float64            `json:"forgetting_rate"`
	NoiseRate         float64            `json:"noise_rate"`
	LearningRate      float64            `json:"learning_rate"`
	RetentionBaseline float64            `json:"retention_baseline"`
}

func cloneMap(in map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

// StandardPersonas is data-like and versioned so reports remain comparable.
func StandardPersonas() map[string]LearnerPersona {
	base := func(id, description string, ability float64) LearnerPersona {
		return LearnerPersona{ID: id, Version: "1", Description: description, BaseAbility: ability,
			PatternStrengths: map[string]float64{}, PatternWeaknesses: map[string]float64{},
			SceneStrengths: map[string]float64{}, SceneWeaknesses: map[string]float64{},
			ForgettingRate: .025, NoiseRate: 0, LearningRate: .045, RetentionBaseline: .78}
	}
	all := map[string]LearnerPersona{}
	p := base("beginner", "Low overall ability; foundations are stronger than past tense, conditionals, and work.", 2.35)
	p.PatternStrengths["be-basic"], p.PatternStrengths["like"], p.PatternStrengths["can"] = .45, .35, .3
	for _, x := range []string{"past-perfect", "conditional", "mixed-conditional", "professional-suggestion", "formal-opinion"} {
		p.PatternWeaknesses[x] = -1.2
	}
	p.SceneWeaknesses["meeting"], p.SceneWeaknesses["work"] = -1.0, -.8
	p.LearningRate, p.RetentionBaseline = .055, .62
	all[p.ID] = p

	p = base("stable-intermediate", "Medium, steady learner; daily life and requests are stronger than conditionals and disagreement.", 4.65)
	for _, x := range []string{"like", "simple-past", "polite-request", "would-like"} {
		p.PatternStrengths[x] = .5
	}
	for _, x := range []string{"conditional", "mixed-conditional", "disagreement", "formal-opinion"} {
		p.PatternWeaknesses[x] = -.85
	}
	p.SceneWeaknesses["meeting"] = -.45
	all[p.ID] = p

	p = base("advanced-uneven", "High overall ability with phone calls, perfect modals, and advanced conditionals as weak spots.", 6.65)
	for _, x := range []string{"professional-suggestion", "disagreement", "formal-opinion", "reported-speech"} {
		p.PatternStrengths[x] = .65
	}
	for _, x := range []string{"phone-clarification", "perfect-modal", "mixed-conditional", "modal-possibility"} {
		p.PatternWeaknesses[x] = -1.0
	}
	p.SceneWeaknesses["phone"], p.SceneWeaknesses["meeting"] = -.9, -.15
	p.SceneStrengths["work"], p.SceneStrengths["meeting"], p.SceneStrengths["daily"] = .45, .35, .3
	p.LearningRate = .035
	all[p.ID] = p

	p = base("forgetful", "Learns quickly but loses retention after long gaps.", 4.2)
	p.ForgettingRate, p.RetentionBaseline, p.LearningRate = .13, .58, .07
	all[p.ID] = p

	p = base("fast-learner", "New skills improve quickly while retention remains fairly stable.", 4.6)
	p.LearningRate, p.RetentionBaseline, p.ForgettingRate = .115, .86, .018
	all[p.ID] = p

	p = base("noisy-learner", "Strong learner with isolated random failures rather than persistent weaknesses.", 5.8)
	p.NoiseRate, p.LearningRate, p.RetentionBaseline = .14, .04, .82
	all[p.ID] = p

	p = base("scene-uneven", "Strong in daily life and restaurant; weak in phone calls and meetings.", 5.2)
	p.SceneStrengths["daily"], p.SceneStrengths["restaurant"] = .7, .65
	p.SceneWeaknesses["phone"], p.SceneWeaknesses["meeting"] = -1.35, -1.05
	p.LearningRate = .05
	all[p.ID] = p
	return all
}

func ResolveLearnerPersona(id string) (LearnerPersona, error) {
	key := strings.ToLower(strings.TrimSpace(id))
	key = strings.ReplaceAll(key, "_", "-")
	key = strings.ReplaceAll(key, " ", "-")
	aliases := map[string]string{"intermediate": "stable-intermediate", "stable": "stable-intermediate", "advanced": "advanced-uneven", "advanced-uneven": "advanced-uneven", "fast": "fast-learner", "noisy": "noisy-learner", "forgetful-learner": "forgetful", "scene": "scene-uneven"}
	if v, ok := aliases[key]; ok {
		key = v
	}
	p, ok := StandardPersonas()[key]
	if !ok {
		return LearnerPersona{}, fmt.Errorf("unknown simulation persona %q", id)
	}
	p.PatternStrengths, p.PatternWeaknesses = cloneMap(p.PatternStrengths), cloneMap(p.PatternWeaknesses)
	p.SceneStrengths, p.SceneWeaknesses = cloneMap(p.SceneStrengths), cloneMap(p.SceneWeaknesses)
	return p, nil
}

type SimulationConfig struct {
	Mode, Persona, Scene, Subscene, TimeProfile, Generator           string
	Attempts, SessionSize, MaxAttempts, MaxTokens                    int
	Seed                                                             int64
	Timeout                                                          time.Duration
	EstimatedCostLimit                                               float64
	LearnerProvider, LearnerModel, EvaluatorProvider, EvaluatorModel string
	GeneratorProvider, GeneratorModel                                string
	Output, SimulationDBPath                                         string
	DryRun, AllowLarge, AttemptsExplicit                             bool
}

func DefaultSimulationConfig() SimulationConfig {
	return SimulationConfig{Mode: SimulationModeAlgorithm, Persona: "stable-intermediate", Attempts: 200, SessionSize: 20, MaxAttempts: 100, Seed: 42, TimeProfile: SimulationTimeDaily, Generator: "fixture-generator", MaxTokens: 256, Timeout: 45 * time.Second, EstimatedCostLimit: 1}
}

func validateSimulationConfig(c SimulationConfig) error {
	if c.Mode != SimulationModeAlgorithm && c.Mode != SimulationModeDeterministic && c.Mode != SimulationModeLLMLearner && c.Mode != SimulationModeFullAI {
		return fmt.Errorf("unsupported simulation mode %q", c.Mode)
	}
	if c.Attempts <= 0 || c.SessionSize <= 0 {
		return errors.New("attempts and session-size must be positive")
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 100
	}
	if c.Mode != SimulationModeAlgorithm && c.Attempts > c.MaxAttempts && !c.AllowLarge {
		return fmt.Errorf("AI simulation is limited to %d attempts; explicitly pass --allow-large to exceed it", c.MaxAttempts)
	}
	if c.MaxTokens <= 0 || c.Timeout <= 0 || c.EstimatedCostLimit < 0 {
		return errors.New("max-tokens, timeout, and cost limit must be valid")
	}
	if c.Mode != SimulationModeAlgorithm && c.EstimatedCostLimit > 0 && estimateAIUsage(c).EstimatedCost > c.EstimatedCostLimit && !c.AllowLarge {
		return fmt.Errorf("estimated AI cost %.3f exceeds configured limit %.3f; raise the limit explicitly", estimateAIUsage(c).EstimatedCost, c.EstimatedCostLimit)
	}
	if c.TimeProfile != SimulationTimeSameDay && c.TimeProfile != SimulationTimeDaily && c.TimeProfile != SimulationTimeIrregular && c.TimeProfile != SimulationTimeWeekly {
		return fmt.Errorf("unsupported time profile %q", c.TimeProfile)
	}
	if c.SimulationDBPath != "" {
		prod := os.Getenv("ENGLISH_PRACTICE_DATA")
		if prod == "" {
			prod = "data"
		}
		prodPath := filepath.Join(prod, "english-practice.db")
		if filepath.Clean(c.SimulationDBPath) == filepath.Clean(prodPath) || strings.EqualFold(filepath.Base(c.SimulationDBPath), "english-practice.db") {
			return errors.New("refusing to use the production database as a simulation database")
		}
	}
	return nil
}

type SimulationExercise struct {
	ID, ChinesePrompt, PatternID, Pattern, SceneID, SubsceneID, Intent, DifficultyBand string
	Difficulty                                                                         float64
	ReferenceAnswers                                                                   []string `json:"reference_answers,omitempty"`
}

// GenerationSpec is application-owned selection context. It is passed to the
// provider as context, then bound back onto the exercise after linguistic
// content is accepted; the provider never becomes authoritative for these
// fields.
type GenerationSpec struct {
	ExerciseID       string  `json:"exercise_id"`
	SceneID          string  `json:"scene_id"`
	SubsceneID       string  `json:"subscene_id,omitempty"`
	Intent           string  `json:"intent"`
	PatternID        string  `json:"pattern_id"`
	Pattern          string  `json:"pattern"`
	TargetDifficulty float64 `json:"target_difficulty"`
	DifficultyBand   string  `json:"difficulty_band"`
}

func generationSpecFromExercise(ex SimulationExercise) GenerationSpec {
	return GenerationSpec{ExerciseID: ex.ID, SceneID: ex.SceneID, SubsceneID: ex.SubsceneID, Intent: ex.Intent, PatternID: ex.PatternID, Pattern: ex.Pattern, TargetDifficulty: ex.Difficulty, DifficultyBand: ex.DifficultyBand}
}

type SimulatedLearnerState struct {
	PersonaID      string             `json:"persona_id"`
	Ability        float64            `json:"ability"`
	PatternMastery map[string]float64 `json:"pattern_mastery"`
	SceneMastery   map[string]float64 `json:"scene_mastery"`
	HistorySummary string             `json:"history_summary"`
}

type SimulatedAnswer struct {
	Text string `json:"text"`
}
type SimulationEvaluation struct {
	Correct            bool             `json:"correct"`
	Verdict            string           `json:"verdict"`
	Meaning            float64          `json:"meaning"`
	Grammar            float64          `json:"grammar"`
	Naturalness        float64          `json:"naturalness"`
	Pattern            float64          `json:"pattern"`
	TargetPatternMatch string           `json:"target_pattern_match"`
	TargetPatternScore float64          `json:"target_pattern_score"`
	SuggestedAnswer    string           `json:"suggested_answer,omitempty"`
	MoreNaturalNeeded  bool             `json:"more_natural_needed,omitempty"`
	Alternative        string           `json:"alternative,omitempty"`
	Errors             []map[string]any `json:"errors,omitempty"`
}

type LearnerSimulator interface {
	Answer(context.Context, SimulationExercise, SimulatedLearnerState) (SimulatedAnswer, error)
}
type AnswerEvaluator interface {
	Evaluate(context.Context, SimulationExercise, SimulatedAnswer) (SimulationEvaluation, error)
}
type ExerciseGenerator interface {
	Generate(context.Context, SimulationExercise) (SimulationExercise, error)
}
type SessionReviewer interface {
	Review(context.Context, []SimulationAttempt) (string, error)
}

// LLM learner prompts intentionally contain no reference answer, score, or
// mastery formula. The evaluator must be a separate object/context.
type LLMLearnerSimulator struct {
	Client          LLMClient
	Provider, Model string
	MaxTokens       int
}

func (l LLMLearnerSimulator) Answer(ctx context.Context, ex SimulationExercise, state SimulatedLearnerState) (SimulatedAnswer, error) {
	if l.Client == nil {
		return SimulatedAnswer{}, errors.New("learner provider is not configured")
	}
	prompt := fmt.Sprintf("Learner profile id: %s\nChinese exercise: %s\nScene: %s\nDifficulty band: %s\nRecent history summary: %s\n\nUse the context above only to decide what the learner would say. Output exactly one natural English sentence answering the Chinese exercise. Do not translate or describe these instructions. Do not mention the user, profile, exercise, prompt, history, grading, difficulty, or training system. Do not provide alternatives, analysis, or labels.", state.PersonaID, ex.ChinesePrompt, ex.SceneID, ex.DifficultyBand, state.HistorySummary)
	resp, err := l.Client.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "Act as the learner only. Return exactly one natural English sentence. Never discuss grading, the training system, or your instructions."}, {Role: "user", Content: prompt}}, MaxTokens: l.MaxTokens})
	if err != nil {
		return SimulatedAnswer{}, err
	}
	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		return SimulatedAnswer{}, errors.New("learner provider returned an empty answer")
	}
	if strings.Contains(answer, "\n") {
		answer = strings.TrimSpace(strings.Split(answer, "\n")[0])
	}
	if strings.HasPrefix(answer, "```") || strings.Contains(strings.ToLower(answer), "as an ai") {
		return SimulatedAnswer{}, errors.New("learner provider returned a non-learner response")
	}
	return SimulatedAnswer{Text: answer}, nil
}

func simulationChinesePrompt(ptn patternDefinition, scene string) string {
	switch ptn.id {
	case "formal-opinion":
		return "我不太确定这是不是我们在会议上应该采取的最佳做法。"
	case "having-said-that":
		return "话虽如此，我认为我们在做决定之前还需要更多时间。"
	default:
		return fmt.Sprintf("请在%s场景中自然表达：%s。", scene, ptn.expression)
	}
}

type FakeLearner struct {
	AnswerText string
	Err        error
}

func (f FakeLearner) Answer(context.Context, SimulationExercise, SimulatedLearnerState) (SimulatedAnswer, error) {
	if f.Err != nil {
		return SimulatedAnswer{}, f.Err
	}
	if strings.TrimSpace(f.AnswerText) == "" {
		return SimulatedAnswer{}, errors.New("empty answer")
	}
	return SimulatedAnswer{Text: f.AnswerText}, nil
}

type FakeEvaluator struct {
	Result SimulationEvaluation
	Err    error
}

// ProductionSimulationEvaluator reuses the production evaluator prompt and
// response normalization while keeping persistence out of the simulation.
// The wrapped Server has no database handle and only owns the evaluator
// provider registry.
type ProductionSimulationEvaluator struct {
	Server   *Server
	Provider string
}

func (e ProductionSimulationEvaluator) Evaluate(ctx context.Context, ex SimulationExercise, answer SimulatedAnswer) (SimulationEvaluation, error) {
	if e.Server == nil || e.Server.llm == nil {
		return SimulationEvaluation{}, errors.New("evaluator provider is not configured")
	}
	eval, _, _, _, err := e.Server.evaluateWithProvider(ctx, ex.ChinesePrompt, ex.PatternID, answer.Text, e.Provider)
	if err != nil {
		return SimulationEvaluation{}, err
	}
	return SimulationEvaluation{Correct: eval.Verdict == "correct" || eval.Verdict == "mostly_correct", Verdict: eval.Verdict, Meaning: eval.MeaningScore, Grammar: eval.GrammarScore, Naturalness: eval.NaturalnessScore, Pattern: eval.PatternScore, TargetPatternMatch: eval.TargetPatternMatch, TargetPatternScore: eval.TargetPatternScore, SuggestedAnswer: eval.SuggestedAnswer, MoreNaturalNeeded: eval.MoreNaturalNeeded, Alternative: eval.Alternative, Errors: eval.Errors}, nil
}

// LLMExerciseGenerator is intentionally independent from the production DB.
// It generates a candidate prompt but returns only the learner-facing fields.
type LLMExerciseGenerator struct {
	Client    LLMClient
	MaxTokens int
}

func (g LLMExerciseGenerator) Generate(ctx context.Context, ex SimulationExercise) (SimulationExercise, error) {
	generated, _, err := g.GenerateDetailed(ctx, ex)
	return generated, err
}

func simulationProviderDBPath() string {
	dataDir := os.Getenv("ENGLISH_PRACTICE_DATA")
	if dataDir == "" {
		dataDir = "data"
	}
	return filepath.Join(dataDir, "english-practice.db")
}

func readConfiguredProvider(row interface{ Scan(...any) error }) (ProviderConfig, error) {
	var c ProviderConfig
	var enabled int
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Model, &c.Timeout, &c.Temperature, &c.MaxTokens, &enabled); err != nil {
		return c, err
	}
	c.Enabled = enabled == 1
	return c, nil
}

func chooseSimulationProvider(configs []ProviderConfig, requested, model string) (ProviderConfig, error) {
	for _, c := range configs {
		requestedMatch := requested == "" || requested == c.ID || strings.EqualFold(requested, c.Name)
		modelMatch := model == "" || model == c.Model
		if c.Enabled && requestedMatch && modelMatch {
			if model != "" {
				c.Model = model
			}
			return c, nil
		}
	}
	return ProviderConfig{}, fmt.Errorf("no enabled provider matches provider=%q model=%q", requested, model)
}

func configuredSimulationRunner(cfg SimulationConfig) (*SimulationRunner, SimulationConfig, error) {
	path := simulationProviderDBPath()
	if _, err := os.Stat(path); err != nil {
		return nil, cfg, fmt.Errorf("provider configuration DB is unavailable: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, cfg, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.Query(`SELECT id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled FROM llm_providers WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, cfg, err
	}
	defer rows.Close()
	var configs []ProviderConfig
	for rows.Next() {
		c, scanErr := readConfiguredProvider(rows)
		if scanErr != nil {
			return nil, cfg, scanErr
		}
		configs = append(configs, c)
	}
	if len(configs) == 0 {
		return nil, cfg, errors.New("no enabled simulation provider is configured")
	}
	learner, err := chooseSimulationProvider(configs, cfg.LearnerProvider, cfg.LearnerModel)
	if err != nil {
		return nil, cfg, err
	}
	evaluator, err := chooseSimulationProvider(configs, cfg.EvaluatorProvider, cfg.EvaluatorModel)
	if err != nil {
		return nil, cfg, err
	}
	generator, err := chooseSimulationProvider(configs, cfg.GeneratorProvider, cfg.GeneratorModel)
	if err != nil {
		return nil, cfg, err
	}
	cfg.LearnerProvider, cfg.LearnerModel = learner.ID, learner.Model
	cfg.EvaluatorProvider, cfg.EvaluatorModel = evaluator.ID, evaluator.Model
	cfg.GeneratorProvider, cfg.GeneratorModel = generator.ID, generator.Model
	registry := &LLMRegistry{configs: map[string]ProviderConfig{learner.ID: learner, evaluator.ID: evaluator, generator.ID: generator}}
	server := &Server{llm: registry}
	runner := &SimulationRunner{Learner: LLMLearnerSimulator{Client: registry.Client(learner), Provider: learner.ID, Model: learner.Model, MaxTokens: cfg.MaxTokens}, Evaluator: ProductionSimulationEvaluator{Server: server, Provider: evaluator.ID}}
	if cfg.Generator == "real-generator" {
		runner.Generator = LLMExerciseGenerator{Client: registry.Client(generator), MaxTokens: cfg.MaxTokens}
	}
	return runner, cfg, nil
}

func (f FakeEvaluator) Evaluate(context.Context, SimulationExercise, SimulatedAnswer) (SimulationEvaluation, error) {
	if f.Err != nil {
		return SimulationEvaluation{}, f.Err
	}
	return f.Result, nil
}

type SimulationAttempt struct {
	Index              int                   `json:"index"`
	Session            int                   `json:"session"`
	VirtualTime        time.Time             `json:"virtual_time"`
	Exercise           SimulationExercise    `json:"exercise"`
	Correct            bool                  `json:"correct"`
	Review             bool                  `json:"review"`
	Probe              bool                  `json:"probe"`
	NewSkill           bool                  `json:"new_skill"`
	Reason             string                `json:"reason"`
	ErrorKind          string                `json:"error_kind,omitempty"`
	TargetDifficulty   float64               `json:"target_difficulty"`
	RealizedDifficulty float64               `json:"realized_difficulty"`
	EffectiveAbility   float64               `json:"effective_ability"`
	SuccessProbability float64               `json:"success_probability"`
	Answer             string                `json:"answer,omitempty"`
	Verdict            string                `json:"verdict"`
	Evaluation         SimulationEvaluation  `json:"evaluation,omitempty"`
	Generation         GenerationDiagnostics `json:"generation,omitempty"`
}

type SkillUnlockEvent struct {
	Skill             string             `json:"skill"`
	AttemptIndex      int                `json:"attempt_index"`
	VirtualTime       time.Time          `json:"virtual_time"`
	PrerequisiteState map[string]float64 `json:"prerequisite_state"`
	Reason            string             `json:"unlock_reason"`
}

type SimulationMetrics struct {
	OverallAccuracy                       float64            `json:"overall_accuracy"`
	AccuracyByDifficulty                  map[string]float64 `json:"accuracy_by_difficulty,omitempty"`
	AccuracyByPattern                     map[string]float64 `json:"accuracy_by_pattern,omitempty"`
	AccuracyByScene                       map[string]float64 `json:"accuracy_by_scene,omitempty"`
	DifficultyJitter                      float64            `json:"difficulty_jitter"`
	MaxAdjacentDifficultyJump             float64            `json:"max_adjacent_difficulty_jump"`
	ProductiveZoneRatio                   float64            `json:"productive_zone_ratio"`
	ExactRepeatRate                       float64            `json:"exact_repeat_rate"`
	NormalizedExactDuplicateRate          float64            `json:"normalized_exact_duplicate_rate"`
	SamePatternSpacing                    float64            `json:"same_pattern_spacing"`
	WeakSkillExposure                     float64            `json:"weak_skill_exposure"`
	ReviewHitRate                         float64            `json:"review_hit_rate"`
	ProbeRatio                            float64            `json:"probe_ratio"`
	ProbeSuccessRate                      float64            `json:"probe_success_rate"`
	UnknownToObserved                     float64            `json:"unknown_to_observed"`
	FoundationExposureRatio               float64            `json:"foundation_exposure_ratio"`
	MaxConsecutiveSamePattern             int                `json:"max_consecutive_same_pattern"`
	SkillUnlocks                          int                `json:"skill_unlocks"`
	SceneCoverage                         map[string]float64 `json:"scene_coverage"`
	SceneMastery                          map[string]float64 `json:"scene_mastery"`
	TransferEvents                        int                `json:"transfer_events"`
	AcquisitionTrajectory                 []float64          `json:"acquisition_trajectory"`
	RetentionTrajectory                   []float64          `json:"retention_trajectory"`
	TransferTrajectory                    []float64          `json:"transfer_trajectory"`
	SessionDifficultyTrajectory           []float64          `json:"session_difficulty_trajectory"`
	LearnerAbilityTrajectory              []float64          `json:"learner_ability_trajectory"`
	MemoryIntervalBefore                  []float64          `json:"memory_interval_before,omitempty"`
	MemoryIntervalAfter                   []float64          `json:"memory_interval_after,omitempty"`
	SystemFailures                        int                `json:"system_failures"`
	ReviewDue                             int                `json:"review_due"`
	ReviewServed                          int                `json:"review_served"`
	ProbeCount                            int                `json:"probe_count"`
	UniquePatterns                        int                `json:"unique_patterns"`
	UniqueIntents                         int                `json:"unique_intents"`
	SceneMismatchCount                    int                `json:"scene_mismatch_count"`
	ScopeRelaxationCount                  int                `json:"scope_relaxation_count"`
	PatternMatchCounts                    map[string]int     `json:"pattern_match_counts,omitempty"`
	GeneratorExactDuplicateRate           float64            `json:"generator_exact_duplicate_rate"`
	GeneratorNormalizedDuplicateRate      float64            `json:"generator_normalized_duplicate_rate"`
	GeneratorInitialCalls                 int                `json:"generator_initial_calls"`
	GeneratorInitialSuccesses             int                `json:"generator_initial_successes"`
	GeneratorRepairAttempts               int                `json:"generator_repair_attempts"`
	GeneratorRepairSuccesses              int                `json:"generator_repair_successes"`
	GeneratorFreshRetryAttempts           int                `json:"generator_fresh_retry_attempts"`
	GeneratorFreshRetrySuccesses          int                `json:"generator_fresh_retry_successes"`
	GeneratorFallbackCount                int                `json:"generator_fallback_count"`
	GeneratorFinalDeliveryCount           int                `json:"generator_final_delivery_count"`
	GeneratorInitialSuccessRate           float64            `json:"generator_initial_success_rate"`
	GeneratorRepairRate                   float64            `json:"generator_repair_rate"`
	GeneratorRetryRate                    float64            `json:"generator_retry_rate"`
	GeneratorFallbackRate                 float64            `json:"generator_fallback_rate"`
	GeneratorFinalDeliveryRate            float64            `json:"generator_final_delivery_rate"`
	GeneratorFailureKinds                 map[string]int     `json:"generator_failure_kinds,omitempty"`
	GeneratorEmptyResponseCount           int                `json:"generator_empty_response_count"`
	GeneratorMalformedJSONCount           int                `json:"generator_malformed_json_count"`
	GeneratorTruncatedJSONCount           int                `json:"generator_truncated_json_count"`
	GeneratorTimeoutCount                 int                `json:"generator_timeout_count"`
	GeneratorSchemaInvalidCount           int                `json:"generator_schema_invalid_count"`
	GeneratorConstraintViolationCount     int                `json:"generator_constraint_violation_count"`
	GeneratorSceneMismatchCount           int                `json:"generator_scene_mismatch_count"`
	GeneratorDifficultyRejects            int                `json:"generator_difficulty_rejects"`
	GeneratorRequests                     int                `json:"generator_requests"`
	GeneratorInitialFailures              int                `json:"generator_initial_failures"`
	GeneratorStructuralFailures           int                `json:"generator_structural_failures"`
	GeneratorSemanticFailures             int                `json:"generator_semantic_failures"`
	GeneratorAdapterExtractionFailures    int                `json:"generator_adapter_extraction_failures"`
	GeneratorProviderEmptyCount           int                `json:"generator_provider_empty_count"`
	GeneratorReasoningOnlyCount           int                `json:"generator_reasoning_only_count"`
	GeneratorDeterministicRepairSuccesses int                `json:"generator_deterministic_repair_successes"`
	GeneratorLLMRepairSuccesses           int                `json:"generator_llm_repair_successes"`
	AdaptiveStateUpdates                  int                `json:"adaptive_state_updates"`
	AdaptiveNextExerciseReplans           int                `json:"adaptive_next_exercise_replans"`
	AdaptiveReplanEligible                int                `json:"adaptive_replan_eligible"`
	AdaptiveReplanMisses                  int                `json:"adaptive_replan_misses"`
	TargetDifficultyTrajectory            []float64          `json:"target_difficulty_trajectory"`
	RealizedDifficultyTrajectory          []float64          `json:"realized_difficulty_trajectory"`
}

type SimulationResult struct {
	SimulationVersion        string              `json:"simulation_version"`
	Persona                  string              `json:"persona"`
	PersonaVersion           string              `json:"persona_version"`
	Mode                     string              `json:"mode"`
	Seed                     int64               `json:"seed"`
	Attempts                 int                 `json:"attempts"`
	Sessions                 int                 `json:"sessions"`
	VirtualDays              float64             `json:"virtual_days"`
	Provider                 string              `json:"provider,omitempty"`
	Model                    string              `json:"model,omitempty"`
	LearnerProvider          string              `json:"learner_provider,omitempty"`
	LearnerModel             string              `json:"learner_model,omitempty"`
	EvaluatorProvider        string              `json:"evaluator_provider,omitempty"`
	EvaluatorModel           string              `json:"evaluator_model,omitempty"`
	GeneratorProvider        string              `json:"generator_provider,omitempty"`
	GeneratorModel           string              `json:"generator_model,omitempty"`
	GeneratorMode            string              `json:"generator_mode"`
	GeneratorContractVersion string              `json:"generator_contract_version,omitempty"`
	MaxAllowedAttempts       int                 `json:"max_allowed_attempts"`
	PolicyVersion            string              `json:"policy_version"`
	DifficultyPolicyVersion  string              `json:"difficulty_policy_version"`
	StartedAt                time.Time           `json:"started_at"`
	CompletedAt              time.Time           `json:"completed_at"`
	DryRun                   bool                `json:"dry_run"`
	Metrics                  SimulationMetrics   `json:"metrics"`
	HealthFlags              []string            `json:"health_flags"`
	UnlockEvents             []SkillUnlockEvent  `json:"unlock_events,omitempty"`
	AttemptsTrace            []SimulationAttempt `json:"attempts_trace,omitempty"`
	Manifest                 map[string]any      `json:"manifest"`
	AI                       AIUsage             `json:"ai_usage"`
}

type AIUsage struct {
	LearnerCalls   int     `json:"learner_calls"`
	EvaluatorCalls int     `json:"evaluator_calls"`
	GeneratorCalls int     `json:"generator_calls"`
	TotalCalls     int     `json:"total_calls"`
	InputTokens    int     `json:"input_tokens"`
	OutputTokens   int     `json:"output_tokens"`
	EstimatedCost  float64 `json:"estimated_cost"`
	CostStatus     string  `json:"cost_status"`
	Status         string  `json:"status"`
}

type simPatternState struct {
	Mastery, Acquisition, Retention, Transfer  float64
	Attempts, Successes, Failures, Consecutive int
	Last                                       time.Time
	Next                                       time.Time
	Scenes                                     map[string]bool
	LastInterval                               float64
}
type simSceneState struct {
	Attempts, Successes int
	Patterns, Intents   map[string]bool
	Mastery             float64
}

func simulationClock(profile string, session, attempt int, sessionSize int, current time.Time) time.Time {
	if attempt == 0 {
		return current
	}
	if sessionSize <= 0 {
		sessionSize = 20
	}
	if attempt%sessionSize == 0 {
		days := 0
		switch profile {
		case SimulationTimeDaily:
			days = 1
		case SimulationTimeWeekly:
			days = 7
		case SimulationTimeIrregular:
			days = []int{0, 1, 2, 0, 4, 7}[session%6]
		}
		return current.Add(time.Duration(days) * 24 * time.Hour)
	}
	return current.Add(15 * time.Minute)
}

func simSceneForPattern(pattern string, requested string, session int) string {
	if requested != "" {
		return requested
	}
	matches := []string{}
	for _, scene := range []string{"daily", "restaurant", "travel", "hotel", "work", "meeting", "phone", "friends", "shopping"} {
		for _, p := range scenePatternFamilies[scene] {
			if p == pattern {
				matches = append(matches, scene)
				break
			}
		}
	}
	if len(matches) > 0 {
		return matches[session%len(matches)]
	}
	return "daily"
}

func simulationCandidates(scene, subscene string) []patternDefinition {
	valid := map[string]bool{}
	if scene != "" {
		for _, p := range scenePatternFamilies[scene] {
			valid[p] = true
		}
	}
	if subscene != "" && scene != "" {
		for _, p := range subscenePatterns(subscene, scenePatternFamilies[scene]) {
			valid[p] = true
		}
	}
	all := patternCatalog()
	out := make([]patternDefinition, 0, len(all))
	for _, p := range all {
		if len(valid) == 0 || valid[p.id] {
			out = append(out, p)
		}
	}
	return out
}

func simMapValue(m map[string]float64, key string) float64 {
	if m == nil {
		return 0
	}
	return m[key]
}
func simDifficultyBand(x float64) string {
	if x < 3.2 {
		return "easy"
	}
	if x < 5.5 {
		return "appropriate"
	}
	return "challenging"
}
func simClamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }

func (r *SimulationRunner) Run(ctx context.Context, cfg SimulationConfig) (SimulationResult, error) {
	if cfg.Mode == SimulationModeDeterministic {
		cfg.Mode = SimulationModeAlgorithm
	}
	if err := validateSimulationConfig(cfg); err != nil {
		return SimulationResult{}, err
	}
	persona, err := ResolveLearnerPersona(cfg.Persona)
	if err != nil {
		return SimulationResult{}, err
	}
	started := time.Now().UTC()
	result := SimulationResult{SimulationVersion: simulationVersion, Persona: persona.ID, PersonaVersion: persona.Version, Mode: cfg.Mode, Seed: cfg.Seed, PolicyVersion: "adaptive-v2.3", DifficultyPolicyVersion: difficultyPolicyVersion, StartedAt: started, LearnerProvider: cfg.LearnerProvider, LearnerModel: cfg.LearnerModel, EvaluatorProvider: cfg.EvaluatorProvider, EvaluatorModel: cfg.EvaluatorModel, GeneratorProvider: cfg.GeneratorProvider, GeneratorModel: cfg.GeneratorModel, GeneratorMode: cfg.Generator, GeneratorContractVersion: generatorContractVersion, MaxAllowedAttempts: cfg.MaxAttempts, DryRun: cfg.DryRun, Manifest: map[string]any{"config": cfg, "persona": persona, "seed": cfg.Seed, "virtual_time_profile": cfg.TimeProfile, "database": "isolated-simulation-store", "generator_contract_version": generatorContractVersion}}
	if cfg.DryRun {
		result.Attempts = cfg.Attempts
		result.Sessions = (cfg.Attempts + cfg.SessionSize - 1) / cfg.SessionSize
		result.AI = estimateAIUsage(cfg)
		result.AI.Status = "dry-run; provider not called"
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}
	store, err := openSimulationStore(cfg)
	if err != nil {
		return SimulationResult{}, err
	}
	defer store.Close()
	if cfg.Mode != SimulationModeAlgorithm {
		if r.Learner == nil || r.Evaluator == nil {
			return SimulationResult{}, errors.New("live AI execution requires explicit learner and evaluator adapters; use --dry-run for CLI planning")
		}
		if cfg.Mode == SimulationModeFullAI && r.Generator == nil {
			return SimulationResult{}, errors.New("full-ai execution requires an explicit exercise generator adapter")
		}
		result = r.runAI(ctx, cfg, persona, result)
		result.CompletedAt = time.Now().UTC()
		return result, nil
	}
	result = r.runDeterministic(ctx, cfg, persona, result)
	result.CompletedAt = time.Now().UTC()
	return result, nil
}

func (r *SimulationRunner) runAI(ctx context.Context, cfg SimulationConfig, p LearnerPersona, result SimulationResult) SimulationResult {
	// Use the deterministic trace as a bounded preflight, then re-plan each
	// subsequent exercise from the real evaluator outcome. This keeps the run
	// reproducible while preserving the Full-AI chain: generator -> learner ->
	// evaluator -> adaptive state -> next exercise.
	result = r.runDeterministic(ctx, cfg, p, result)
	// The deterministic trace is only a bounded scheduling preflight. Do not
	// let its legacy provider label leak into a Full-AI report.
	result.Provider, result.Model = "", ""
	valid, correct := 0, 0
	byPattern, goodPattern := map[string]int{}, map[string]int{}
	byScene, goodScene := map[string]int{}, map[string]int{}
	state := SimulatedLearnerState{PersonaID: p.ID, Ability: p.BaseAbility, PatternMastery: map[string]float64{}, SceneMastery: map[string]float64{}}
	matchCounts := map[string]int{}
	promptCounts := map[string]int{}
	normalizedPromptCounts := map[string]int{}
	recentAnswers := []string{}
	aiStates := map[string]*simPatternState{}
	aiScenes := map[string]*simSceneState{}
	for _, ptn := range patternCatalog() {
		aiStates[ptn.id] = &simPatternState{Mastery: .2, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
	}
	for scene := range scenePatternFamilies {
		aiScenes[scene] = &simSceneState{Patterns: map[string]bool{}, Intents: map[string]bool{}}
	}
	aiPatterns := simulationCandidates(cfg.Scene, cfg.Subscene)
	aiRecentPatterns := []string{}
	aiConsecutive := 0
	aiCenter, aiAbility := p.BaseAbility, p.BaseAbility
	aiRNG := rand.New(rand.NewSource(cfg.Seed + 7919))
	aiCfg := difficultyConfig(defaultAdaptiveConfig())
	for i := range result.AttemptsTrace {
		a := &result.AttemptsTrace[i]
		ex := a.Exercise
		if r.Generator != nil {
			generated, generation, err := generateSimulationExercise(ctx, r.Generator, ex)
			result.AI.GeneratorCalls += generation.ProviderCalls
			result.Metrics.GeneratorRequests += generation.ProviderCalls
			result.Metrics.GeneratorInitialCalls += generation.InitialCalls
			if generation.InitialSuccess {
				result.Metrics.GeneratorInitialSuccesses++
			} else if generation.InitialCalls > 0 {
				result.Metrics.GeneratorInitialFailures++
			}
			result.Metrics.GeneratorRepairAttempts += generation.RepairAttempts
			result.Metrics.GeneratorFreshRetryAttempts += generation.FreshRetries
			if generation.FinalSource == "repaired" {
				result.Metrics.GeneratorRepairSuccesses++
			}
			if generation.FinalSource == "regenerated" {
				result.Metrics.GeneratorFreshRetrySuccesses++
			}
			if generation.FinalSource == "repaired" {
				result.Metrics.GeneratorLLMRepairSuccesses++
			}
			for _, response := range generation.Responses {
				if response.DeterministicRepair {
					result.Metrics.GeneratorDeterministicRepairSuccesses++
				}
			}
			if result.Metrics.GeneratorFailureKinds == nil {
				result.Metrics.GeneratorFailureKinds = map[string]int{}
			}
			for _, kind := range generation.FailureKinds {
				result.Metrics.GeneratorFailureKinds[string(kind)]++
				recordGeneratorFailureMetric(&result.Metrics, kind)
			}
			a.Generation = generation
			if err != nil {
				// A generation failure is not a learner failure. The bounded
				// scene-aware fallback keeps the attempt deliverable while the
				// diagnostics preserve the provider failure taxonomy.
				result.Metrics.GeneratorFallbackCount++
				a.Generation.FallbackUsed = true
				a.Generation.FinalSource = "fallback"
				ex = fallbackSimulationExercise(ex)
				a.Exercise = ex
			} else {
				ex = generated
				a.Exercise = generated
			}
			result.Metrics.GeneratorFinalDeliveryCount++
		}
		promptCounts[ex.ChinesePrompt]++
		normalizedPromptCounts[strings.ToLower(strings.Join(strings.Fields(ex.ChinesePrompt), " "))]++
		answer, err := r.Learner.Answer(ctx, ex, state)
		result.AI.LearnerCalls++
		if err != nil {
			a.ErrorKind, a.Verdict = classifySimulationFailure(err), "system_failure"
			continue
		}
		if strings.TrimSpace(answer.Text) == "" {
			a.ErrorKind, a.Verdict = "empty_answer", "system_failure"
			continue
		}
		a.Answer = answer.Text
		evaluation, err := r.Evaluator.Evaluate(ctx, ex, answer)
		result.AI.EvaluatorCalls++
		if err != nil {
			a.ErrorKind, a.Verdict = classifySimulationFailure(err), "system_failure"
			continue
		}
		valid++
		a.Evaluation = evaluation
		match := evaluation.TargetPatternMatch
		if match == "" {
			match = TargetPatternUnknown
		}
		matchCounts[match]++
		a.Correct = evaluation.Correct
		if evaluation.Correct {
			correct++
			a.Verdict = "correct"
		} else {
			a.Verdict = "incorrect"
		}
		byPattern[ex.PatternID]++
		byScene[ex.SceneID]++
		if evaluation.Correct {
			goodPattern[ex.PatternID]++
			goodScene[ex.SceneID]++
		}
		// Feed the real evaluator outcome into the next learner context. The
		// scheduler remains bounded and reproducible, while learner history and
		// mastery evidence now reflect the actual Full-AI result rather than the
		// deterministic preflight outcome.
		state.PatternMastery[ex.PatternID] = evaluation.Pattern
		state.SceneMastery[ex.SceneID] = evaluation.Naturalness
		state.Ability = simClamp(state.Ability+(evaluation.Meaning-.5)*.08, 1, 8)
		aiAbility = state.Ability
		recentAnswers = append(recentAnswers, answer.Text)
		if len(recentAnswers) > 3 {
			recentAnswers = recentAnswers[len(recentAnswers)-3:]
		}
		state.HistorySummary = strings.Join(recentAnswers, " | ")
		result.Metrics.AdaptiveStateUpdates++
		updateAIAdaptiveState(aiStates, aiScenes, ex, evaluation, a.VirtualTime, p)
		if ex.PatternID == aiRecentPattern(aiRecentPatterns) {
			aiConsecutive++
		} else {
			aiConsecutive = 1
		}
		aiRecentPatterns = append(aiRecentPatterns, ex.PatternID)
		if len(aiRecentPatterns) > aiCfg.RecentPatternWindow {
			aiRecentPatterns = aiRecentPatterns[len(aiRecentPatterns)-aiCfg.RecentPatternWindow:]
		}
		nextIndex := i + 1
		if nextIndex < len(result.AttemptsTrace) {
			result.Metrics.AdaptiveReplanEligible++
			if len(aiPatterns) == 0 {
				result.Metrics.AdaptiveReplanMisses++
				continue
			}
			if nextIndex%cfg.SessionSize == 0 {
				aiCenter = simClamp(aiCenter+(aiAbility-aiCenter)*.08, 1, 8)
			}
			next, review, probe, reason := chooseSimulationPattern(aiPatterns, aiStates, aiScenes, p, result.AttemptsTrace[nextIndex].VirtualTime, aiCenter, aiAbility, aiRecentPatterns, aiConsecutive, aiCfg, aiRNG)
			if next.id != "" {
				nextScene := simSceneForPattern(next.id, cfg.Scene, nextIndex/cfg.SessionSize)
				patternState := aiStates[next.id]
				patternAbility := p.BaseAbility + simMapValue(p.PatternStrengths, next.id) + simMapValue(p.PatternWeaknesses, next.id) + (patternState.Mastery-.5)*1.2
				target, _ := targetDifficulty(aiCenter, aiAbility, patternAbility, next.difficulty, reason, review, probe, patternState.Retention, aiCfg)
				realized := simClamp(target+(aiRNG.Float64()-.5)*.18, 1, 8)
				result.AttemptsTrace[nextIndex].Exercise = SimulationExercise{ID: result.AttemptsTrace[nextIndex].Exercise.ID, ChinesePrompt: simulationChinesePrompt(next, nextScene), PatternID: next.id, Pattern: next.expression, SceneID: nextScene, Intent: next.intent, DifficultyBand: simDifficultyBand(target), Difficulty: target}
				result.AttemptsTrace[nextIndex].Review = review
				result.AttemptsTrace[nextIndex].Probe = probe
				result.AttemptsTrace[nextIndex].Reason = reason
				result.AttemptsTrace[nextIndex].TargetDifficulty = target
				result.AttemptsTrace[nextIndex].RealizedDifficulty = realized
				result.Metrics.AdaptiveNextExerciseReplans++
			} else {
				result.Metrics.AdaptiveReplanMisses++
			}
		}
	}
	for _, attempt := range result.AttemptsTrace {
		if attempt.ErrorKind != "" {
			result.Metrics.SystemFailures++
		}
	}
	result.AI.TotalCalls = result.AI.LearnerCalls + result.AI.EvaluatorCalls + result.AI.GeneratorCalls
	result.AI.Status = "executed with separated learner/evaluator adapters"
	if result.AI.InputTokens == 0 && result.AI.OutputTokens == 0 {
		result.AI.CostStatus = "unavailable: provider did not return token usage"
		result.AI.EstimatedCost = 0
	}
	if valid > 0 {
		result.Metrics.OverallAccuracy = float64(correct) / float64(valid)
	} else {
		result.Metrics.OverallAccuracy = 0
	}
	for key, n := range byPattern {
		result.Metrics.AccuracyByPattern[key] = float64(goodPattern[key]) / float64(n)
	}
	for key, n := range byScene {
		result.Metrics.AccuracyByScene[key] = float64(goodScene[key]) / float64(n)
	}
	result.Metrics.UniquePatterns = len(byPattern)
	actualIntents := map[string]bool{}
	actualScenes := map[string]int{}
	for _, attempt := range result.AttemptsTrace {
		if attempt.ErrorKind == "generator_failure" {
			continue
		}
		actualIntents[attempt.Exercise.Intent] = true
		actualScenes[attempt.Exercise.SceneID]++
	}
	result.Metrics.UniqueIntents = len(actualIntents)
	result.Metrics.SceneCoverage = map[string]float64{}
	for scene, n := range actualScenes {
		result.Metrics.SceneCoverage[scene] = float64(n) / simMax(1, float64(result.Attempts))
	}
	result.Metrics.PatternMatchCounts = matchCounts
	result.Metrics.GeneratorExactDuplicateRate = duplicateRate(promptCounts)
	result.Metrics.GeneratorNormalizedDuplicateRate = duplicateRate(normalizedPromptCounts)
	initialCalls := float64(result.Metrics.GeneratorInitialCalls)
	if initialCalls > 0 {
		result.Metrics.GeneratorInitialSuccessRate = float64(result.Metrics.GeneratorInitialSuccesses) / initialCalls
		result.Metrics.GeneratorRepairRate = float64(result.Metrics.GeneratorRepairSuccesses) / initialCalls
		result.Metrics.GeneratorRetryRate = float64(result.Metrics.GeneratorRepairAttempts+result.Metrics.GeneratorFreshRetryAttempts) / initialCalls
	}
	if cfg.Attempts > 0 {
		result.Metrics.GeneratorFallbackRate = float64(result.Metrics.GeneratorFallbackCount) / float64(cfg.Attempts)
		result.Metrics.GeneratorFinalDeliveryRate = float64(result.Metrics.GeneratorFinalDeliveryCount) / float64(cfg.Attempts)
	}
	if r.Reviewer != nil {
		if _, err := r.Reviewer.Review(ctx, result.AttemptsTrace); err != nil {
			result.HealthFlags = append(result.HealthFlags, "SIM_REVIEWER_FAILURE")
		}
	}
	return result
}

func aiRecentPattern(recent []string) string {
	if len(recent) == 0 {
		return ""
	}
	return recent[len(recent)-1]
}

func updateAIAdaptiveState(states map[string]*simPatternState, scenes map[string]*simSceneState, ex SimulationExercise, e SimulationEvaluation, now time.Time, p LearnerPersona) {
	st := states[ex.PatternID]
	if st == nil {
		st = &simPatternState{Mastery: .2, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
		states[ex.PatternID] = st
	}
	st.Attempts++
	if e.Correct {
		st.Successes++
		st.Consecutive++
		st.Acquisition = .65*st.Acquisition + .35*e.Pattern
	} else {
		st.Failures++
		st.Consecutive = 0
		st.Acquisition = .65*st.Acquisition + .35*e.Pattern
	}
	st.Mastery = simClamp(.65*st.Mastery+.35*e.Pattern, 0, 1)
	st.Retention = simClamp(.7*st.Retention+.3*e.Naturalness, 0, 1)
	st.Transfer = simClamp(.7*st.Transfer+.3*e.Naturalness, 0, 1)
	st.Last = now
	st.Next = now.Add(time.Duration(nextSimulationInterval(st, e.Correct, 0, p) * float64(time.Hour)))
	st.Scenes[ex.SceneID] = true
	sc := scenes[ex.SceneID]
	if sc == nil {
		sc = &simSceneState{Patterns: map[string]bool{}, Intents: map[string]bool{}}
		scenes[ex.SceneID] = sc
	}
	sc.Attempts++
	if e.Correct {
		sc.Successes++
	}
	sc.Patterns[ex.PatternID] = true
	sc.Intents[ex.Intent] = true
	sc.Mastery = simClamp(.7*sc.Mastery+.3*e.Naturalness, 0, 1)
}

func classifySimulationFailure(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline"):
		return "timeout"
	case strings.Contains(s, "empty"):
		return "empty_answer"
	case strings.Contains(s, "invalid"):
		return "invalid_response"
	case strings.Contains(s, "500"), strings.Contains(s, "provider"):
		return "provider_error"
	default:
		return "system_failure"
	}
}

type SimulationRunner struct {
	Learner   LearnerSimulator
	Evaluator AnswerEvaluator
	Generator ExerciseGenerator
	Reviewer  SessionReviewer
}

// RunSimulation is the small programmatic entry point used by acceptance
// tests and integrations that do not need to construct a runner explicitly.
func RunSimulation(ctx context.Context, cfg SimulationConfig) (SimulationResult, error) {
	return (&SimulationRunner{}).Run(ctx, cfg)
}

func estimateAIUsage(c SimulationConfig) AIUsage {
	calls := c.Attempts * 2
	if c.Generator == "real-generator" {
		// Generator reliability allows one repair and one fresh generation
		// retry. Cost guards must reserve the full bounded retry budget.
		calls += c.Attempts * generatorMaxProviderCalls
	}
	return AIUsage{LearnerCalls: c.Attempts, EvaluatorCalls: c.Attempts, GeneratorCalls: calls - 2*c.Attempts, TotalCalls: calls, EstimatedCost: float64(calls) * .002, CostStatus: "estimated"}
}

func duplicateRate(counts map[string]int) float64 {
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return 0
	}
	return float64(total-len(counts)) / float64(total)
}

func openSimulationStore(c SimulationConfig) (*sql.DB, error) {
	name := "file:english-practice-simulation?mode=memory&cache=private"
	if c.SimulationDBPath != "" {
		name = c.SimulationDBPath
	}
	db, err := sql.Open("sqlite", name)
	if err != nil {
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS simulation_runs (id TEXT PRIMARY KEY, manifest_json TEXT NOT NULL); CREATE TABLE IF NOT EXISTS simulation_attempts (id INTEGER PRIMARY KEY, payload_json TEXT NOT NULL)`); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (r *SimulationRunner) runDeterministic(ctx context.Context, cfg SimulationConfig, p LearnerPersona, result SimulationResult) SimulationResult {
	_ = ctx
	cfgA := difficultyConfig(defaultAdaptiveConfig())
	rng := rand.New(rand.NewSource(cfg.Seed))
	patterns := simulationCandidates(cfg.Scene, cfg.Subscene)
	states := map[string]*simPatternState{}
	scenes := map[string]*simSceneState{}
	for _, ptn := range patternCatalog() {
		states[ptn.id] = &simPatternState{Mastery: .20, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
	}
	for scene := range scenePatternFamilies {
		scenes[scene] = &simSceneState{Patterns: map[string]bool{}, Intents: map[string]bool{}}
	}
	ability, center := p.BaseAbility, p.BaseAbility
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	now := start
	unlocked := map[string]bool{"foundations": true}
	recent := []string{}
	consecutive, maxConsecutive := 0, 0
	trace := make([]SimulationAttempt, 0, cfg.Attempts)
	unlocks := []SkillUnlockEvent{}
	metrics := SimulationMetrics{AccuracyByDifficulty: map[string]float64{}, AccuracyByPattern: map[string]float64{}, AccuracyByScene: map[string]float64{}, SceneCoverage: map[string]float64{}, SceneMastery: map[string]float64{}}
	countsD, successD, countsP, successP, countsS, successS := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	correctTotal, productive, probes, probeSuccess, weakExposure, reviews := 0, 0, 0, 0, 0, 0
	prevTarget, maxJump, jitter, prevPattern := center, 0.0, 0.0, ""
	seenPrompt := map[string]int{}
	samePatternGap, samePatternGapN := 0.0, 0
	for i := 0; i < cfg.Attempts; i++ {
		session := i / cfg.SessionSize
		if i > 0 {
			now = simulationClock(cfg.TimeProfile, session, i, cfg.SessionSize, now)
		}
		if i%cfg.SessionSize == 0 && i > 0 {
			center = simClamp(center+(ability-center)*.08, 1, 8)
		}
		ptn, review, probe, reason := chooseSimulationPattern(patterns, states, scenes, p, now, center, ability, recent, consecutive, cfgA, rng)
		if ptn.id == "" {
			ptn = patterns[i%len(patterns)]
		}
		scene := simSceneForPattern(ptn.id, cfg.Scene, session)
		if cfg.Scene != "" {
			scene = cfg.Scene
		}
		sc := scenes[scene]
		if sc == nil {
			sc = &simSceneState{Patterns: map[string]bool{}, Intents: map[string]bool{}}
			scenes[scene] = sc
		}
		st := states[ptn.id]
		if st == nil {
			st = &simPatternState{Mastery: .2, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
			states[ptn.id] = st
		}
		patternAbility := p.BaseAbility + simMapValue(p.PatternStrengths, ptn.id) + simMapValue(p.PatternWeaknesses, ptn.id) + (st.Mastery-.5)*1.2
		sceneAbility := simMapValue(p.SceneStrengths, scene) + simMapValue(p.SceneWeaknesses, scene)
		gapHours := 0.0
		if !st.Last.IsZero() {
			gapHours = now.Sub(st.Last).Hours()
			if gapHours < 0 {
				gapHours = 0
			}
		}
		retention := simClamp(p.RetentionBaseline*math.Exp(-p.ForgettingRate*gapHours/24), 0, 1)
		st.Retention = retention
		memoryAdjustment := (retention - .5) * .7
		effective := simClamp(patternAbility+sceneAbility+memoryAdjustment+st.Transfer*.3, 1, 8)
		target, _ := targetDifficulty(center, ability, patternAbility, ptn.difficulty, reason, review, probe, retention, cfgA)
		if cfg.Scene != "" && sceneAbility < 0 {
			target = simClamp(target-.15, 1, 8)
		}
		probability := 1 / (1 + math.Exp(-(effective-target)*1.15))
		noiseFailure := rng.Float64() < p.NoiseRate
		correct := rng.Float64() < probability
		if noiseFailure {
			correct = false
		}
		if probe {
			probes++
			if correct {
				probeSuccess++
			}
		}
		if review {
			reviews++
		}
		if math.Abs(target-ability) <= .75 && !probe {
			productive++
		}
		if target > prevTarget {
			if target-prevTarget > maxJump {
				maxJump = target - prevTarget
			}
		} else if prevTarget-target > maxJump {
			maxJump = prevTarget - target
		}
		if i > 0 {
			jitter += math.Abs(target - prevTarget)
		}
		prevTarget = target
		if ptn.id == prevPattern {
			consecutive++
		} else {
			consecutive = 1
		}
		if consecutive > maxConsecutive {
			maxConsecutive = consecutive
		}
		prevPattern = ptn.id
		if st.Attempts > 0 {
			samePatternGap += now.Sub(st.Last).Hours()
			samePatternGapN++
		}
		st.Attempts++
		if correct {
			st.Successes++
			st.Consecutive++
			correctTotal++
			st.Acquisition = simClamp(st.Acquisition+p.LearningRate*(1-st.Acquisition), 0, 1)
			st.Mastery = simClamp(st.Mastery+.15*(1-st.Mastery), 0, 1)
		} else {
			st.Failures++
			st.Consecutive = 0
			st.Mastery = simClamp(st.Mastery-.06, 0, 1)
		}
		st.Retention = simClamp(st.Retention+.08*boolFloat(correct)-.04*boolFloat(!correct), 0, 1)
		st.Last = now
		interval := nextSimulationInterval(st, correct, gapHours, p)
		st.LastInterval = interval
		st.Next = now.Add(time.Duration(interval * float64(time.Hour)))
		if st.Mastery > .72 && correct {
			st.Transfer = simClamp(st.Transfer+.04, 0, 1)
		}
		if correct && st.Scenes[scene] {
			st.Transfer = simClamp(st.Transfer+.025, 0, 1)
		}
		if correct {
			st.Scenes[scene] = true
		}
		sc.Attempts++
		if correct {
			sc.Successes++
		}
		sc.Patterns[ptn.id] = true
		sc.Intents[ptn.intent] = true
		sc.Mastery = simClamp(.62*sc.Mastery+.38*(float64(sc.Successes)/float64(sc.Attempts)), 0, 1)
		ability = simClamp(ability+(float64(boolFloat(correct))-.5)*p.LearningRate*.8, 1, 8)
		if st.Mastery < cfgA.WeakSkillThreshold {
			weakExposure++
		}
		if !st.Scenes[scene] {
			st.Scenes[scene] = true
		}
		newSkill := ptn.skill != "" && !unlocked[ptn.skill]
		if ptn.skill != "" && !unlocked[ptn.skill] && st.Mastery > .56 && prerequisiteReady(ptn.skill, states, unlocked, cfgA.MasteryThreshold) {
			unlocked[ptn.skill] = true
			unlocks = append(unlocks, SkillUnlockEvent{Skill: ptn.skill, AttemptIndex: i, VirtualTime: now, PrerequisiteState: map[string]float64{ptn.id: st.Mastery}, Reason: "mastery and prerequisite evidence"})
		}
		prompt := simulationChinesePrompt(ptn, scene)
		normalized := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
		seenPrompt[normalized]++
		band := simDifficultyBand(target)
		realized := simClamp(target+(rng.Float64()-.5)*.18, 1, 8)
		metrics.TargetDifficultyTrajectory = append(metrics.TargetDifficultyTrajectory, target)
		metrics.RealizedDifficultyTrajectory = append(metrics.RealizedDifficultyTrajectory, realized)
		attempt := SimulationAttempt{Index: i, Session: session + 1, VirtualTime: now, Exercise: SimulationExercise{ID: fmt.Sprintf("sim-%d", i+1), ChinesePrompt: prompt, PatternID: ptn.id, Pattern: ptn.expression, SceneID: scene, Intent: ptn.intent, DifficultyBand: band, Difficulty: target}, Correct: correct, Review: review, Probe: probe, NewSkill: newSkill, Reason: reason, TargetDifficulty: target, RealizedDifficulty: realized, EffectiveAbility: effective, SuccessProbability: probability, Verdict: map[bool]string{true: "correct", false: "incorrect"}[correct]}
		trace = append(trace, attempt)
		recent = append(recent, ptn.id)
		if len(recent) > cfgA.RecentPatternWindow {
			recent = recent[len(recent)-cfgA.RecentPatternWindow:]
		}
		key := band
		countsD[key]++
		if correct {
			successD[key]++
		}
		countsP[ptn.id]++
		if correct {
			successP[ptn.id]++
		}
		countsS[scene]++
		if correct {
			successS[scene]++
		}
		metrics.AcquisitionTrajectory = append(metrics.AcquisitionTrajectory, st.Acquisition)
		metrics.RetentionTrajectory = append(metrics.RetentionTrajectory, st.Retention)
		metrics.TransferTrajectory = append(metrics.TransferTrajectory, st.Transfer)
		if i%cfg.SessionSize == 0 {
			metrics.SessionDifficultyTrajectory = append(metrics.SessionDifficultyTrajectory, center)
			metrics.LearnerAbilityTrajectory = append(metrics.LearnerAbilityTrajectory, ability)
		}
		metrics.MemoryIntervalBefore = append(metrics.MemoryIntervalBefore, gapHours)
		metrics.MemoryIntervalAfter = append(metrics.MemoryIntervalAfter, interval)
	}
	for k, n := range countsD {
		metrics.AccuracyByDifficulty[k] = float64(successD[k]) / float64(n)
	}
	for k, n := range countsP {
		metrics.AccuracyByPattern[k] = float64(successP[k]) / float64(n)
	}
	for k, n := range countsS {
		metrics.AccuracyByScene[k] = float64(successS[k]) / float64(n)
		metrics.SceneCoverage[k] = float64(scenes[k].Attempts) / float64(cfg.Attempts)
		metrics.SceneMastery[k] = scenes[k].Mastery
	}
	metrics.OverallAccuracy = float64(correctTotal) / float64(cfg.Attempts)
	metrics.ReviewDue, metrics.ReviewServed, metrics.ProbeCount = reviews, reviews, probes
	metrics.UniquePatterns = len(countsP)
	intentSet := map[string]bool{}
	for _, x := range trace {
		intentSet[x.Exercise.Intent] = true
	}
	metrics.UniqueIntents = len(intentSet)
	metrics.DifficultyJitter = jitter / simMax(1, float64(cfg.Attempts-1))
	metrics.MaxAdjacentDifficultyJump = maxJump
	metrics.ProductiveZoneRatio = float64(productive) / float64(cfg.Attempts)
	metrics.WeakSkillExposure = float64(weakExposure) / float64(cfg.Attempts)
	metrics.ReviewHitRate = float64(correctReview(trace)) / float64(simMax(1, float64(reviews)))
	metrics.ProbeRatio = float64(probes) / float64(cfg.Attempts)
	metrics.ProbeSuccessRate = float64(probeSuccess) / float64(simMax(1, float64(probes)))
	metrics.UnknownToObserved = float64(len(countsP)) / float64(len(patternCatalog()))
	metrics.FoundationExposureRatio = foundationExposure(trace)
	metrics.MaxConsecutiveSamePattern = maxConsecutive
	metrics.SkillUnlocks = len(unlocks)
	if samePatternGapN > 0 {
		metrics.SamePatternSpacing = samePatternGap / float64(samePatternGapN)
	}
	exact := 0
	for _, n := range seenPrompt {
		if n > 1 {
			exact += n - 1
		}
	}
	metrics.ExactRepeatRate = float64(exact) / float64(cfg.Attempts)
	metrics.NormalizedExactDuplicateRate = metrics.ExactRepeatRate
	metrics.TransferEvents = transferEvents(trace)
	result.Attempts, result.Sessions, result.VirtualDays = len(trace), (len(trace)+cfg.SessionSize-1)/cfg.SessionSize, now.Sub(start).Hours()/24
	result.Metrics, result.UnlockEvents, result.AttemptsTrace = metrics, unlocks, trace
	result.Provider, result.Model = "deterministic", "local"
	result.HealthFlags = simulationHealthFlags(result, cfgA)
	return result
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
func simMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func correctReview(xs []SimulationAttempt) int {
	n := 0
	for _, x := range xs {
		if x.Review && x.Correct {
			n++
		}
	}
	return n
}
func transferEvents(xs []SimulationAttempt) int {
	seen := map[string]map[string]bool{}
	n := 0
	for _, x := range xs {
		if seen[x.Exercise.PatternID] == nil {
			seen[x.Exercise.PatternID] = map[string]bool{}
		}
		if len(seen[x.Exercise.PatternID]) > 0 && !seen[x.Exercise.PatternID][x.Exercise.SceneID] {
			n++
		}
		seen[x.Exercise.PatternID][x.Exercise.SceneID] = true
	}
	return n
}
func foundationExposure(xs []SimulationAttempt) float64 {
	n := 0
	for _, x := range xs {
		if x.TargetDifficulty < 2.5 {
			n++
		}
	}
	if len(xs) == 0 {
		return 0
	}
	return float64(n) / float64(len(xs))
}
func prerequisiteReady(skill string, states map[string]*simPatternState, unlocked map[string]bool, threshold float64) bool {
	if skill == "foundations" {
		return true
	}
	if skill == "conditionals" || skill == "professional" || skill == "nuance" {
		return unlocked["because"] || unlocked["past_tense"]
	}
	return true
}
func nextSimulationInterval(st *simPatternState, success bool, gap float64, p LearnerPersona) float64 {
	if !success {
		if gap > 24*3 {
			return .5
		}
		return 2.0 / 24
	}
	switch st.Consecutive {
	case 1:
		return 1
	case 2:
		return 3
	case 3:
		return 7
	case 4:
		return 14
	default:
		return 30
	}
}

func chooseSimulationPattern(patterns []patternDefinition, states map[string]*simPatternState, scenes map[string]*simSceneState, p LearnerPersona, now time.Time, center, ability float64, recent []string, consecutive int, cfg AdaptiveConfig, rng *rand.Rand) (patternDefinition, bool, bool, string) {
	if len(patterns) == 0 {
		return patternDefinition{}, false, false, "current_zone"
	}
	candidates := make([]patternDefinition, 0, len(patterns))
	for _, x := range patterns {
		st := states[x.id]
		if consecutive >= cfg.MaxPatternRepeats && len(recent) > 0 && recent[len(recent)-1] == x.id {
			continue
		}
		if st != nil && !st.Next.IsZero() && !now.Before(st.Next) {
			candidates = append(candidates, x)
		}
	}
	if len(candidates) == 0 {
		for _, x := range patterns {
			if consecutive < cfg.MaxPatternRepeats || len(recent) == 0 || recent[len(recent)-1] != x.id {
				candidates = append(candidates, x)
			}
		}
		if len(candidates) == 0 {
			candidates = patterns
		}
	}
	// Review first, then weak/unknown, then the current productive zone.
	best := candidates[0]
	bestScore := -1e9
	review, probe := false, false
	reason := "current_zone"
	for _, x := range candidates {
		st := states[x.id]
		score := rng.Float64() * .15
		weakness := simMapValue(p.PatternWeaknesses, x.id)
		score += -weakness*1.4 + (1-st.Mastery)*.8 - math.Abs(x.difficulty-center)*.45
		localReview := st != nil && !st.Next.IsZero() && !now.Before(st.Next)
		if localReview {
			score += 2
		}
		if score > bestScore {
			best, bestScore = x, score
			review = localReview
			reason = "weak_skill"
			if localReview {
				reason = "scheduled_review"
			}
		}
	}
	if len(recent) > 5 && rng.Float64() < cfg.ProbeMaxRatio {
		probe = true
		reason = "probe"
		review = false
	}
	if bestScore < -1e8 {
		reason = "current_zone"
	}
	_ = ability
	return best, review, probe, reason
}

func simulationHealthFlags(r SimulationResult, cfg AdaptiveConfig) []string {
	out := []string{}
	if r.Metrics.DifficultyJitter > cfg.MaxSessionCenterStep*4 {
		out = append(out, "SIM_DIFFICULTY_OSCILLATION")
	}
	if r.Metrics.WeakSkillExposure > .7 {
		out = append(out, "SIM_WEAK_OVEREXPOSURE")
	}
	if r.Metrics.ReviewHitRate < .5 {
		out = append(out, "SIM_REVIEW_STARVATION")
	}
	if r.Metrics.FoundationExposureRatio > .65 && r.Persona == "advanced-uneven" {
		out = append(out, "SIM_FOUNDATION_OVEREXPOSURE")
	}
	if r.Metrics.SceneCoverage["meeting"] > 0 && r.Metrics.AccuracyByScene["meeting"] < .3 {
		out = append(out, "SIM_SCENE_MISMATCH")
	}
	if r.Metrics.TransferEvents == 0 && r.Attempts > 20 {
		out = append(out, "SIM_TRANSFER_NOT_PROGRESSING")
	}
	if r.Metrics.SkillUnlocks > r.Attempts/2 {
		out = append(out, "SIM_UNLOCK_TOO_FAST")
	}
	if r.Metrics.SkillUnlocks == 0 && r.Attempts > 100 {
		out = append(out, "SIM_UNLOCK_STALLED")
	}
	return out
}

func writeSimulationReport(w io.Writer, result SimulationResult, jsonOutput bool) error {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}
	fmt.Fprintf(w, "Simulation %s\nPersona: %s\nMode: %s\nAttempts: %d\nSessions: %d\nMax allowed attempts: %d\nLearner provider/model: %s / %s\nEvaluator provider/model: %s / %s\nGenerator mode/provider/model: %s / %s / %s\nGenerator contract: %s\nPlanned/actual LLM calls: %d\nEstimated cost: %.3f\nSystem failures: %d\nAdaptive state updates/replans: %d/%d\nPattern matches: %v\nGenerator duplicate exact/normalized: %.1f%%/%.1f%%\nGenerator scene mismatch/difficulty rejects: %d/%d\nAccuracy: %.1f%%\nDifficulty jitter: %.3f\nMax jump: %.3f\nWeak exposure: %.1f%%\nReview due/served/hit: %d/%d/%.1f%%\nProbe count/ratio/success: %d/%.1f%%/%.1f%%\nUnique patterns/intents: %d/%d\nFoundation exposure: %.1f%%\nTransfer events: %d\nHealth flags: %s\n", result.SimulationVersion, result.Persona, result.Mode, result.Attempts, result.Sessions, result.MaxAllowedAttempts, result.LearnerProvider, result.LearnerModel, result.EvaluatorProvider, result.EvaluatorModel, result.GeneratorMode, result.GeneratorProvider, result.GeneratorModel, result.GeneratorContractVersion, result.AI.TotalCalls, result.AI.EstimatedCost, result.Metrics.SystemFailures, result.Metrics.AdaptiveStateUpdates, result.Metrics.AdaptiveNextExerciseReplans, result.Metrics.PatternMatchCounts, result.Metrics.GeneratorExactDuplicateRate*100, result.Metrics.GeneratorNormalizedDuplicateRate*100, result.Metrics.GeneratorSceneMismatchCount, result.Metrics.GeneratorDifficultyRejects, result.Metrics.OverallAccuracy*100, result.Metrics.DifficultyJitter, result.Metrics.MaxAdjacentDifficultyJump, result.Metrics.WeakSkillExposure*100, result.Metrics.ReviewDue, result.Metrics.ReviewServed, result.Metrics.ReviewHitRate*100, result.Metrics.ProbeCount, result.Metrics.ProbeRatio*100, result.Metrics.ProbeSuccessRate*100, result.Metrics.UniquePatterns, result.Metrics.UniqueIntents, result.Metrics.FoundationExposureRatio*100, result.Metrics.TransferEvents, strings.Join(result.HealthFlags, ", "))
	fmt.Fprintf(w, "Cost status: %s\nAdaptive replan eligible/misses: %d/%d\nGenerator requests/initial success/failure: %d/%d/%d\nGenerator structural/semantic/adapter failures: %d/%d/%d\nGenerator provider-empty/reasoning-only: %d/%d\nGenerator deterministic repair/LLM repair: %d/%d\nGenerator initial success/repair success/fresh retry success/fallback: %d/%d/%d/%d\nGenerator delivery/initial success/repair/retry/fallback rates: %.1f%%/%.1f%%/%.1f%%/%.1f%%/%.1f%%\nGenerator failure kinds: %v\n", result.AI.CostStatus, result.Metrics.AdaptiveReplanEligible, result.Metrics.AdaptiveReplanMisses, result.Metrics.GeneratorRequests, result.Metrics.GeneratorInitialSuccesses, result.Metrics.GeneratorInitialFailures, result.Metrics.GeneratorStructuralFailures, result.Metrics.GeneratorSemanticFailures, result.Metrics.GeneratorAdapterExtractionFailures, result.Metrics.GeneratorProviderEmptyCount, result.Metrics.GeneratorReasoningOnlyCount, result.Metrics.GeneratorDeterministicRepairSuccesses, result.Metrics.GeneratorLLMRepairSuccesses, result.Metrics.GeneratorInitialSuccesses, result.Metrics.GeneratorRepairSuccesses, result.Metrics.GeneratorFreshRetrySuccesses, result.Metrics.GeneratorFallbackCount, result.Metrics.GeneratorFinalDeliveryRate*100, result.Metrics.GeneratorInitialSuccessRate*100, result.Metrics.GeneratorRepairRate*100, result.Metrics.GeneratorRetryRate*100, result.Metrics.GeneratorFallbackRate*100, result.Metrics.GeneratorFailureKinds)
	return nil
}

func runSimulationCLI(args []string) error {
	cfg := DefaultSimulationConfig()
	preset := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "smoke":
			preset = "smoke"
			args = args[1:]
			cfg.Attempts = 200
		case "extended":
			preset = "extended"
			args = args[1:]
			cfg.Attempts = 1000
		case "ai-acceptance":
			args = args[1:]
			cfg.Mode = SimulationModeLLMLearner
			cfg.Attempts = 20
			cfg.AttemptsExplicit = true
		}
	}
	fs := flag.NewFlagSet("simulate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("mode", cfg.Mode, "simulation mode")
	persona := fs.String("persona", cfg.Persona, "persona")
	attempts := fs.Int("attempts", cfg.Attempts, "attempts")
	sessionSize := fs.Int("session-size", cfg.SessionSize, "attempts per session")
	seed := fs.Int64("seed", cfg.Seed, "fixed random seed")
	scene := fs.String("scene", "", "scene")
	subscene := fs.String("subscene", "", "subscene")
	profile := fs.String("time-profile", cfg.TimeProfile, "same-day, daily, irregular, or weekly")
	output := fs.String("output", "", "JSON report path")
	format := fs.String("format", "text", "text or json")
	dry := fs.Bool("dry-run", false, "plan AI calls without invoking a provider")
	allowLarge := fs.Bool("allow-large", false, "allow more than the default AI attempt cap")
	generator := fs.String("generator", cfg.Generator, "fixture-generator or real-generator")
	maxAttempts := fs.Int("max-attempts", cfg.MaxAttempts, "maximum AI attempts unless --allow-large is set")
	maxTokens := fs.Int("max-tokens", cfg.MaxTokens, "maximum tokens per AI call")
	timeoutSeconds := fs.Int("timeout", int(cfg.Timeout/time.Second), "AI provider timeout in seconds")
	costLimit := fs.Float64("estimated-cost-limit", cfg.EstimatedCostLimit, "estimated AI cost limit")
	learnerProvider := fs.String("learner-provider", "", "learner provider id")
	learnerModel := fs.String("learner-model", "", "learner model")
	evaluatorProvider := fs.String("evaluator-provider", "", "evaluator provider id")
	evaluatorModel := fs.String("evaluator-model", "", "evaluator model")
	generatorProvider := fs.String("generator-provider", "", "exercise generator provider id")
	generatorModel := fs.String("generator-model", "", "exercise generator model")
	simulationDB := fs.String("simulation-db", "", "dedicated simulation DB path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Mode = *mode
	cfg.Persona = *persona
	cfg.Attempts = *attempts
	cfg.AttemptsExplicit = true
	cfg.SessionSize = *sessionSize
	cfg.Seed = *seed
	cfg.Scene = *scene
	cfg.Subscene = *subscene
	cfg.TimeProfile = *profile
	cfg.Output = *output
	cfg.DryRun = *dry
	cfg.AllowLarge = *allowLarge
	cfg.Generator = *generator
	cfg.MaxAttempts = *maxAttempts
	cfg.MaxTokens = *maxTokens
	cfg.Timeout = time.Duration(*timeoutSeconds) * time.Second
	cfg.EstimatedCostLimit = *costLimit
	cfg.LearnerProvider, cfg.LearnerModel = *learnerProvider, *learnerModel
	cfg.EvaluatorProvider, cfg.EvaluatorModel = *evaluatorProvider, *evaluatorModel
	cfg.GeneratorProvider, cfg.GeneratorModel = *generatorProvider, *generatorModel
	cfg.SimulationDBPath = *simulationDB
	if cfg.Mode == SimulationModeDeterministic {
		cfg.Mode = SimulationModeAlgorithm
	}
	runner := &SimulationRunner{}
	if cfg.Mode != SimulationModeAlgorithm {
		configured, resolved, err := configuredSimulationRunner(cfg)
		if err != nil {
			return err
		}
		runner, cfg = configured, resolved
	}
	if preset == "smoke" || preset == "extended" {
		personas := []string{"beginner", "stable-intermediate", "noisy-learner", "scene-uneven"}
		if preset == "extended" {
			personas = []string{"beginner", "stable-intermediate", "advanced-uneven", "forgetful", "fast-learner", "noisy-learner", "scene-uneven"}
		}
		results := make([]SimulationResult, 0, len(personas))
		for _, personaID := range personas {
			one := cfg
			one.Persona = personaID
			result, err := runner.Run(context.Background(), one)
			if err != nil {
				return err
			}
			results = append(results, result)
		}
		var w io.Writer = os.Stdout
		var file *os.File
		if cfg.Output != "" {
			var createErr error
			file, createErr = os.Create(cfg.Output)
			if createErr != nil {
				return createErr
			}
			defer file.Close()
			w = file
		}
		jsonOutput := strings.EqualFold(*format, "json") || cfg.Output != ""
		if jsonOutput {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(results)
		}
		for _, result := range results {
			if err := writeSimulationReport(w, result, false); err != nil {
				return err
			}
		}
		return nil
	}
	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		return err
	}
	var w io.Writer = os.Stdout
	var file *os.File
	if cfg.Output != "" {
		var createErr error
		file, createErr = os.Create(cfg.Output)
		if createErr != nil {
			return createErr
		}
		defer file.Close()
		w = file
	}
	return writeSimulationReport(w, result, strings.EqualFold(*format, "json") || cfg.Output != "")
}

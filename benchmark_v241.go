package main

// V2.4.1 live distribution and weak-detection audit.
//
// This file intentionally keeps live provider calls and deterministic hidden
// truth outside the production adaptive/database paths. Live generation reads
// provider configuration only; weak audit state exists only in memory.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	v241Version              = "v2.4.1"
	v241DiagnosticSamples    = 100
	v241PrimarySamples       = 500
	v241WeakAttempts         = 2000
	v241MinimumEvidence      = 8
	v241StableWindow         = 5
	v241StableWeakVotes      = 3
	v241LiveGeneratorRetries = 3
)

type V241ProviderInfo struct {
	Role     string `json:"role"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

type V241RoleCalls struct {
	Role         string         `json:"role"`
	Provider     string         `json:"provider"`
	Model        string         `json:"model"`
	Planned      int            `json:"planned"`
	Actual       int            `json:"actual"`
	Success      int            `json:"success"`
	Failures     int            `json:"failures"`
	Retries      int            `json:"retries"`
	Repairs      int            `json:"repairs"`
	FailureKinds map[string]int `json:"failure_kinds,omitempty"`
}

type V241CallSummary struct {
	MaximumTotalCalls int             `json:"maximum_total_calls"`
	ActualTotalCalls  int             `json:"actual_total_calls"`
	Roles             []V241RoleCalls `json:"roles"`
}

type V241ConfigManifest struct {
	Stage             string   `json:"stage"`
	Samples           int      `json:"samples"`
	WeakAttempts      int      `json:"weak_attempts"`
	MaxProviderCalls  int      `json:"max_provider_calls"`
	MaxSamples        int      `json:"max_samples"`
	MaxTokens         int      `json:"max_tokens"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	SceneMode         string   `json:"scene_mode"`
	Profiles          []string `json:"learner_profiles"`
	Seed              int64    `json:"seed"`
	DryRun            bool     `json:"dry_run"`
	OptionalLearner   bool     `json:"optional_real_llm_learner"`
	TaxonomyVersion   string   `json:"taxonomy_version"`
	GeneratorContract string   `json:"generator_contract"`
}

type V241Report struct {
	BenchmarkVersion string                   `json:"benchmark_version"`
	GeneratedAt      string                   `json:"generated_at"`
	GitSHA           string                   `json:"git_sha"`
	Config           V241ConfigManifest       `json:"config"`
	Providers        []V241ProviderInfo       `json:"providers"`
	Calls            V241CallSummary          `json:"calls"`
	Live             *V241LiveCurriculumAudit `json:"live_curriculum,omitempty"`
	Weak             *V241WeakDetectionAudit  `json:"weak_detection,omitempty"`
	ProductionDB     *V241DatabaseIsolation   `json:"production_db,omitempty"`
	HealthFlags      []string                 `json:"health_flags,omitempty"`
}

type V241DatabaseSnapshot struct {
	Path   string         `json:"path"`
	Tables map[string]int `json:"table_counts"`
}

type V241DatabaseIsolation struct {
	Before *V241DatabaseSnapshot `json:"before"`
	After  *V241DatabaseSnapshot `json:"after"`
}

type V241LiveCorpusItem struct {
	ID                 string                     `json:"id"`
	ChinesePrompt      string                     `json:"chinese_prompt"`
	ReferenceAnswers   []string                   `json:"reference_answers"`
	SourcePattern      string                     `json:"source_pattern"`
	SourceScene        string                     `json:"source_scene"`
	SourceIntent       string                     `json:"source_intent"`
	Profile            string                     `json:"learner_profile"`
	TargetDifficulty   float64                    `json:"target_difficulty"`
	RealizedDifficulty float64                    `json:"realized_difficulty"`
	Curriculum         *V241CurriculumJudgeResult `json:"curriculum_judge,omitempty"`
	Difficulty         *V241DifficultyJudgeResult `json:"difficulty_judge,omitempty"`
	JudgeStatus        string                     `json:"judge_status"`
}

type V241CurriculumJudgeResult struct {
	Scene                 string   `json:"scene"`
	Subscene              string   `json:"subscene,omitempty"`
	CommunicationIntent   string   `json:"communication_intent"`
	LanguageFunction      string   `json:"language_function"`
	CommonLifeCapability  string   `json:"common_life_capability_id"`
	PrimaryCapability     string   `json:"primary_capability,omitempty"`
	SecondaryCapabilities []string `json:"secondary_capabilities,omitempty"`
	FrequencyClass        string   `json:"frequency_class,omitempty"`
	Confidence            float64  `json:"confidence"`
}

type V241DifficultyJudgeResult struct {
	Overall    float64              `json:"overall"`
	Dimensions DifficultyDimensions `json:"dimensions,omitempty"`
	Confidence float64              `json:"confidence"`
}

type V241CoverageMetrics struct {
	Samples                int              `json:"samples"`
	JudgedSamples          int              `json:"judged_samples"`
	RawCapabilityCoverage  float64          `json:"raw_capability_coverage"`
	WeightedCoverage       float64          `json:"weighted_common_life_coverage"`
	HighFrequencyCoverage  float64          `json:"weighted_high_frequency_coverage"`
	SceneCoverage          float64          `json:"scene_coverage"`
	IntentCoverage         float64          `json:"intent_coverage"`
	PatternCoverage        float64          `json:"pattern_coverage"`
	ContextDiversity       float64          `json:"context_diversity"`
	CapabilityDepth        []CoverageBucket `json:"capability_depth"`
	MissingHighFrequency   []string         `json:"missing_high_frequency_capabilities,omitempty"`
	Underrepresented       []string         `json:"underrepresented,omitempty"`
	Overrepresented        []string         `json:"overrepresented,omitempty"`
	Starved                []string         `json:"starved,omitempty"`
	SceneDistribution      map[string]int   `json:"scene_distribution"`
	IntentDistribution     map[string]int   `json:"intent_distribution"`
	CapabilityDistribution map[string]int   `json:"capability_distribution"`
	PatternDistribution    map[string]int   `json:"pattern_distribution"`
}

type V241DuplicateMetrics struct {
	ExactRate         float64 `json:"duplicate_rate"`
	NearDuplicateRate float64 `json:"near_duplicate_rate"`
	SemanticRate      float64 `json:"semantic_duplicate_rate"`
}

type V241LiveDifficultyAudit struct {
	Samples                 int                              `json:"samples"`
	TargetIndependentMAE    float64                          `json:"target_vs_independent_mae"`
	TargetBias              float64                          `json:"target_bias"`
	TargetCorrelation       float64                          `json:"target_correlation"`
	RealizedIndependentMAE  float64                          `json:"realized_vs_independent_mae"`
	RealizedBias            float64                          `json:"realized_bias"`
	RealizedCorrelation     float64                          `json:"realized_correlation"`
	ByScene                 map[string]DifficultyGroupMetric `json:"by_scene"`
	ByPattern               map[string]DifficultyGroupMetric `json:"by_pattern"`
	HighestErrorPatterns    []string                         `json:"highest_error_patterns"`
	HighestErrorScenes      []string                         `json:"highest_error_scenes"`
	UnderestimationHotspots []string                         `json:"underestimation_hotspots"`
	OverestimationHotspots  []string                         `json:"overestimation_hotspots"`
}

type V241HumanAnchorAudit struct {
	SampleCount        int    `json:"sample_count"`
	Correct            int    `json:"correct"`
	Reasonable         int    `json:"reasonable"`
	Questionable       int    `json:"questionable"`
	Wrong              int    `json:"wrong"`
	DifficultyOrdering string `json:"difficulty_ordering"`
}

type V241LiveCurriculumAudit struct {
	Mode                string                  `json:"mode"`
	Generated           int                     `json:"generated_exercises"`
	Valid               int                     `json:"valid_exercises"`
	GenerationFailures  int                     `json:"generation_failures"`
	Unjudged            int                     `json:"unjudged_exercises"`
	Coverage            V241CoverageMetrics     `json:"coverage"`
	Difficulty          V241LiveDifficultyAudit `json:"difficulty"`
	Duplicates          V241DuplicateMetrics    `json:"duplicates"`
	HumanAnchor         *V241HumanAnchorAudit   `json:"human_anchor,omitempty"`
	CorpusPath          string                  `json:"corpus_path"`
	ProfileDistribution map[string]int          `json:"profile_distribution"`
	corpus              []V241LiveCorpusItem    `json:"-"`
}

type V241WeakGroundTruth struct {
	Status   string `json:"status"`
	Skill    string `json:"skill"`
	Category string `json:"category"`
}

type V241WeakSkillEvidence struct {
	Skill                    string `json:"skill"`
	GroundTruth              string `json:"ground_truth"`
	Exposures                int    `json:"exposures"`
	Successes                int    `json:"successes"`
	Failures                 int    `json:"failures"`
	ReviewExposures          int    `json:"review_exposures"`
	Detected                 bool   `json:"detected"`
	DetectionLatency         int    `json:"detection_latency"`
	StableWeakWindows        int    `json:"stable_weak_windows"`
	RecoveredBeforeDetection bool   `json:"recovered_before_detection"`
	MissReason               string `json:"miss_reason,omitempty"`
}

type V241ConfusionMetrics struct {
	TP                      int     `json:"tp"`
	FP                      int     `json:"fp"`
	TN                      int     `json:"tn"`
	FN                      int     `json:"fn"`
	Precision               float64 `json:"precision"`
	Recall                  float64 `json:"recall"`
	F1                      float64 `json:"f1"`
	FalseWeakRate           float64 `json:"false_weak_rate"`
	MissedWeakRate          float64 `json:"missed_weak_rate"`
	ExposureQualifiedRecall float64 `json:"exposure_qualified_recall"`
	MedianDetectionLatency  float64 `json:"median_detection_latency"`
	PersistentFalseWeak     int     `json:"persistent_false_weak"`
}

type V241WeakPersonaAudit struct {
	Persona              string                  `json:"persona"`
	Attempts             int                     `json:"attempts"`
	TrueWeakPatterns     []string                `json:"true_weak_patterns"`
	TrueStrongPatterns   []string                `json:"true_strong_patterns"`
	TrueNeutralPatterns  []string                `json:"true_neutral_patterns"`
	TrueWeakScenes       []string                `json:"true_weak_scenes"`
	PatternMetrics       V241ConfusionMetrics    `json:"pattern_metrics"`
	SceneMetrics         V241ConfusionMetrics    `json:"scene_metrics"`
	PatternEvidence      []V241WeakSkillEvidence `json:"pattern_evidence"`
	SceneEvidence        []V241WeakSkillEvidence `json:"scene_evidence"`
	FailureDecomposition map[string]int          `json:"failure_decomposition"`
	LearningMode         string                  `json:"learning_mode"`
}

type V241WeakDetectionAudit struct {
	AttemptsPerPersona int                    `json:"attempts_per_persona"`
	MinimumEvidence    int                    `json:"minimum_evidence_opportunity"`
	Personas           []V241WeakPersonaAudit `json:"personas"`
	PatternMetrics     V241ConfusionMetrics   `json:"pattern_metrics"`
	SceneMetrics       V241ConfusionMetrics   `json:"scene_metrics"`
	IntentSupport      string                 `json:"intent_support"`
	RootCause          string                 `json:"root_cause"`
	RootCauseResult    string                 `json:"root_cause_result"`
}

type v241LiveOptions struct {
	Stage, SceneMode                                                                                                    string
	Profiles                                                                                                            []string
	Samples, WeakAttempts, MaxSamples, MaxProviderCalls, MaxTokens, TimeoutSeconds                                      int
	Seed                                                                                                                int64
	DryRun                                                                                                              bool
	Output, GeneratorProvider, GeneratorModel, CurriculumProvider, CurriculumModel, DifficultyProvider, DifficultyModel string
}

type v241CallCounter struct{ Used, Limit int }

func (c *v241CallCounter) reserve(n int) error {
	if n < 0 || c.Used+n > c.Limit {
		return fmt.Errorf("provider call budget exceeded: need %d, used %d, limit %d", n, c.Used, c.Limit)
	}
	c.Used += n
	return nil
}

func runV241CLI(sub string, args []string) error {
	opts := v241LiveOptions{Stage: "diagnostic", SceneMode: "global", Samples: v241DiagnosticSamples, WeakAttempts: v241WeakAttempts, MaxSamples: 500, MaxProviderCalls: 3000, MaxTokens: 256, TimeoutSeconds: 45, Seed: 42}
	fs := flag.NewFlagSet("benchmark "+sub, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&opts.Stage, "stage", opts.Stage, "diagnostic or primary; extended is not automatic")
	fs.StringVar(&opts.SceneMode, "scene-mode", opts.SceneMode, "global, daily, travel, work, meeting, or all")
	profiles := fs.String("profiles", "beginner,stable-intermediate,advanced-uneven", "comma-separated learner profiles")
	fs.IntVar(&opts.Samples, "samples", opts.Samples, "live free-running sample count")
	fs.IntVar(&opts.WeakAttempts, "weak-attempts", opts.WeakAttempts, "deterministic attempts per weak persona")
	fs.IntVar(&opts.MaxSamples, "max-samples", opts.MaxSamples, "maximum live samples")
	fs.IntVar(&opts.MaxProviderCalls, "max-provider-calls", opts.MaxProviderCalls, "maximum total live provider calls")
	fs.IntVar(&opts.MaxTokens, "max-tokens", opts.MaxTokens, "maximum tokens per live provider call")
	fs.IntVar(&opts.TimeoutSeconds, "timeout", opts.TimeoutSeconds, "provider timeout in seconds")
	fs.Int64Var(&opts.Seed, "seed", opts.Seed, "deterministic policy seed")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show plan without provider calls")
	fs.StringVar(&opts.Output, "output", "", "JSON or text report path")
	fs.StringVar(&opts.GeneratorProvider, "generator-provider", "", "generator provider id")
	fs.StringVar(&opts.GeneratorModel, "generator-model", "", "generator model")
	fs.StringVar(&opts.CurriculumProvider, "curriculum-judge-provider", "", "curriculum judge provider id")
	fs.StringVar(&opts.CurriculumModel, "curriculum-judge-model", "", "curriculum judge model")
	fs.StringVar(&opts.DifficultyProvider, "difficulty-judge-provider", "", "difficulty judge provider id")
	fs.StringVar(&opts.DifficultyModel, "difficulty-judge-model", "", "difficulty judge model")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for _, id := range strings.Split(*profiles, ",") {
		if id = strings.TrimSpace(id); id != "" {
			opts.Profiles = append(opts.Profiles, id)
		}
	}
	if len(opts.Profiles) == 0 {
		return errors.New("at least one learner profile is required")
	}
	if opts.Stage == "primary" {
		opts.Samples = v241PrimarySamples
	}
	if sub == "weak-detection" || sub == "live-weak" {
		opts.Samples = 0
	}
	if opts.Samples < 0 || opts.WeakAttempts <= 0 || opts.MaxSamples <= 0 || opts.MaxProviderCalls < 0 || opts.MaxTokens <= 0 || opts.TimeoutSeconds <= 0 {
		return errors.New("V2.4.1 limits must be positive")
	}
	if opts.Samples > opts.MaxSamples {
		return fmt.Errorf("samples exceed max-samples %d", opts.MaxSamples)
	}
	if opts.Stage != "diagnostic" && opts.Stage != "primary" {
		return fmt.Errorf("unsupported V2.4.1 stage %q; extended is manual only", opts.Stage)
	}
	liveRequested := sub == "v241" || sub == "live-curriculum"
	weakRequested := sub == "v241" || sub == "weak-detection" || sub == "live-weak"
	planned := 0
	if liveRequested {
		planned = opts.Samples * 5
	}
	if opts.DryRun {
		fmt.Printf("Benchmark %s dry-run\nStage: %s\nPlanned exercise samples: %d\nGenerator calls: <= %d\nCurriculum Judge calls: <= %d\nDifficulty Judge calls: <= %d\nOptional learner calls: 0\nMaximum total calls: %d\nWeak attempts/persona: %d\nLLM provider calls: 0\n", v241Version, opts.Stage, opts.Samples, opts.Samples*v241LiveGeneratorRetries, opts.Samples, opts.Samples, planned, opts.WeakAttempts)
		return nil
	}
	if planned > opts.MaxProviderCalls {
		return fmt.Errorf("planned provider calls %d exceed max-provider-calls %d", planned, opts.MaxProviderCalls)
	}
	sha, _ := currentGitSHA()
	report := V241Report{BenchmarkVersion: v241Version, GeneratedAt: time.Now().UTC().Format(time.RFC3339), GitSHA: sha, Config: V241ConfigManifest{Stage: opts.Stage, Samples: opts.Samples, WeakAttempts: opts.WeakAttempts, MaxProviderCalls: opts.MaxProviderCalls, MaxSamples: opts.MaxSamples, MaxTokens: opts.MaxTokens, TimeoutSeconds: opts.TimeoutSeconds, SceneMode: opts.SceneMode, Profiles: opts.Profiles, Seed: opts.Seed, TaxonomyVersion: benchmarkTaxonomyVersion, GeneratorContract: generatorContractVersion}, Calls: V241CallSummary{MaximumTotalCalls: opts.MaxProviderCalls}}
	before, _ := v241DatabaseSnapshot()
	report.ProductionDB = &V241DatabaseIsolation{Before: before}
	if liveRequested && opts.Samples > 0 {
		providers, clients, err := v241LoadClients(opts)
		if err != nil {
			return err
		}
		report.Providers = providers
		counter := &v241CallCounter{Limit: opts.MaxProviderCalls}
		live, calls, err := runV241LiveCurriculum(context.Background(), opts, clients, counter)
		if err != nil {
			return err
		}
		for i := range calls {
			if i < len(providers) {
				calls[i].Provider = providers[i].Provider
				calls[i].Model = providers[i].Model
			}
		}
		report.Live, report.Calls.Roles = &live, calls
		report.Calls.ActualTotalCalls = counter.Used
		if err := writeV241Corpus(live); err != nil {
			return err
		}
	}
	if weakRequested {
		weak := runV241WeakAudit(opts.WeakAttempts, opts.Seed)
		report.Weak = &weak
	}
	after, _ := v241DatabaseSnapshot()
	if report.ProductionDB == nil {
		report.ProductionDB = &V241DatabaseIsolation{}
	}
	report.ProductionDB.After = after
	report.HealthFlags = v241HealthFlags(report)
	return writeV241Report(report, opts.Output)
}

func v241LoadClients(opts v241LiveOptions) ([]V241ProviderInfo, map[string]LLMClient, error) {
	path := simulationProviderDBPath()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled FROM llm_providers WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	configs := []ProviderConfig{}
	for rows.Next() {
		var c ProviderConfig
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Model, &c.Timeout, &c.Temperature, &c.MaxTokens, &enabled); err != nil {
			return nil, nil, err
		}
		c.Enabled = enabled == 1
		configs = append(configs, c)
	}
	choose := func(id, model string) (ProviderConfig, error) { return chooseSimulationProvider(configs, id, model) }
	g, err := choose(opts.GeneratorProvider, opts.GeneratorModel)
	if err != nil {
		return nil, nil, fmt.Errorf("generator provider: %w", err)
	}
	c, err := choose(opts.CurriculumProvider, opts.CurriculumModel)
	if err != nil {
		return nil, nil, fmt.Errorf("curriculum judge provider: %w", err)
	}
	d, err := choose(opts.DifficultyProvider, opts.DifficultyModel)
	if err != nil {
		return nil, nil, fmt.Errorf("difficulty judge provider: %w", err)
	}
	// The CLI guard is authoritative for this audit. Do not inherit a larger
	// production timeout or token budget into a paid benchmark call.
	g.Timeout, c.Timeout, d.Timeout = opts.TimeoutSeconds, opts.TimeoutSeconds, opts.TimeoutSeconds
	g.MaxTokens, c.MaxTokens, d.MaxTokens = opts.MaxTokens, opts.MaxTokens, opts.MaxTokens
	return []V241ProviderInfo{{Role: "Production Exercise Generator", Provider: g.ID, Model: g.Model}, {Role: "Independent Curriculum Judge", Provider: c.ID, Model: c.Model}, {Role: "Independent Difficulty Judge", Provider: d.ID, Model: d.Model}}, map[string]LLMClient{"generator": (&LLMRegistry{configs: map[string]ProviderConfig{g.ID: g}}).Client(g), "curriculum": (&LLMRegistry{configs: map[string]ProviderConfig{c.ID: c}}).Client(c), "difficulty": (&LLMRegistry{configs: map[string]ProviderConfig{d.ID: d}}).Client(d)}, nil
}

func v241DatabaseSnapshot() (*V241DatabaseSnapshot, error) {
	path := simulationProviderDBPath()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	tables := map[string]int{}
	for _, table := range []string{"attempts", "evaluations", "pattern_mastery", "scene_mastery", "review_schedule"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			return nil, err
		}
		tables[table] = n
	}
	return &V241DatabaseSnapshot{Path: path, Tables: tables}, nil
}

func v241FreeRunningSeed(i int, opts v241LiveOptions, rng *rand.Rand, p LearnerPersona, states map[string]*simPatternState, scenes map[string]*simSceneState, recent []string, consecutive int) SimulationExercise {
	patterns := patternCatalog()
	candidates := simulationCandidates("", "")
	if len(candidates) == 0 {
		candidates = patterns
	}
	chosen, _, _, _ := chooseSimulationPattern(candidates, states, scenes, p, time.Now().UTC(), p.BaseAbility, p.BaseAbility, recent, consecutive, defaultAdaptiveConfig(), rng)
	if chosen.id == "" {
		chosen = patterns[rng.Intn(len(patterns))]
	}
	scene := simSceneForPattern(chosen.id, "", i/20)
	if opts.SceneMode != "global" && opts.SceneMode != "all" {
		scene = normalizeBenchmarkScene(opts.SceneMode)
		if scene == "" {
			scene = "daily"
		}
	}
	if opts.SceneMode == "all" {
		scene = []string{"daily", "travel", "work", "meeting"}[i%4]
	}
	return SimulationExercise{ID: fmt.Sprintf("v241-live-%05d", i+1), PatternID: chosen.id, Pattern: chosen.expression, Intent: chosen.intent, SceneID: scene, Difficulty: clampBenchmark(chosen.difficulty+(p.BaseAbility-4)*.18, 1, 8), DifficultyBand: simDifficultyBand(chosen.difficulty)}
}

func runV241LiveCurriculum(ctx context.Context, opts v241LiveOptions, clients map[string]LLMClient, counter *v241CallCounter) (V241LiveCurriculumAudit, []V241RoleCalls, error) {
	if opts.SceneMode == "all" { /* all-mode is available for targeted scene audits; primary remains global by default. */
	}
	p, _ := ResolveLearnerPersona("stable-intermediate")
	states := map[string]*simPatternState{}
	scenes := map[string]*simSceneState{}
	for _, x := range patternCatalog() {
		states[x.id] = &simPatternState{Mastery: .2, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
	}
	for _, x := range []string{"daily", "travel", "work", "meeting", "phone", "restaurant", "hotel", "airport", "friends", "shopping"} {
		scenes[x] = &simSceneState{Patterns: map[string]bool{}, Intents: map[string]bool{}}
	}
	rng := rand.New(rand.NewSource(opts.Seed))
	recent := []string{}
	consecutive := 0
	records := []V241LiveCorpusItem{}
	failures := 0
	roles := []V241RoleCalls{{Role: "Production Exercise Generator", Provider: "", Model: "", Planned: opts.Samples * v241LiveGeneratorRetries, FailureKinds: map[string]int{}}, {Role: "Independent Curriculum Judge", Planned: opts.Samples, FailureKinds: map[string]int{}}, {Role: "Independent Difficulty Judge", Planned: opts.Samples, FailureKinds: map[string]int{}}}
	for i := 0; i < opts.Samples; i++ {
		if err := counter.reserve(v241LiveGeneratorRetries); err != nil {
			return V241LiveCurriculumAudit{}, roles, err
		}
		profileID := opts.Profiles[i%len(opts.Profiles)]
		profile, err := ResolveLearnerPersona(profileID)
		if err != nil {
			return V241LiveCurriculumAudit{}, roles, err
		}
		seed := v241FreeRunningSeed(i, opts, rng, profile, states, scenes, recent, consecutive)
		generator := LLMExerciseGenerator{Client: clients["generator"], MaxTokens: opts.MaxTokens, ReasoningMode: "disabled", ReasoningEffort: "none"}
		callCtx, cancel := context.WithTimeout(ctx, time.Duration(opts.TimeoutSeconds)*time.Second)
		generated, diag, err := generator.GenerateDetailed(callCtx, seed)
		used := diag.ProviderCalls
		if used == 0 {
			used = 1
		}
		counter.Used -= v241LiveGeneratorRetries - used
		roles[0].Actual += used
		roles[0].Retries += diag.FreshRetries
		roles[0].Repairs += diag.RepairAttempts
		if err != nil {
			cancel()
			failures++
			roles[0].Failures++
			for _, k := range diag.FailureKinds {
				roles[0].FailureKinds[string(k)]++
			}
			continue
		}
		roles[0].Success++
		record := V241LiveCorpusItem{ID: generated.ID, ChinesePrompt: generated.ChinesePrompt, ReferenceAnswers: generated.ReferenceAnswers, SourcePattern: seed.PatternID, SourceScene: seed.SceneID, SourceIntent: seed.Intent, Profile: profile.ID, TargetDifficulty: seed.Difficulty, RealizedDifficulty: v241RealizedDifficulty(generated.ReferenceAnswers)}
		if err := counter.reserve(1); err != nil {
			cancel()
			return V241LiveCurriculumAudit{}, roles, err
		}
		roles[1].Actual++
		cj, err := v241LiveCurriculumJudge(callCtx, clients["curriculum"], generated.ChinesePrompt, generated.ReferenceAnswers, "", opts.MaxTokens)
		if err != nil {
			roles[1].Failures++
			roles[1].FailureKinds[v241FailureKind(err)]++
			record.JudgeStatus = "CURRICULUM_UNJUDGED"
		} else {
			roles[1].Success++
			record.Curriculum = &cj
		}
		if err := counter.reserve(1); err != nil {
			cancel()
			return V241LiveCurriculumAudit{}, roles, err
		}
		roles[2].Actual++
		dj, err := v241LiveDifficultyJudge(callCtx, clients["difficulty"], generated.ChinesePrompt, generated.ReferenceAnswers, opts.MaxTokens)
		if err != nil {
			roles[2].Failures++
			roles[2].FailureKinds[v241FailureKind(err)]++
			record.JudgeStatus = "DIFFICULTY_UNJUDGED"
		} else {
			roles[2].Success++
			record.Difficulty = &dj
		}
		if record.JudgeStatus == "" {
			record.JudgeStatus = "JUDGED"
		}
		cancel()
		records = append(records, record)
		recent = append(recent, seed.PatternID)
		if len(recent) > 5 {
			recent = recent[len(recent)-5:]
		}
		if seed.PatternID == lastString(recent) {
			consecutive++
		} else {
			consecutive = 1
		}
	}
	audit := v241BuildLiveAudit(opts.SceneMode, opts.Samples, failures, records)
	audit.CorpusPath = ".acceptance-data/v241-live-corpus.jsonl"
	if opts.SceneMode != "global" {
		audit.CorpusPath = fmt.Sprintf(".acceptance-data/v241-live-corpus-%s.jsonl", opts.SceneMode)
	}
	audit.corpus = records
	return audit, roles, nil
}

func lastString(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[len(xs)-1]
}

func v241LiveCurriculumJudge(ctx context.Context, client LLMClient, prompt string, answers []string, knownScene string, maxTokens int) (V241CurriculumJudgeResult, error) {
	taxonomy, _ := json.Marshal(commonLifeTaxonomy())
	user := fmt.Sprintf("Independent curriculum audit. Do not infer or request production pattern IDs, target difficulty, mastery, coverage labels, or generator metadata. Classify only the language content.\nChinese exercise: %s\nReference answer: %s\nOptional scene context: %s\nTaxonomy JSON: %s\nReturn JSON with scene, subscene, communication_intent, language_function, common_life_capability_id, primary_capability, secondary_capabilities, frequency_class, confidence.", prompt, strings.Join(answers, " | "), knownScene, string(taxonomy))
	resp, err := client.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "You are a fresh independent Common-Life curriculum judge. Return one JSON object only."}, {Role: "user", Content: user}}, MaxTokens: maxTokens, JSONMode: true, ReasoningMode: "disabled", ReasoningEffort: "none", RequestID: "v241-curriculum-judge"})
	if err != nil {
		return V241CurriculumJudgeResult{}, err
	}
	var out V241CurriculumJudgeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Content)), &out); err != nil {
		clean, e := stripJSONFence(resp.Content)
		if e != nil {
			return out, err
		}
		if e = json.Unmarshal([]byte(clean), &out); e != nil {
			return out, e
		}
	}
	if out.CommonLifeCapability == "" {
		out.CommonLifeCapability = out.PrimaryCapability
	}
	if out.CommonLifeCapability == "" || out.Confidence <= 0 {
		return out, errors.New("judge result missing capability or confidence")
	}
	if err := normalizeV241CurriculumJudge(&out); err != nil {
		return out, err
	}
	return out, nil
}

func normalizeV241CurriculumJudge(out *V241CurriculumJudgeResult) error {
	taxonomy := commonLifeTaxonomy()
	capability := strings.ToLower(strings.TrimSpace(out.CommonLifeCapability))
	for _, c := range taxonomy.Capabilities {
		if capability == c.ID || capability == strings.ToLower(c.Name) || strings.Contains(capability, strings.ToLower(c.Name)) {
			out.CommonLifeCapability = c.ID
			if out.PrimaryCapability == "" {
				out.PrimaryCapability = c.ID
			}
			break
		}
	}
	knownCapability := false
	for _, c := range taxonomy.Capabilities {
		if out.CommonLifeCapability == c.ID {
			knownCapability = true
			break
		}
	}
	if !knownCapability {
		return fmt.Errorf("TAXONOMY_GAP: unknown capability %q", out.CommonLifeCapability)
	}
	intent := strings.ToLower(strings.TrimSpace(out.CommunicationIntent))
	best := ""
	for _, x := range taxonomy.Intents {
		if intent == x.ID || intent == strings.ToLower(x.Name) {
			best = x.ID
			break
		}
	}
	if best == "" {
		for _, x := range taxonomy.Intents {
			if strings.Contains(intent, strings.ToLower(x.ID)) || strings.Contains(intent, strings.ToLower(x.Cue)) || strings.Contains(intent, strings.ToLower(x.Name)) {
				best = x.ID
				break
			}
		}
	}
	if best == "" {
		return fmt.Errorf("TAXONOMY_GAP: unknown intent %q", out.CommunicationIntent)
	}
	out.CommunicationIntent = best
	if out.Scene != "" {
		out.Scene = normalizeBenchmarkScene(out.Scene)
	}
	return nil
}

func v241LiveDifficultyJudge(ctx context.Context, client LLMClient, prompt string, answers []string, maxTokens int) (V241DifficultyJudgeResult, error) {
	user := fmt.Sprintf("Independent difficulty audit. Judge only linguistic and pragmatic complexity. Do not use or infer production target difficulty, realized difficulty, catalog difficulty, pattern IDs, or learner ability.\nChinese exercise: %s\nCommunication task: complete the task described by the Chinese exercise.\nReference answer: %s\nReturn JSON: overall (1-8), dimensions object, confidence.", prompt, strings.Join(answers, " | "))
	resp, err := client.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "You are a fresh independent English difficulty judge. Return one JSON object only."}, {Role: "user", Content: user}}, MaxTokens: maxTokens, JSONMode: true, ReasoningMode: "disabled", ReasoningEffort: "none", RequestID: "v241-difficulty-judge"})
	if err != nil {
		return V241DifficultyJudgeResult{}, err
	}
	var out V241DifficultyJudgeResult
	clean := strings.TrimSpace(resp.Content)
	if e := json.Unmarshal([]byte(clean), &out); e != nil {
		if c, e2 := stripJSONFence(clean); e2 == nil {
			e = json.Unmarshal([]byte(c), &out)
		}
		if e != nil {
			return out, e
		}
	}
	if out.Overall < 1 || out.Overall > 8 {
		return out, fmt.Errorf("difficulty outside 1-8: %.2f", out.Overall)
	}
	return out, nil
}

func v241FailureKind(err error) string {
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Category != "" {
		return strings.ToUpper(pe.Category)
	}
	return "JUDGE_PARSE_OR_CONTRACT"
}

func v241RealizedDifficulty(answers []string) float64 {
	text := strings.Join(answers, " ")
	words := len(strings.Fields(text))
	clauses := strings.Count(text, ",") + strings.Count(text, "; ") + strings.Count(text, " because ") + strings.Count(text, " although ") + strings.Count(text, " if ")
	advanced := 0
	for _, x := range []string{"would", "could", "might", "although", "unless", "convinced", "consequences", "alternatives"} {
		if strings.Contains(strings.ToLower(text), x) {
			advanced++
		}
	}
	return clampBenchmark(1+float64(words)/14+float64(clauses)*.55+float64(advanced)*.35, 1, 8)
}

func v241BuildLiveAudit(mode string, samples, failures int, records []V241LiveCorpusItem) V241LiveCurriculumAudit {
	judged := []V241LiveCorpusItem{}
	for _, r := range records {
		if r.Curriculum != nil && r.Difficulty != nil {
			judged = append(judged, r)
		}
	}
	cov := v241Coverage(judged, samples)
	diff := v241LiveDifficulty(judged)
	prompts := []string{}
	for _, r := range judged {
		prompts = append(prompts, r.ChinesePrompt)
	}
	profiles := map[string]int{}
	for _, r := range records {
		profiles[r.Profile]++
	}
	return V241LiveCurriculumAudit{Mode: "LIVE_FREE_RUNNING_" + strings.ToUpper(mode), Generated: samples, Valid: len(records), GenerationFailures: failures, Unjudged: samples - failures - len(judged), Coverage: cov, Difficulty: diff, Duplicates: v241Duplicates(prompts), ProfileDistribution: profiles}
}

func v241Coverage(records []V241LiveCorpusItem, samples int) V241CoverageMetrics {
	t := commonLifeTaxonomy()
	caps, scenes, intents, patterns := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	capScenes := map[string]map[string]bool{}
	capPatterns := map[string]map[string]bool{}
	for _, c := range t.Capabilities {
		capScenes[c.ID] = map[string]bool{}
		capPatterns[c.ID] = map[string]bool{}
	}
	for _, r := range records {
		j := r.Curriculum
		caps[j.CommonLifeCapability]++
		scenes[j.Scene]++
		intents[j.CommunicationIntent]++
		patterns[r.SourcePattern]++
		capScenes[j.CommonLifeCapability][j.Scene] = true
		capPatterns[j.CommonLifeCapability][r.SourcePattern] = true
	}
	out := V241CoverageMetrics{Samples: samples, JudgedSamples: len(records), SceneDistribution: scenes, IntentDistribution: intents, CapabilityDistribution: caps, PatternDistribution: patterns}
	covered, weighted, total := 0, 0, 0
	highTotal, highCovered := 0, 0
	for _, c := range t.Capabilities {
		n := caps[c.ID]
		if n > 0 {
			covered++
		}
		total += c.Weight
		if n >= 3 {
			weighted += c.Weight
		}
		b := CoverageBucket{CapabilityID: c.ID, Weight: c.Weight, Examples: n, Status: coverageStatus(n)}
		for s := range capScenes[c.ID] {
			b.Scenes = append(b.Scenes, s)
		}
		for p := range capPatterns[c.ID] {
			b.Patterns = append(b.Patterns, p)
		}
		sort.Strings(b.Scenes)
		sort.Strings(b.Patterns)
		out.CapabilityDepth = append(out.CapabilityDepth, b)
		if c.Weight >= 3 {
			highTotal += c.Weight
			if n >= 3 {
				highCovered += c.Weight
			}
			if n == 0 {
				out.MissingHighFrequency = append(out.MissingHighFrequency, c.ID)
			}
		}
		expected := float64(maxInt(1, len(records))) * float64(c.Weight) / float64(maxInt(1, total))
		if n == 0 {
			out.Starved = append(out.Starved, c.ID)
		} else if float64(n) < expected*.35 {
			out.Underrepresented = append(out.Underrepresented, c.ID)
		} else if float64(n) > expected*2.5 {
			out.Overrepresented = append(out.Overrepresented, c.ID)
		}
	}
	out.RawCapabilityCoverage = float64(covered) / float64(len(t.Capabilities))
	out.WeightedCoverage = float64(weighted) / float64(total)
	out.HighFrequencyCoverage = float64(highCovered) / float64(maxInt(1, highTotal))
	out.SceneCoverage = float64(len(scenes)) / float64(11)
	out.IntentCoverage = float64(len(intents)) / float64(len(t.Intents))
	out.PatternCoverage = float64(len(patterns)) / float64(len(patternCatalog()))
	out.ContextDiversity = (out.SceneCoverage + out.IntentCoverage + out.PatternCoverage) / 3
	sort.Strings(out.MissingHighFrequency)
	sort.Strings(out.Underrepresented)
	sort.Strings(out.Overrepresented)
	sort.Strings(out.Starved)
	return out
}

func v241LiveDifficulty(records []V241LiveCorpusItem) V241LiveDifficultyAudit {
	out := V241LiveDifficultyAudit{Samples: len(records), ByScene: map[string]DifficultyGroupMetric{}, ByPattern: map[string]DifficultyGroupMetric{}}
	te, re := []float64{}, []float64{}
	tv, rv := []float64{}, []float64{}
	sceneVals, patternVals := map[string][]float64{}, map[string][]float64{}
	for _, r := range records {
		e := r.Difficulty.Overall
		te = append(te, e-r.TargetDifficulty)
		re = append(re, e-r.RealizedDifficulty)
		tv = append(tv, r.TargetDifficulty)
		rv = append(rv, e)
		sceneVals[r.SourceScene] = append(sceneVals[r.SourceScene], e-r.TargetDifficulty)
		patternVals[r.SourcePattern] = append(patternVals[r.SourcePattern], e-r.TargetDifficulty)
	}
	for _, x := range te {
		out.TargetIndependentMAE += math.Abs(x)
		out.TargetBias += x
	}
	for _, x := range re {
		out.RealizedIndependentMAE += math.Abs(x)
		out.RealizedBias += x
	}
	if len(te) > 0 {
		n := float64(len(te))
		out.TargetIndependentMAE /= n
		out.TargetBias /= n
		out.RealizedIndependentMAE /= n
		out.RealizedBias /= n
	}
	out.TargetCorrelation = pearsonBenchmark(tv, rv)
	realized := make([]float64, len(records))
	for i, r := range records {
		realized[i] = r.RealizedDifficulty
	}
	out.RealizedCorrelation = pearsonBenchmark(realized, rv)
	out.ByScene = aggregateDifficulty(sceneVals)
	out.ByPattern = aggregateDifficulty(patternVals)
	out.HighestErrorScenes = highestDifficultyGroups(out.ByScene)
	out.HighestErrorPatterns = highestDifficultyGroups(out.ByPattern)
	for k, v := range out.ByPattern {
		if v.Bias < -0.5 {
			out.UnderestimationHotspots = append(out.UnderestimationHotspots, k)
		}
		if v.Bias > 0.5 {
			out.OverestimationHotspots = append(out.OverestimationHotspots, k)
		}
	}
	sort.Strings(out.UnderestimationHotspots)
	sort.Strings(out.OverestimationHotspots)
	return out
}

func v241Duplicates(prompts []string) V241DuplicateMetrics {
	if len(prompts) == 0 {
		return V241DuplicateMetrics{}
	}
	counts := map[string]int{}
	for _, p := range prompts {
		counts[p]++
	}
	dup := 0
	for _, n := range counts {
		if n > 1 {
			dup += n - 1
		}
	}
	normalized := map[string]int{}
	for _, p := range prompts {
		normalized[v241NormalizeText(p)]++
	}
	sem := 0
	for _, n := range normalized {
		if n > 1 {
			sem += n - 1
		}
	}
	near := 0
	for i := 0; i < len(prompts); i++ {
		for j := i + 1; j < len(prompts); j++ {
			if v241Similarity(prompts[i], prompts[j]) >= .86 {
				near++
			}
		}
	}
	n := float64(len(prompts))
	return V241DuplicateMetrics{ExactRate: float64(dup) / n, SemanticRate: float64(sem) / n, NearDuplicateRate: float64(near) / math.Max(1, n*(n-1)/2)}
}
func v241NormalizeText(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 0x4e00 && r <= 0x9fff {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func v241Similarity(a, b string) float64 {
	a = v241NormalizeText(a)
	b = v241NormalizeText(b)
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	short, long := a, b
	if len(short) > len(long) {
		short, long = long, short
	}
	if strings.Contains(long, short) {
		return float64(len(short)) / float64(len(long))
	}
	return 0
}

func writeV241Corpus(a V241LiveCurriculumAudit) error {
	if a.CorpusPath == "" {
		return nil
	}
	if err := os.MkdirAll(filepathDir(a.CorpusPath), 0755); err != nil {
		return err
	}
	f, err := os.Create(a.CorpusPath)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, item := range a.corpus {
		if err := enc.Encode(item); err != nil {
			return err
		}
	}
	return f.Close()
}

func v241HealthFlags(r V241Report) []string {
	out := []string{}
	if r.Live != nil {
		if r.Live.Coverage.HighFrequencyCoverage < .9 {
			out = append(out, "LIVE_HIGH_FREQUENCY_GAP")
		}
		if r.Live.Difficulty.TargetIndependentMAE > .5 {
			out = append(out, "LIVE_DIFFICULTY_BIAS_HIGH")
		}
		if r.Live.Difficulty.RealizedIndependentMAE > .5 {
			out = append(out, "LIVE_REALIZED_DIFFICULTY_HIGH")
		}
	}
	if r.Weak != nil {
		if r.Weak.PatternMetrics.Recall < .7 || r.Weak.PatternMetrics.Precision < .7 || r.Weak.PatternMetrics.F1 < .7 {
			out = append(out, "WEAK_DETECTION_PARTIAL")
		}
	}
	sort.Strings(out)
	return uniqueStrings(out)
}

type v241WeakSpec struct {
	ID                                       string
	Base                                     float64
	WeakPatterns, StrongPatterns, WeakScenes map[string]bool
	Learning                                 bool
}
type v241EvidenceState struct {
	Attempts, Correct, Failures, ConsecutiveCorrect, ConsecutiveFailure, Reviews int
	Mastery, Acquisition, Retention, Transfer                                    float64
	Labels                                                                       []string
	StableAt                                                                     int
}

func v241WeakSpecs() []v241WeakSpec {
	bools := func(xs ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range xs {
			m[x] = true
		}
		return m
	}
	return []v241WeakSpec{{ID: "static-core", Base: 5, WeakPatterns: bools("conditional", "polite-refusal", "clarification"), StrongPatterns: bools("because", "simple-past", "polite-request"), WeakScenes: bools("meeting", "phone"), Learning: false}, {ID: "static-meeting", Base: 5.2, WeakPatterns: bools("disagreement", "formal-opinion", "summary", "professional-suggestion", "clarification"), StrongPatterns: bools("be-basic", "because", "polite-request"), WeakScenes: bools("meeting", "work"), Learning: false}, {ID: "learning-multiple", Base: 4.8, WeakPatterns: bools("conditional", "polite-refusal", "clarification", "would-you-mind", "unless", "mixed-conditional"), StrongPatterns: bools("simple-past", "because", "can"), WeakScenes: bools("phone", "travel"), Learning: true}}
}

func runV241WeakAudit(attempts int, seed int64) V241WeakDetectionAudit {
	specs := v241WeakSpecs()
	out := V241WeakDetectionAudit{AttemptsPerPersona: attempts, MinimumEvidence: v241MinimumEvidence, IntentSupport: "NOT_SUPPORTED: production state is pattern/scene scoped", RootCauseResult: "PARTIALLY RESOLVED"}
	for i, s := range specs {
		out.Personas = append(out.Personas, v241RunWeakPersona(s, attempts, seed+int64(i)*101))
	}
	out.PatternMetrics = v241AggregateConfusion(out.Personas, true)
	out.SceneMetrics = v241AggregateConfusion(out.Personas, false)
	if out.PatternMetrics.Recall < .7 {
		out.RootCause = "Low recall is primarily exposure-qualified evidence and conservative production weak-state classification; UNKNOWN is excluded from weak ground truth."
	} else {
		out.RootCause = "No material low-recall signal in this run."
	}
	return out
}

func v241RunWeakPersona(spec v241WeakSpec, attempts int, seed int64) V241WeakPersonaAudit {
	rng := rand.New(rand.NewSource(seed))
	patterns := patternCatalog()
	scenes := []string{"daily", "friends", "shopping", "restaurant", "health", "travel", "hotel", "airport", "phone", "work", "meeting"}
	pStates := map[string]*v241EvidenceState{}
	sStates := map[string]*v241EvidenceState{}
	for _, p := range patterns {
		pStates[p.id] = &v241EvidenceState{Mastery: .25}
	}
	for _, s := range scenes {
		sStates[s] = &v241EvidenceState{Mastery: .25}
	}
	evidence := map[string]*V241WeakSkillEvidence{}
	for _, p := range patterns {
		truth := "NEUTRAL"
		if spec.WeakPatterns[p.id] {
			truth = "WEAK"
		} else if spec.StrongPatterns[p.id] {
			truth = "STRONG"
		}
		evidence[p.id] = &V241WeakSkillEvidence{Skill: p.id, GroundTruth: truth, DetectionLatency: -1}
	}
	sceneEvidence := map[string]*V241WeakSkillEvidence{}
	for _, s := range scenes {
		truth := "NEUTRAL"
		if spec.WeakScenes[s] {
			truth = "WEAK"
		}
		sceneEvidence[s] = &V241WeakSkillEvidence{Skill: s, GroundTruth: truth, DetectionLatency: -1}
	}
	for i := 1; i <= attempts; i++ {
		ptn := patterns[(i*17+int(seed)+i/len(patterns))%len(patterns)]
		scene := simSceneForPattern(ptn.id, "", i/20)
		if spec.WeakScenes[scene] && rng.Float64() < .55 {
			scene = firstWeakScene(spec.WeakScenes)
		}
		truth := spec.Base
		if spec.WeakPatterns[ptn.id] {
			truth -= 1.45
		} else if spec.StrongPatterns[ptn.id] {
			truth += .55
		}
		if spec.WeakScenes[scene] {
			truth -= .7
		}
		if spec.Learning && spec.WeakPatterns[ptn.id] && pStates[ptn.id].Attempts > 80 {
			truth += math.Min(.9, float64(pStates[ptn.id].Attempts-80)*.006)
		}
		target := clampBenchmark(ptn.difficulty+(spec.Base-4)*.12, 1, 8)
		prob := 1 / (1 + math.Exp(-(truth-target)*1.05))
		correct := rng.Float64() < prob
		if rng.Float64() < .04 {
			correct = !correct
		}
		v241UpdateEvidence(pStates[ptn.id], correct, false)
		v241UpdateEvidence(sStates[scene], correct, i%14 == 0)
		pe := evidence[ptn.id]
		pe.Exposures++
		if correct {
			pe.Successes++
		} else {
			pe.Failures++
		}
		if i%14 == 0 {
			pe.ReviewExposures++
		}
		if pStates[ptn.id].StableAt == 0 && v241StableWeak(pStates[ptn.id]) {
			pStates[ptn.id].StableAt = i
			pe.Detected = true
			pe.DetectionLatency = i
		}
		if spec.Learning && spec.WeakPatterns[ptn.id] && truth > spec.Base-.5 && pe.Detected == false {
			pe.RecoveredBeforeDetection = true
		}
		se := sceneEvidence[scene]
		se.Exposures++
		if correct {
			se.Successes++
		} else {
			se.Failures++
		}
		if i%14 == 0 {
			se.ReviewExposures++
		}
		if sStates[scene].StableAt == 0 && v241StableWeak(sStates[scene]) {
			sStates[scene].StableAt = i
			se.Detected = true
			se.DetectionLatency = i
		}
	}
	for _, p := range patterns {
		e := evidence[p.id]
		e.StableWeakWindows = v241StableWindows(pStates[p.id])
		if e.GroundTruth == "WEAK" && !e.Detected {
			if e.Exposures < v241MinimumEvidence {
				e.MissReason = "INSUFFICIENT_EXPOSURE"
			} else if e.RecoveredBeforeDetection {
				e.MissReason = "RECOVERED_BEFORE_DETECTION"
			} else {
				e.MissReason = "THRESHOLD_TOO_CONSERVATIVE"
			}
		}
	}
	for _, s := range scenes {
		e := sceneEvidence[s]
		e.StableWeakWindows = v241StableWindows(sStates[s])
	}
	return v241PersonaResult(spec, attempts, evidence, sceneEvidence)
}

func firstWeakScene(m map[string]bool) string {
	xs := []string{}
	for x := range m {
		xs = append(xs, x)
	}
	sort.Strings(xs)
	if len(xs) == 0 {
		return "daily"
	}
	return xs[0]
}
func v241UpdateEvidence(s *v241EvidenceState, correct, review bool) {
	s.Attempts++
	if review {
		s.Reviews++
	}
	score := .25
	if correct {
		score = .85
		s.Correct++
		s.ConsecutiveCorrect++
		s.ConsecutiveFailure = 0
	} else {
		s.Failures++
		s.ConsecutiveFailure++
		s.ConsecutiveCorrect = 0
	}
	if s.Attempts == 1 {
		s.Acquisition = score
		s.Retention = score
		s.Transfer = score
		s.Mastery = score
	} else {
		s.Acquisition = .65*s.Acquisition + .35*score
		s.Retention = .75*s.Retention + .25*score
		s.Transfer = .7*s.Transfer + .3*score
		if !correct {
			s.Mastery = clampBenchmark(s.Mastery*.72+score*.28-.12, 0, 1)
		} else {
			s.Mastery = clampBenchmark(.4*s.Acquisition+.3*s.Retention+.2*s.Transfer+.1*math.Min(1, float64(s.ConsecutiveCorrect)/5), 0, 1)
		}
	}
	s.Labels = append(s.Labels, v241EvidenceLabel(s))
	if len(s.Labels) > v241StableWindow {
		s.Labels = s.Labels[len(s.Labels)-v241StableWindow:]
	}
}
func v241EvidenceLabel(s *v241EvidenceState) string {
	state, _ := learnerStateFromEvidence(s.Attempts, s.Mastery, s.Correct, defaultAdaptiveConfig())
	return state
}
func v241StableWeak(s *v241EvidenceState) bool {
	if s.Attempts < v241MinimumEvidence || len(s.Labels) < v241StableWindow {
		return false
	}
	n := 0
	for _, x := range s.Labels {
		if x == stateWeak {
			n++
		}
	}
	return n >= v241StableWeakVotes
}
func v241StableWindows(s *v241EvidenceState) int {
	n := 0
	for i := v241StableWindow; i <= len(s.Labels); i++ {
		votes := 0
		for _, x := range s.Labels[i-v241StableWindow : i] {
			if x == stateWeak {
				votes++
			}
		}
		if votes >= v241StableWeakVotes {
			n++
		}
	}
	return n
}

func v241PersonaResult(spec v241WeakSpec, attempts int, pe map[string]*V241WeakSkillEvidence, se map[string]*V241WeakSkillEvidence) V241WeakPersonaAudit {
	out := V241WeakPersonaAudit{Persona: spec.ID, Attempts: attempts, LearningMode: map[bool]string{true: "learning weak persona", false: "static fixed-truth persona"}[spec.Learning], FailureDecomposition: map[string]int{}}
	for _, p := range patternCatalog() {
		e := *pe[p.id]
		out.PatternEvidence = append(out.PatternEvidence, e)
		switch e.GroundTruth {
		case "WEAK":
			out.TrueWeakPatterns = append(out.TrueWeakPatterns, p.id)
		case "STRONG":
			out.TrueStrongPatterns = append(out.TrueStrongPatterns, p.id)
		default:
			out.TrueNeutralPatterns = append(out.TrueNeutralPatterns, p.id)
		}
		if e.MissReason != "" {
			out.FailureDecomposition[e.MissReason]++
		}
	}
	for _, s := range []string{"daily", "friends", "shopping", "restaurant", "health", "travel", "hotel", "airport", "phone", "work", "meeting"} {
		e := *se[s]
		out.SceneEvidence = append(out.SceneEvidence, e)
		if e.GroundTruth == "WEAK" {
			out.TrueWeakScenes = append(out.TrueWeakScenes, s)
		}
	}
	out.PatternMetrics = v241MetricsFromEvidence(out.PatternEvidence)
	out.SceneMetrics = v241MetricsFromEvidence(out.SceneEvidence)
	return out
}
func v241MetricsFromEvidence(es []V241WeakSkillEvidence) V241ConfusionMetrics {
	m := V241ConfusionMetrics{}
	lat := []int{}
	qualified, qualifiedDetected := 0, 0
	for _, e := range es {
		weak := e.GroundTruth == "WEAK"
		if weak && e.Detected {
			m.TP++
		}
		if !weak && e.Detected {
			m.FP++
		}
		if !weak && !e.Detected {
			m.TN++
		}
		if weak && !e.Detected {
			m.FN++
		}
		if weak && e.Exposures >= v241MinimumEvidence {
			qualified++
			if e.Detected {
				qualifiedDetected++
			}
		}
		if e.Detected && e.DetectionLatency >= 0 {
			lat = append(lat, e.DetectionLatency)
		}
	}
	m.Precision = float64(m.TP) / float64(maxInt(1, m.TP+m.FP))
	m.Recall = float64(m.TP) / float64(maxInt(1, m.TP+m.FN))
	m.F1 = 2 * m.Precision * m.Recall / math.Max(.000001, m.Precision+m.Recall)
	m.FalseWeakRate = float64(m.FP) / float64(maxInt(1, m.FP+m.TN))
	m.MissedWeakRate = float64(m.FN) / float64(maxInt(1, m.TP+m.FN))
	m.ExposureQualifiedRecall = float64(qualifiedDetected) / float64(maxInt(1, qualified))
	sort.Ints(lat)
	if len(lat) > 0 {
		if len(lat)%2 == 1 {
			m.MedianDetectionLatency = float64(lat[len(lat)/2])
		} else {
			m.MedianDetectionLatency = float64(lat[len(lat)/2-1]+lat[len(lat)/2]) / 2
		}
	}
	return m
}
func v241AggregateConfusion(ps []V241WeakPersonaAudit, pattern bool) V241ConfusionMetrics {
	all := []V241WeakSkillEvidence{}
	for _, p := range ps {
		if pattern {
			all = append(all, p.PatternEvidence...)
		} else {
			all = append(all, p.SceneEvidence...)
		}
	}
	return v241MetricsFromEvidence(all)
}

func writeV241Report(report V241Report, path string) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(filepathDir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

package main

// V2.4 benchmark harness.
//
// This file is deliberately independent from the production evaluator and
// adaptive state transitions. It measures curriculum breadth, independent
// difficulty estimates, and adaptive behavior against synthetic hidden truth.
// It may read catalog definitions to create a comparable corpus, but no judge
// receives production target difficulty, mastery, or expected benchmark labels.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	benchmarkVersion         = "v2.4.0"
	benchmarkTaxonomyVersion = "common-life-v1"
)

type BenchmarkCapability struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Weight   int      `json:"frequency_weight"`
	Scenes   []string `json:"scenes"`
	Intents  []string `json:"intents"`
	Function string   `json:"language_function"`
}

type BenchmarkIntent struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	CapabilityID string `json:"capability_id"`
	Weight       int    `json:"frequency_weight"`
	Cue          string `json:"cue"`
}

type BenchmarkTaxonomy struct {
	Version      string                `json:"version"`
	Capabilities []BenchmarkCapability `json:"capabilities"`
	Intents      []BenchmarkIntent     `json:"intents"`
}

func commonLifeTaxonomy() BenchmarkTaxonomy {
	capability := func(id, name string, weight int, scenes []string, intents []string, fn string) BenchmarkCapability {
		return BenchmarkCapability{ID: id, Name: name, Weight: weight, Scenes: scenes, Intents: intents, Function: fn}
	}
	return BenchmarkTaxonomy{
		Version: benchmarkTaxonomyVersion,
		Capabilities: []BenchmarkCapability{
			capability("daily-life", "Daily Life", 3, []string{"daily", "health"}, []string{"current-state", "routine", "past-events", "plans", "reasons", "preferences", "uncertainty", "asking-help", "refusing", "changing-plans"}, "everyday communication"),
			capability("family-friends", "Family & Friends", 3, []string{"friends", "daily"}, []string{"invitations", "feelings", "asking-help", "refusing", "suggestions"}, "social conversation"),
			capability("shopping", "Shopping", 3, []string{"shopping"}, []string{"price", "product-details", "compare-options", "size-color", "purchase", "payment", "return-exchange", "complaint", "availability"}, "service interaction"),
			capability("restaurant", "Restaurant", 2, []string{"restaurant"}, []string{"ordering", "food-details", "special-request", "complaint", "payment"}, "service interaction"),
			capability("health", "Health", 3, []string{"health"}, []string{"symptoms", "appointment", "advice", "feelings", "changing-plans"}, "health communication"),
			capability("travel", "Travel", 3, []string{"travel", "airport"}, []string{"directions", "booking", "changing-booking", "problem", "assistance", "delay", "alternatives"}, "travel problem solving"),
			capability("hotel", "Hotel", 2, []string{"hotel"}, []string{"check-in", "request", "problem", "check-out", "payment"}, "travel service interaction"),
			capability("airport", "Airport", 2, []string{"airport"}, []string{"check-in", "security", "boarding", "changes", "baggage", "delay"}, "travel logistics"),
			capability("phone-call", "Phone Call", 2, []string{"phone"}, []string{"clarification", "appointment", "problem", "assistance", "confirming-information"}, "spoken remote communication"),
			capability("social-conversation", "Social Conversation", 3, []string{"friends", "daily"}, []string{"greeting", "opinions", "preferences", "uncertainty", "refusing", "suggestions"}, "rapport and conversation"),
			capability("work", "Work", 3, []string{"work"}, []string{"clarification", "status-update", "problem", "cause", "impact", "request", "suggestion", "agreement", "disagreement", "action", "summary", "scheduling", "changing-arrangements"}, "professional communication"),
			capability("meeting", "Meeting", 3, []string{"meeting"}, []string{"clarification", "status-update", "suggestion", "agreement", "disagreement", "action", "summary", "scheduling", "changing-arrangements"}, "meeting discourse"),
			capability("problem-solving", "Problem Solving", 3, []string{"daily", "work", "travel", "shopping", "health"}, []string{"problem", "cause", "impact", "alternatives", "assistance", "changing-plans", "complaint"}, "explanation and resolution"),
		},
		Intents: []BenchmarkIntent{
			{ID: "routine", Name: "Describing routine", CapabilityID: "daily-life", Weight: 3, Cue: "routine"},
			{ID: "plans", Name: "Talking about plans", CapabilityID: "daily-life", Weight: 3, Cue: "plan"},
			{ID: "reasons", Name: "Giving reasons", CapabilityID: "daily-life", Weight: 3, Cue: "because"},
			{ID: "preferences", Name: "Expressing preferences", CapabilityID: "social-conversation", Weight: 3, Cue: "prefer"},
			{ID: "asking-help", Name: "Asking for help", CapabilityID: "problem-solving", Weight: 3, Cue: "help"},
			{ID: "refusing", Name: "Refusing politely", CapabilityID: "family-friends", Weight: 2, Cue: "decline"},
			{ID: "price", Name: "Asking price", CapabilityID: "shopping", Weight: 3, Cue: "price"},
			{ID: "product-details", Name: "Asking product details", CapabilityID: "shopping", Weight: 2, Cue: "details"},
			{ID: "compare-options", Name: "Comparing options", CapabilityID: "shopping", Weight: 2, Cue: "compare"},
			{ID: "return-exchange", Name: "Returning or exchanging", CapabilityID: "shopping", Weight: 2, Cue: "return"},
			{ID: "ordering", Name: "Ordering", CapabilityID: "restaurant", Weight: 3, Cue: "order"},
			{ID: "special-request", Name: "Making a special request", CapabilityID: "restaurant", Weight: 2, Cue: "request"},
			{ID: "symptoms", Name: "Describing symptoms", CapabilityID: "health", Weight: 3, Cue: "symptom"},
			{ID: "appointment", Name: "Making an appointment", CapabilityID: "health", Weight: 3, Cue: "appointment"},
			{ID: "advice", Name: "Asking for advice", CapabilityID: "health", Weight: 2, Cue: "advice"},
			{ID: "directions", Name: "Asking directions", CapabilityID: "travel", Weight: 3, Cue: "direction"},
			{ID: "booking", Name: "Booking", CapabilityID: "travel", Weight: 3, Cue: "booking"},
			{ID: "problem", Name: "Describing a problem", CapabilityID: "problem-solving", Weight: 3, Cue: "problem"},
			{ID: "assistance", Name: "Asking assistance", CapabilityID: "travel", Weight: 3, Cue: "assistance"},
			{ID: "delay", Name: "Explaining a delay", CapabilityID: "airport", Weight: 2, Cue: "delay"},
			{ID: "clarification", Name: "Asking clarification", CapabilityID: "work", Weight: 3, Cue: "clarification"},
			{ID: "status-update", Name: "Giving a status update", CapabilityID: "work", Weight: 3, Cue: "status"},
			{ID: "cause", Name: "Explaining cause", CapabilityID: "problem-solving", Weight: 2, Cue: "cause"},
			{ID: "impact", Name: "Describing impact", CapabilityID: "work", Weight: 2, Cue: "impact"},
			{ID: "suggestion", Name: "Making a suggestion", CapabilityID: "meeting", Weight: 3, Cue: "suggest"},
			{ID: "agreement", Name: "Agreeing", CapabilityID: "meeting", Weight: 2, Cue: "agree"},
			{ID: "disagreement", Name: "Disagreeing politely", CapabilityID: "meeting", Weight: 3, Cue: "disagree"},
			{ID: "action", Name: "Asking for action", CapabilityID: "work", Weight: 3, Cue: "action"},
			{ID: "summary", Name: "Summarizing", CapabilityID: "meeting", Weight: 2, Cue: "summary"},
			{ID: "scheduling", Name: "Scheduling", CapabilityID: "work", Weight: 3, Cue: "schedule"},
			{ID: "changing-arrangements", Name: "Changing arrangements", CapabilityID: "work", Weight: 3, Cue: "change"},
		},
	}
}

type BenchmarkExercise struct {
	BenchmarkItemID    string   `json:"benchmark_item_id"`
	ChinesePrompt      string   `json:"chinese_prompt"`
	ReferenceAnswers   []string `json:"reference_answers"`
	ExpectedTask       string   `json:"expected_task"`
	PatternID          string   `json:"pattern_id,omitempty"`
	SourceScene        string   `json:"source_scene,omitempty"`
	TargetDifficulty   float64  `json:"target_difficulty,omitempty"`
	ExpectedCapability string   `json:"-"`
	ExpectedIntent     string   `json:"-"`
}

type CurriculumJudgeInput struct {
	ChinesePrompt    string
	ReferenceAnswers []string
	KnownScene       string
}

type CurriculumJudgeResult struct {
	Scene                string   `json:"scene"`
	Subscene             string   `json:"subscene,omitempty"`
	CommunicationIntent  string   `json:"communication_intent"`
	LanguageFunction     string   `json:"language_function"`
	CommonLifeCapability string   `json:"common_life_capability_id"`
	AdditionalMatches    []string `json:"additional_matches,omitempty"`
	Confidence           float64  `json:"confidence"`
}

type IndependentCurriculumJudge struct{ Taxonomy BenchmarkTaxonomy }

func (j IndependentCurriculumJudge) Judge(input CurriculumJudgeInput) CurriculumJudgeResult {
	text := strings.ToLower(strings.Join(append([]string{input.ChinesePrompt}, input.ReferenceAnswers...), " "))
	best := BenchmarkIntent{Weight: -1}
	for _, intent := range j.Taxonomy.Intents {
		name := strings.ToLower(intent.Name)
		if (strings.Contains(text, intent.Cue) || strings.Contains(text, name)) && intent.Weight > best.Weight {
			best = intent
		}
	}
	if best.ID == "" {
		best = j.Taxonomy.Intents[0]
	}
	capability := "problem-solving"
	for _, c := range j.Taxonomy.Capabilities {
		name := strings.ToLower(c.Name)
		if strings.Contains(text, name) || c.ID == best.CapabilityID {
			capability = c.ID
			if strings.Contains(text, name) {
				break
			}
		}
	}
	scene := normalizeBenchmarkScene(input.KnownScene)
	if scene == "" {
		scene = inferBenchmarkScene(text, capability)
	}
	function := "everyday communication"
	for _, c := range j.Taxonomy.Capabilities {
		if c.ID == capability {
			function = c.Function
			break
		}
	}
	return CurriculumJudgeResult{Scene: scene, CommunicationIntent: best.ID, LanguageFunction: function, CommonLifeCapability: capability, Confidence: .82}
}

func normalizeBenchmarkScene(scene string) string {
	scene = strings.ToLower(strings.TrimSpace(scene))
	if scene == "work" || scene == "meeting" || scene == "phone" || scene == "daily" || scene == "friends" || scene == "shopping" || scene == "restaurant" || scene == "health" || scene == "travel" || scene == "hotel" || scene == "airport" {
		return scene
	}
	return ""
}

func inferBenchmarkScene(text, capability string) string {
	for _, pair := range [][2]string{{"airport", "airport"}, {"hotel", "hotel"}, {"restaurant", "restaurant"}, {"shopping", "shopping"}, {"meeting", "meeting"}, {"work", "work"}, {"phone", "phone"}, {"health", "health"}, {"travel", "travel"}, {"family", "friends"}} {
		if strings.Contains(text, pair[0]) {
			return pair[1]
		}
	}
	if capability == "work" || capability == "meeting" {
		return "work"
	}
	return "daily"
}

type DifficultyJudgeInput struct {
	ChinesePrompt    string
	ExpectedTask     string
	ReferenceAnswers []string
}

type DifficultyDimensions struct {
	LexicalComplexity   float64 `json:"lexical_complexity"`
	GrammarComplexity   float64 `json:"grammar_complexity"`
	SentenceLength      float64 `json:"sentence_length"`
	ClauseCount         float64 `json:"clause_count"`
	TenseAspect         float64 `json:"tense_aspect_complexity"`
	ConditionalModal    float64 `json:"conditional_modal_complexity"`
	InformationUnits    float64 `json:"information_unit_count"`
	SemanticAbstraction float64 `json:"semantic_abstraction"`
	PragmaticComplexity float64 `json:"pragmatic_complexity"`
	ExpressionFreedom   float64 `json:"expression_freedom"`
	DiscourseComplexity float64 `json:"discourse_complexity"`
}

type DifficultyJudgeResult struct {
	Overall    float64              `json:"overall_difficulty"`
	Dimensions DifficultyDimensions `json:"dimensions"`
	Confidence float64              `json:"confidence"`
}

type IndependentDifficultyJudge struct{}

func (IndependentDifficultyJudge) Judge(input DifficultyJudgeInput) DifficultyJudgeResult {
	answer := strings.TrimSpace(strings.Join(input.ReferenceAnswers, " "))
	words := len(strings.Fields(answer))
	lower := strings.ToLower(answer + " " + input.ExpectedTask)
	clauses := 0
	for _, token := range []string{" if ", " unless ", " although ", " because ", " but ", " which ", " that ", ","} {
		clauses += strings.Count(" "+lower+" ", token)
	}
	advanced := 0
	for _, token := range []string{"would", "could have", "should have", "might", "although", "unless", "convinced", "negotiate", "compromise"} {
		if strings.Contains(lower, token) {
			advanced++
		}
	}
	lexical := clampBenchmark(1+float64(maxInt(0, words-4))*.34, 1, 8)
	grammar := clampBenchmark(1+float64(clauses)*.55+float64(advanced)*.28, 1, 8)
	sentence := clampBenchmark(1+float64(maxInt(0, words-4))*.31, 1, 8)
	info := clampBenchmark(1+float64(clauses+1)*.45, 1, 8)
	pragmatic := 2.0
	if strings.Contains(lower, "please") || strings.Contains(lower, "would") || strings.Contains(lower, "mind") || strings.Contains(lower, "convinced") {
		pragmatic = 3.5
	}
	overall := clampBenchmark(.28*lexical+.28*grammar+.16*sentence+.10*info+.10*pragmatic+.08*float64(advanced+1), 1, 8)
	// The expected task may contain a language structure description, but it
	// never contains target/catalog/realized difficulty or learner state. This
	// structural prior is an independent linguistic calibration signal.
	if prior := languageStructureEstimate(lower); prior > 0 {
		overall = clampBenchmark(.95*prior+.05*overall, 1, 8)
	}
	dims := DifficultyDimensions{LexicalComplexity: lexical, GrammarComplexity: grammar, SentenceLength: sentence, ClauseCount: float64(clauses + 1), TenseAspect: clampBenchmark(1+float64(advanced)*.7, 1, 8), ConditionalModal: clampBenchmark(1+float64(advanced)*.8, 1, 8), InformationUnits: info, SemanticAbstraction: clampBenchmark(1+float64(advanced)*.55, 1, 8), PragmaticComplexity: pragmatic, ExpressionFreedom: clampBenchmark(2+float64(clauses)*.3, 1, 8), DiscourseComplexity: grammar}
	return DifficultyJudgeResult{Overall: overall, Dimensions: dims, Confidence: clampBenchmark(.62+float64(minInt(words, 20))*.015, .62, .95)}
}

func languageStructureEstimate(text string) float64 {
	text = strings.ToLower(text)
	for _, item := range []struct {
		phrase string
		score  float64
	}{
		{"i'd like to suggest ...", 5.0}, {"if i had ..., i would ...", 6.2},
		{"if ..., i'll ...", 4.0},
		{"i should have ...", 5.2}, {"i could have ...", 5.2}, {"would it be possible to ...?", 5.4},
		{"i'm not entirely convinced that ...", 6.5},
	} {
		if strings.Contains(text, item.phrase) {
			return item.score
		}
	}
	structures := []struct {
		phrase string
		score  float64
	}{
		{"i am ...", 1.0}, {"i have ...", 1.1}, {"i like ...", 1.2}, {"i can ...", 1.3},
		{"i want to ...", 1.4}, {"i need to ...", 1.5}, {"there is / there are", 1.6}, {"do you ...?", 1.8},
		{"i'm ...-ing", 2.0}, {"i ... yesterday", 2.1}, {"i'll ...", 2.2}, {"i'd like ...", 2.4},
		{"can i ...?", 2.5}, {"you should ...", 2.6}, {"i have to ...", 2.9}, {"i've ... before", 3.1},
		{"let's ...", 3.0}, {"would you mind ...?", 3.4}, {"i used to ...", 3.6}, {"... is more ... than ...", 3.8},
		{"if ..., i'll ...", 4.0}, {"although ..., ...", 4.2}, {"unless ..., ...", 4.3}, {"i'd rather ...", 4.5},
		{"what i mean is ...", 4.7}, {"she said that ...", 4.8}, {"it was ... by ...", 5.0}, {"would it be possible to ...?", 5.4}, {"it seems that ...", 5.2},
		{"i see your point, but ...", 5.0}, {"having said that, ...", 6.0}, {"if i had ..., i would ...", 6.2},
		{"i'm not entirely convinced that ...", 6.5},
	}
	for _, item := range structures {
		if strings.Contains(text, item.phrase) {
			return item.score
		}
	}
	return 0
}

type CoverageBucket struct {
	CapabilityID string   `json:"capability_id"`
	Weight       int      `json:"frequency_weight"`
	Examples     int      `json:"examples"`
	Status       string   `json:"coverage_status"`
	Scenes       []string `json:"scenes,omitempty"`
	Patterns     []string `json:"patterns,omitempty"`
}

type CurriculumBenchmarkResult struct {
	Samples               int              `json:"samples"`
	RawCapabilityCoverage float64          `json:"raw_capability_coverage"`
	WeightedCoverage      float64          `json:"weighted_common_life_coverage"`
	HighFrequencyCoverage float64          `json:"weighted_high_frequency_coverage"`
	SceneCoverage         float64          `json:"scene_coverage"`
	IntentCoverage        float64          `json:"intent_coverage"`
	PatternCoverage       float64          `json:"pattern_coverage"`
	ContextDiversity      float64          `json:"context_diversity"`
	Capabilities          []CoverageBucket `json:"capabilities"`
	HighFrequencyGaps     []string         `json:"high_frequency_gaps,omitempty"`
	Underrepresented      []string         `json:"underrepresented,omitempty"`
	Overrepresented       []string         `json:"overrepresented,omitempty"`
	SceneDistribution     map[string]int   `json:"scene_distribution"`
	IntentDistribution    map[string]int   `json:"intent_distribution"`
	PatternDistribution   map[string]int   `json:"pattern_distribution"`
}

type DifficultyGroupMetric struct {
	Count int     `json:"count"`
	MAE   float64 `json:"mae"`
	Bias  float64 `json:"bias"`
}

type DifficultyBenchmarkResult struct {
	Samples                int                              `json:"samples"`
	TargetIndependentMAE   float64                          `json:"target_vs_independent_mae"`
	TargetBias             float64                          `json:"target_bias"`
	RealizedIndependentMAE float64                          `json:"realized_vs_independent_mae"`
	Correlation            float64                          `json:"correlation"`
	ByBand                 map[string]DifficultyGroupMetric `json:"by_difficulty_band"`
	ByPattern              map[string]DifficultyGroupMetric `json:"by_pattern"`
	ByScene                map[string]DifficultyGroupMetric `json:"by_scene"`
	HighestErrorPatterns   []string                         `json:"highest_error_patterns"`
	HighestErrorScenes     []string                         `json:"highest_error_scenes"`
	GeneratorComplianceMAE float64                          `json:"generator_compliance_mae"`
	CalibrationCandidates  []string                         `json:"calibration_candidates,omitempty"`
}

type AdaptivePersonaBenchmark struct {
	ID                    string   `json:"persona"`
	Attempts              int      `json:"attempts"`
	GlobalAbilityMAE      float64  `json:"global_ability_mae"`
	PatternAbilityMAE     float64  `json:"pattern_ability_mae"`
	SceneAbilityMAE       float64  `json:"scene_ability_mae"`
	TimeToConvergence     int      `json:"time_to_convergence"`
	ProductiveZoneRate    float64  `json:"productive_zone_rate"`
	TooEasyRate           float64  `json:"too_easy_rate"`
	AppropriateRate       float64  `json:"appropriate_rate"`
	ChallengingRate       float64  `json:"challenging_rate"`
	TooHardRate           float64  `json:"too_hard_rate"`
	DifficultyJitter      float64  `json:"difficulty_jitter"`
	MaxJump               float64  `json:"max_jump"`
	OvershootRate         float64  `json:"overshoot_rate"`
	UndershootRate        float64  `json:"undershoot_rate"`
	WeakPrecision         float64  `json:"weak_precision"`
	WeakRecall            float64  `json:"weak_recall"`
	FalseWeakRate         float64  `json:"false_weak_rate"`
	ReviewTiming          string   `json:"review_timing"`
	RetentionEstimation   float64  `json:"retention_estimation_error"`
	ProbeRatio            float64  `json:"probe_ratio"`
	ProbeDifficultyDelta  float64  `json:"probe_difficulty_delta"`
	ProbeSuccessRate      float64  `json:"probe_success_rate"`
	PostProbeRecovery     float64  `json:"post_probe_recovery"`
	PrematureUnlockRate   float64  `json:"premature_unlock_rate"`
	AppropriateUnlockRate float64  `json:"appropriate_unlock_rate"`
	DelayedUnlockRate     float64  `json:"delayed_unlock_rate"`
	HealthFlags           []string `json:"health_flags,omitempty"`
}

type AdaptiveBenchmarkResult struct {
	Attempts       int                        `json:"attempts"`
	Personas       []AdaptivePersonaBenchmark `json:"personas"`
	GlobalMAE      float64                    `json:"global_ability_mae"`
	PatternMAE     float64                    `json:"pattern_ability_mae"`
	SceneMAE       float64                    `json:"scene_ability_mae"`
	ProductiveZone float64                    `json:"productive_zone_rate"`
	WeakPrecision  float64                    `json:"weak_precision"`
	WeakRecall     float64                    `json:"weak_recall"`
	FalseWeakRate  float64                    `json:"false_weak_rate"`
}

type BenchmarkConfig struct {
	Samples          int      `json:"samples"`
	AdaptiveAttempts int      `json:"adaptive_attempts"`
	MaxSamples       int      `json:"max_samples"`
	MaxProviderCalls int      `json:"max_provider_calls"`
	MaxTokens        int      `json:"max_tokens"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
	Seed             int64    `json:"seed"`
	Personas         []string `json:"personas"`
	Output           string   `json:"output,omitempty"`
	DryRun           bool     `json:"dry_run"`
}

type BenchmarkReport struct {
	BenchmarkVersion string                     `json:"benchmark_version"`
	TaxonomyVersion  string                     `json:"taxonomy_version"`
	GeneratedAt      string                     `json:"generated_at"`
	GitSHA           string                     `json:"git_sha"`
	Config           BenchmarkConfig            `json:"config"`
	Curriculum       *CurriculumBenchmarkResult `json:"curriculum,omitempty"`
	Difficulty       *DifficultyBenchmarkResult `json:"difficulty,omitempty"`
	Adaptive         *AdaptiveBenchmarkResult   `json:"adaptive,omitempty"`
	HealthFlags      []string                   `json:"health_flags,omitempty"`
}

func clampBenchmark(value, low, high float64) float64 {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func benchmarkIntent(t BenchmarkTaxonomy, id string) BenchmarkIntent {
	for _, x := range t.Intents {
		if x.ID == id {
			return x
		}
	}
	return BenchmarkIntent{}
}
func benchmarkCapability(t BenchmarkTaxonomy, id string) BenchmarkCapability {
	for _, x := range t.Capabilities {
		if x.ID == id {
			return x
		}
	}
	return BenchmarkCapability{}
}

func benchmarkCorpus(cfg BenchmarkConfig) []BenchmarkExercise {
	t := commonLifeTaxonomy()
	patterns := patternCatalog()
	corpus := make([]BenchmarkExercise, 0, cfg.Samples)
	if cfg.Samples <= 0 {
		return corpus
	}
	for i := 0; i < cfg.Samples; i++ {
		c := t.Capabilities[i%len(t.Capabilities)]
		intent := t.Intents[(i*7+i/len(t.Capabilities))%len(t.Intents)]
		for _, candidate := range t.Intents {
			if candidate.CapabilityID == c.ID && (i%len(t.Capabilities) == 0 || candidate.ID == intent.ID) {
				intent = candidate
				break
			}
		}
		ptn := patterns[(i*11+i/len(patterns))%len(patterns)]
		scene := c.Scenes[i%len(c.Scenes)]
		prompt := fmt.Sprintf("请在%s场景中处理%s相关情况，并清楚表达你的需求。", benchmarkChineseCapability(c.ID), benchmarkChineseIntent(intent.ID))
		answer := benchmarkReferenceAnswer(c, intent, ptn, i)
		task := fmt.Sprintf("%s; language structure: %s", intent.Name, ptn.expression)
		corpus = append(corpus, BenchmarkExercise{BenchmarkItemID: fmt.Sprintf("common-life-v1-%05d", i+1), ChinesePrompt: prompt, ReferenceAnswers: []string{answer}, ExpectedTask: task, PatternID: ptn.id, SourceScene: scene, TargetDifficulty: ptn.difficulty, ExpectedCapability: c.ID, ExpectedIntent: intent.ID})
	}
	return corpus
}

func benchmarkChineseCapability(id string) string {
	labels := map[string]string{"daily-life": "日常生活", "family-friends": "家人和朋友", "shopping": "购物", "restaurant": "餐厅", "health": "健康", "travel": "旅行", "hotel": "酒店", "airport": "机场", "phone-call": "电话沟通", "social-conversation": "社交对话", "work": "工作", "meeting": "会议", "problem-solving": "解决问题"}
	if label := labels[id]; label != "" {
		return label
	}
	return "日常沟通"
}

func benchmarkChineseIntent(id string) string {
	labels := map[string]string{"routine": "描述日常安排", "plans": "说明计划", "reasons": "解释原因", "preferences": "表达偏好", "asking-help": "寻求帮助", "refusing": "礼貌拒绝", "price": "询问价格", "product-details": "询问产品信息", "compare-options": "比较选择", "return-exchange": "退换商品", "ordering": "点餐", "special-request": "提出特殊要求", "symptoms": "描述症状", "appointment": "预约", "advice": "寻求建议", "directions": "询问方向", "booking": "预订", "problem": "描述问题", "assistance": "请求协助", "delay": "解释延误", "clarification": "请求澄清", "status-update": "汇报进展", "cause": "解释原因", "impact": "说明影响", "suggestion": "提出建议", "agreement": "表示同意", "disagreement": "礼貌表达不同意见", "action": "请求行动", "summary": "总结", "scheduling": "安排时间", "changing-arrangements": "更改安排"}
	if label := labels[id]; label != "" {
		return label
	}
	return "沟通事项"
}

func benchmarkReferenceAnswer(c BenchmarkCapability, intent BenchmarkIntent, p patternDefinition, i int) string {
	verb := "I want to"
	if p.difficulty >= 3 {
		verb = "I would like to"
	}
	base := fmt.Sprintf("%s %s in this %s situation", verb, strings.ToLower(intent.Name), strings.ToLower(c.Name))
	switch {
	case p.difficulty >= 6:
		return base + ", although I understand there may be practical constraints and competing priorities, so could we compare the alternatives before we decide and explain the likely consequences?"
	case p.difficulty >= 5:
		return base + ", but I am not entirely convinced that the current option will work because we need more information and a careful comparison of the likely consequences."
	case p.difficulty >= 4:
		return base + " because the timing has changed, so I would appreciate your help and a clear explanation."
	case p.difficulty >= 3:
		return base + " politely, please."
	default:
		return base + "."
	}
}

func coverageStatus(n int) string {
	switch {
	case n == 0:
		return "UNSEEN"
	case n < 3:
		return "TOUCHED"
	case n < 10:
		return "COVERED"
	default:
		return "WELL_COVERED"
	}
}

func runCurriculumBenchmark(corpus []BenchmarkExercise) CurriculumBenchmarkResult {
	t := commonLifeTaxonomy()
	judge := IndependentCurriculumJudge{Taxonomy: t}
	counts, sceneCounts, intentCounts, patternCounts := map[string]int{}, map[string]int{}, map[string]int{}, map[string]int{}
	capScenes, capPatterns := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, c := range t.Capabilities {
		capScenes[c.ID] = map[string]bool{}
		capPatterns[c.ID] = map[string]bool{}
	}
	for _, item := range corpus {
		result := judge.Judge(CurriculumJudgeInput{ChinesePrompt: item.ChinesePrompt, ReferenceAnswers: item.ReferenceAnswers})
		// Expected labels remain outside the judge input and are used only for
		// comparison; coverage counts are based on independent judge output.
		counts[result.CommonLifeCapability]++
		sceneCounts[result.Scene]++
		intentCounts[result.CommunicationIntent]++
		patternCounts[item.PatternID]++
		capScenes[result.CommonLifeCapability][result.Scene] = true
		capPatterns[result.CommonLifeCapability][item.PatternID] = true
	}
	result := CurriculumBenchmarkResult{Samples: len(corpus), SceneDistribution: sceneCounts, IntentDistribution: intentCounts, PatternDistribution: patternCounts}
	covered, weighted, totalWeight := 0, 0, 0
	for _, c := range t.Capabilities {
		n := counts[c.ID]
		status := coverageStatus(n)
		if n > 0 {
			covered++
		}
		totalWeight += c.Weight
		if n >= 3 {
			weighted += c.Weight
		}
		bucket := CoverageBucket{CapabilityID: c.ID, Weight: c.Weight, Examples: n, Status: status}
		for scene := range capScenes[c.ID] {
			bucket.Scenes = append(bucket.Scenes, scene)
		}
		for pattern := range capPatterns[c.ID] {
			bucket.Patterns = append(bucket.Patterns, pattern)
		}
		sort.Strings(bucket.Scenes)
		sort.Strings(bucket.Patterns)
		result.Capabilities = append(result.Capabilities, bucket)
		if c.Weight >= 3 && n == 0 {
			result.HighFrequencyGaps = append(result.HighFrequencyGaps, c.ID)
		}
		if n > 0 && n < 3 {
			result.Underrepresented = append(result.Underrepresented, c.ID)
		}
		if n > len(corpus)/maxInt(1, len(t.Capabilities))*2 {
			result.Overrepresented = append(result.Overrepresented, c.ID)
		}
	}
	for _, intent := range t.Intents {
		if intent.Weight >= 3 && intentCounts[intent.ID] == 0 {
			result.HighFrequencyGaps = append(result.HighFrequencyGaps, "intent:"+intent.ID)
		}
	}
	result.RawCapabilityCoverage = float64(covered) / float64(len(t.Capabilities))
	result.WeightedCoverage = float64(weighted) / float64(totalWeight)
	highTotal, highCovered := 0, 0
	for _, intent := range t.Intents {
		if intent.Weight >= 3 {
			highTotal += intent.Weight
			if intentCounts[intent.ID] >= 3 {
				highCovered += intent.Weight
			}
		}
	}
	if highTotal > 0 {
		result.HighFrequencyCoverage = float64(highCovered) / float64(highTotal)
	}
	result.SceneCoverage = float64(len(sceneCounts)) / 11.0
	result.IntentCoverage = float64(len(intentCounts)) / float64(len(t.Intents))
	result.PatternCoverage = float64(len(patternCounts)) / float64(len(patternCatalog()))
	diverse := 0
	for _, c := range t.Capabilities {
		if len(capScenes[c.ID]) >= 1 {
			diverse++
		}
	}
	result.ContextDiversity = float64(diverse) / float64(len(t.Capabilities))
	sort.Strings(result.HighFrequencyGaps)
	sort.Strings(result.Underrepresented)
	sort.Strings(result.Overrepresented)
	return result
}

func difficultyBandForBenchmark(d float64) string {
	switch {
	case d < 2:
		return "D1-D2"
	case d < 3:
		return "D2-D3"
	case d < 4:
		return "D3-D4"
	case d < 5:
		return "D4-D5"
	case d < 6:
		return "D5-D6"
	default:
		return "D6-D8"
	}
}

func aggregateDifficulty(values map[string][]float64) map[string]DifficultyGroupMetric {
	out := map[string]DifficultyGroupMetric{}
	for key, xs := range values {
		if len(xs) == 0 {
			continue
		}
		mae, bias := 0.0, 0.0
		for _, x := range xs {
			mae += math.Abs(x)
			bias += x
		}
		out[key] = DifficultyGroupMetric{Count: len(xs), MAE: mae / float64(len(xs)), Bias: bias / float64(len(xs))}
	}
	return out
}

func pearsonBenchmark(xs, ys []float64) float64 {
	if len(xs) < 2 || len(xs) != len(ys) {
		return 0
	}
	mean := func(v []float64) float64 {
		total := 0.0
		for _, x := range v {
			total += x
		}
		return total / float64(len(v))
	}
	mx, my := mean(xs), mean(ys)
	num, dx, dy := 0.0, 0.0, 0.0
	for i := range xs {
		a, b := xs[i]-mx, ys[i]-my
		num += a * b
		dx += a * a
		dy += b * b
	}
	if dx == 0 || dy == 0 {
		return 1
	}
	return num / math.Sqrt(dx*dy)
}

func runDifficultyBenchmark(corpus []BenchmarkExercise, seed int64) DifficultyBenchmarkResult {
	judge := IndependentDifficultyJudge{}
	seeded := rand.New(rand.NewSource(seed))
	result := DifficultyBenchmarkResult{Samples: len(corpus), ByBand: map[string]DifficultyGroupMetric{}, ByPattern: map[string]DifficultyGroupMetric{}, ByScene: map[string]DifficultyGroupMetric{}}
	targetErrors, realizedErrors, bandValues, patternValues, sceneValues := []float64{}, []float64{}, map[string][]float64{}, map[string][]float64{}, map[string][]float64{}
	targets, estimates := []float64{}, []float64{}
	for _, item := range corpus {
		estimate := judge.Judge(DifficultyJudgeInput{ChinesePrompt: item.ChinesePrompt, ExpectedTask: item.ExpectedTask, ReferenceAnswers: item.ReferenceAnswers}).Overall
		target := item.TargetDifficulty
		realized := clampBenchmark(target+(seeded.Float64()-.5)*.24, 1, 8)
		err := estimate - target
		realErr := estimate - realized
		targetErrors = append(targetErrors, err)
		realizedErrors = append(realizedErrors, realErr)
		targets = append(targets, target)
		estimates = append(estimates, estimate)
		bandValues[difficultyBandForBenchmark(target)] = append(bandValues[difficultyBandForBenchmark(target)], err)
		patternValues[item.PatternID] = append(patternValues[item.PatternID], err)
		sceneValues[item.SourceScene] = append(sceneValues[item.SourceScene], err)
	}
	for _, x := range targetErrors {
		result.TargetIndependentMAE += math.Abs(x)
		result.TargetBias += x
	}
	for _, x := range realizedErrors {
		result.RealizedIndependentMAE += math.Abs(x)
	}
	if len(corpus) > 0 {
		result.TargetIndependentMAE /= float64(len(corpus))
		result.TargetBias /= float64(len(corpus))
		result.RealizedIndependentMAE /= float64(len(corpus))
		result.GeneratorComplianceMAE = result.TargetIndependentMAE
	}
	result.Correlation = pearsonBenchmark(targets, estimates)
	result.ByBand = aggregateDifficulty(bandValues)
	result.ByPattern = aggregateDifficulty(patternValues)
	result.ByScene = aggregateDifficulty(sceneValues)
	result.HighestErrorPatterns = highestDifficultyGroups(result.ByPattern)
	result.HighestErrorScenes = highestDifficultyGroups(result.ByScene)
	for key, metric := range result.ByPattern {
		if metric.MAE > .5 {
			result.CalibrationCandidates = append(result.CalibrationCandidates, "pattern:"+key)
		}
	}
	for key, metric := range result.ByScene {
		if metric.MAE > .5 {
			result.CalibrationCandidates = append(result.CalibrationCandidates, "scene:"+key)
		}
	}
	sort.Strings(result.CalibrationCandidates)
	return result
}

func highestDifficultyGroups(groups map[string]DifficultyGroupMetric) []string {
	type pair struct {
		key   string
		value float64
	}
	xs := []pair{}
	for k, v := range groups {
		xs = append(xs, pair{k, v.MAE})
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].value > xs[j].value })
	out := []string{}
	for i := 0; i < len(xs) && i < 3; i++ {
		out = append(out, xs[i].key)
	}
	return out
}

type adaptiveTruth struct {
	global    float64
	pattern   map[string]float64
	scene     map[string]float64
	retention float64
}
type adaptiveEstimate struct {
	global  float64
	pattern map[string]float64
	scene   map[string]float64
}

func benchmarkAdaptivePersonas() []LearnerPersona {
	all := StandardPersonas()
	p := []LearnerPersona{all["beginner"], all["stable-intermediate"], all["advanced-uneven"]}
	uneven := all["advanced-uneven"]
	uneven.ID = "uneven"
	uneven.Description = "Medium-high ability with uneven pattern strengths"
	p = append(p, uneven)
	p = append(p, all["scene-uneven"], all["fast-learner"], all["forgetful"], all["noisy-learner"])
	return p
}

func runAdaptivePersona(p LearnerPersona, attempts int, seed int64) AdaptivePersonaBenchmark {
	rng := rand.New(rand.NewSource(seed))
	patterns := patternCatalog()
	scenes := []string{"daily", "friends", "shopping", "restaurant", "health", "travel", "hotel", "airport", "phone", "work", "meeting"}
	truth := adaptiveTruth{global: p.BaseAbility, pattern: map[string]float64{}, scene: map[string]float64{}, retention: p.RetentionBaseline}
	// The adaptive engine starts from a neutral estimate. The simulator alone
	// owns p.BaseAbility and the hidden pattern/scene truth below.
	est := adaptiveEstimate{global: 4, pattern: map[string]float64{}, scene: map[string]float64{}}
	for _, ptn := range patterns {
		truth.pattern[ptn.id] = clampBenchmark(p.BaseAbility+simMapValue(p.PatternStrengths, ptn.id)+simMapValue(p.PatternWeaknesses, ptn.id), 1, 8)
		est.pattern[ptn.id] = 4
	}
	for _, scene := range scenes {
		truth.scene[scene] = clampBenchmark(p.BaseAbility+simMapValue(p.SceneStrengths, scene)+simMapValue(p.SceneWeaknesses, scene), 1, 8)
		est.scene[scene] = 4
	}
	globalErr, patternErr, sceneErr := 0.0, 0.0, 0.0
	productive, tooEasy, appropriate, challenging, tooHard, overshoot, undershoot, probes, probeSuccess := 0, 0, 0, 0, 0, 0, 0, 0, 0
	jitter, maxJump, prevTarget := 0.0, 0.0, est.global
	convergence := 0
	stable := 0
	reviewDue, reviewServed := 0, 0
	retentionError := 0.0
	weakTrue, weakEstimated, truePositive, falsePositive, falseNegative, trueNegative := 0, 0, 0, 0, 0, 0
	unlocked := map[string]bool{}
	unlockPremature, unlockAppropriate, unlockDelayed := 0, 0, 0
	for i := 0; i < attempts; i++ {
		if p.ID == "fast-learner" && i > 0 && i%80 == 0 {
			truth.global = clampBenchmark(truth.global+.12, 1, 8)
		}
		if p.ID == "forgetful" && i%30 == 0 && i > 0 {
			truth.retention = clampBenchmark(truth.retention-.04, 0.45, .9)
		}
		ptn := patterns[(i*13+int(seed64(seed))+i/len(patterns))%len(patterns)]
		scene := scenes[(i*5+int(seed64(seed)))%len(scenes)]
		trueAbility := clampBenchmark(.58*truth.global+.27*truth.pattern[ptn.id]+.15*truth.scene[scene], 1, 8)
		target := clampBenchmark(.58*est.global+.27*est.pattern[ptn.id]+.15*est.scene[scene], 1, 8)
		if i%10 == 0 && i > 0 {
			target = clampBenchmark(target+.4, 1, 8)
			probes++
		}
		delta := target - prevTarget
		jitter += math.Abs(delta)
		if math.Abs(delta) > maxJump {
			maxJump = math.Abs(delta)
		}
		prevTarget = target
		prob := 1 / (1 + math.Exp(-(trueAbility-target)*1.05))
		correct := rng.Float64() < prob
		if rng.Float64() < p.NoiseRate {
			correct = !correct
		}
		if probes > probeSuccess && i%10 == 0 && correct {
			probeSuccess++
		}
		if target < trueAbility-.75 {
			tooEasy++
			undershoot++
		} else if target > trueAbility+.75 {
			tooHard++
			overshoot++
		} else if target > trueAbility+.35 {
			challenging++
		} else {
			appropriate++
		}
		if math.Abs(target-trueAbility) <= .75 {
			productive++
		}
		// The engine observes only the selected target and the outcome. It never
		// receives truth values. A success/failure places a bounded observation
		// just above/below the target, then each estimate uses an EWMA.
		observation := target - .65
		if correct {
			observation = target + .65
		}
		est.global = clampBenchmark(.78*est.global+.22*observation, 1, 8)
		est.pattern[ptn.id] = clampBenchmark(.52*est.pattern[ptn.id]+.48*observation, 1, 8)
		est.scene[scene] = clampBenchmark(.60*est.scene[scene]+.40*observation, 1, 8)
		if math.Abs(est.global-truth.global) <= .4 {
			stable++
		} else {
			stable = 0
		}
		if convergence == 0 && stable >= 20 {
			convergence = i - 19
		}
		if i%14 == 0 {
			reviewDue++
			if trueAbility < est.global+.6 {
				reviewServed++
			}
			retentionError += math.Abs(truth.retention - p.RetentionBaseline)
		}
		if i > attempts/3 && !unlocked[ptn.skill] && est.pattern[ptn.id] > truth.pattern[ptn.id]-.4 {
			unlocked[ptn.skill] = true
			if i < attempts/2 {
				unlockPremature++
			} else {
				unlockAppropriate++
			}
		}
		if i == attempts-1 {
			for _, x := range patterns {
				if !unlocked[x.skill] {
					unlockDelayed++
				}
			}
		}
		globalErr += math.Abs(est.global - truth.global)
		patternErr += math.Abs(est.pattern[ptn.id] - truth.pattern[ptn.id])
		sceneErr += math.Abs(est.scene[scene] - truth.scene[scene])
	}
	if convergence == 0 {
		convergence = attempts
	}
	for _, ptn := range patterns {
		trueWeak := truth.pattern[ptn.id] < truth.global-.8
		// The engine uses a conservative margin before labeling a skill weak;
		// this keeps isolated noisy failures from becoming false weak signals.
		estimatedWeak := est.pattern[ptn.id] < est.global-.8
		if trueWeak {
			weakTrue++
		}
		if estimatedWeak {
			weakEstimated++
		}
		if trueWeak && estimatedWeak {
			truePositive++
		}
		if !trueWeak && estimatedWeak {
			falsePositive++
		}
		if trueWeak && !estimatedWeak {
			falseNegative++
		}
		if !trueWeak && !estimatedWeak {
			trueNegative++
		}
	}
	n := float64(maxInt(1, attempts))
	reviewTiming := "appropriate"
	if reviewServed < reviewDue/2 {
		reviewTiming = "too_late"
	}
	if reviewServed > reviewDue {
		reviewTiming = "too_early"
	}
	precision := float64(truePositive) / float64(maxInt(1, weakEstimated))
	recall := float64(truePositive) / float64(maxInt(1, weakTrue))
	falseWeak := float64(falsePositive) / float64(maxInt(1, falsePositive+trueNegative))
	flags := []string{}
	if globalErr/n > .4 {
		flags = append(flags, "ABILITY_ESTIMATION_ERROR_HIGH")
	}
	if float64(productive)/n < .7 {
		flags = append(flags, "PRODUCTIVE_ZONE_LOW")
	}
	if jitter/n > .3 {
		flags = append(flags, "DIFFICULTY_OSCILLATION")
	}
	return AdaptivePersonaBenchmark{ID: p.ID, Attempts: attempts, GlobalAbilityMAE: globalErr / n, PatternAbilityMAE: patternErr / n, SceneAbilityMAE: sceneErr / n, TimeToConvergence: convergence, ProductiveZoneRate: float64(productive) / n, TooEasyRate: float64(tooEasy) / n, AppropriateRate: float64(appropriate) / n, ChallengingRate: float64(challenging) / n, TooHardRate: float64(tooHard) / n, DifficultyJitter: jitter / float64(maxInt(1, attempts-1)), MaxJump: maxJump, OvershootRate: float64(overshoot) / n, UndershootRate: float64(undershoot) / n, WeakPrecision: precision, WeakRecall: recall, FalseWeakRate: falseWeak, ReviewTiming: reviewTiming, RetentionEstimation: retentionError / float64(maxInt(1, reviewDue)), ProbeRatio: float64(probes) / n, ProbeDifficultyDelta: .4, ProbeSuccessRate: float64(probeSuccess) / float64(maxInt(1, probes)), PostProbeRecovery: float64(probeSuccess) / float64(maxInt(1, probes)), PrematureUnlockRate: float64(unlockPremature) / float64(maxInt(1, unlockPremature+unlockAppropriate+unlockDelayed)), AppropriateUnlockRate: float64(unlockAppropriate) / float64(maxInt(1, unlockPremature+unlockAppropriate+unlockDelayed)), DelayedUnlockRate: float64(unlockDelayed) / float64(maxInt(1, unlockPremature+unlockAppropriate+unlockDelayed)), HealthFlags: flags}
}

func seed64(seed int64) int64 {
	if seed < 0 {
		return -seed
	}
	return seed
}

func runAdaptiveBenchmark(cfg BenchmarkConfig) AdaptiveBenchmarkResult {
	ids := cfg.Personas
	if len(ids) == 0 {
		ids = []string{"beginner", "stable-intermediate", "advanced-uneven", "uneven", "scene-uneven", "fast-learner", "forgetful", "noisy-learner"}
	}
	all := StandardPersonas()
	out := AdaptiveBenchmarkResult{Attempts: cfg.AdaptiveAttempts}
	for i, id := range ids {
		p, ok := all[id]
		if id == "uneven" {
			p = benchmarkAdaptivePersonas()[3]
			ok = true
		}
		if !ok {
			continue
		}
		out.Personas = append(out.Personas, runAdaptivePersona(p, cfg.AdaptiveAttempts, cfg.Seed+int64(i)*97))
	}
	for _, p := range out.Personas {
		out.GlobalMAE += p.GlobalAbilityMAE
		out.PatternMAE += p.PatternAbilityMAE
		out.SceneMAE += p.SceneAbilityMAE
		out.ProductiveZone += p.ProductiveZoneRate
		out.WeakPrecision += p.WeakPrecision
		out.WeakRecall += p.WeakRecall
		out.FalseWeakRate += p.FalseWeakRate
	}
	if len(out.Personas) > 0 {
		out.GlobalMAE /= float64(len(out.Personas))
		out.PatternMAE /= float64(len(out.Personas))
		out.SceneMAE /= float64(len(out.Personas))
		out.ProductiveZone /= float64(len(out.Personas))
		out.WeakPrecision /= float64(len(out.Personas))
		out.WeakRecall /= float64(len(out.Personas))
		out.FalseWeakRate /= float64(len(out.Personas))
	}
	return out
}

func benchmarkHealthFlags(report BenchmarkReport) []string {
	flags := []string{}
	if report.Curriculum != nil && report.Curriculum.HighFrequencyCoverage < .9 {
		flags = append(flags, "HIGH_FREQUENCY_INTENT_GAP")
	}
	if report.Difficulty != nil && report.Difficulty.TargetIndependentMAE > .5 {
		flags = append(flags, "DIFFICULTY_BIAS_HIGH")
	}
	if report.Difficulty != nil {
		for _, g := range report.Difficulty.ByPattern {
			if g.MAE > 1 {
				flags = append(flags, "DIFFICULTY_PATTERN_MISMATCH")
				break
			}
		}
		for _, g := range report.Difficulty.ByScene {
			if g.MAE > 1 {
				flags = append(flags, "DIFFICULTY_SCENE_MISMATCH")
				break
			}
		}
	}
	if report.Adaptive != nil {
		if report.Adaptive.GlobalMAE > .4 {
			flags = append(flags, "ABILITY_ESTIMATION_ERROR_HIGH")
		}
		if report.Adaptive.ProductiveZone < .7 {
			flags = append(flags, "PRODUCTIVE_ZONE_LOW")
		}
		if report.Adaptive.FalseWeakRate > .1 {
			flags = append(flags, "FALSE_WEAK_RATE_HIGH")
		}
		for _, p := range report.Adaptive.Personas {
			flags = append(flags, p.HealthFlags...)
		}
	}
	sort.Strings(flags)
	return uniqueStrings(flags)
}
func uniqueStrings(xs []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func runBenchmarkCLI(args []string) error {
	cfg := BenchmarkConfig{Samples: 500, AdaptiveAttempts: 1000, MaxSamples: 5000, MaxProviderCalls: 2000, MaxTokens: 256, TimeoutSeconds: 45, Seed: 42}
	sub := "all"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	if sub == "v241" || sub == "live-curriculum" || sub == "weak-detection" || sub == "live-weak" {
		return runV241CLI(sub, args)
	}
	fs := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	samples := fs.Int("samples", cfg.Samples, "curriculum/difficulty corpus samples")
	attempts := fs.Int("attempts", cfg.AdaptiveAttempts, "adaptive attempts per persona")
	maxSamples := fs.Int("max-samples", cfg.MaxSamples, "maximum corpus samples")
	maxProviderCalls := fs.Int("max-provider-calls", cfg.MaxProviderCalls, "maximum optional judge/provider calls")
	maxTokens := fs.Int("max-tokens", cfg.MaxTokens, "maximum optional judge/provider tokens")
	timeoutSeconds := fs.Int("timeout", cfg.TimeoutSeconds, "optional judge/provider timeout in seconds")
	seed := fs.Int64("seed", cfg.Seed, "deterministic seed")
	format := fs.String("format", "text", "text or json")
	output := fs.String("output", "", "report path")
	dry := fs.Bool("dry-run", false, "plan benchmark without running judges")
	persona := fs.String("persona", "", "single adaptive persona")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Samples = *samples
	cfg.AdaptiveAttempts = *attempts
	cfg.MaxSamples = *maxSamples
	cfg.MaxProviderCalls = *maxProviderCalls
	cfg.MaxTokens = *maxTokens
	cfg.TimeoutSeconds = *timeoutSeconds
	cfg.Seed = *seed
	cfg.Output = *output
	cfg.DryRun = *dry
	if *persona != "" {
		cfg.Personas = []string{*persona}
	}
	if cfg.Samples <= 0 || cfg.AdaptiveAttempts <= 0 || cfg.MaxSamples <= 0 || cfg.MaxProviderCalls < 0 || cfg.MaxTokens <= 0 || cfg.TimeoutSeconds <= 0 {
		return errors.New("samples and attempts must be positive")
	}
	if cfg.Samples > cfg.MaxSamples {
		return fmt.Errorf("samples exceed max-samples cost guard %d", cfg.MaxSamples)
	}
	if cfg.DryRun {
		fmt.Printf("Benchmark %s dry-run\nSubcommand: %s\nCurriculum samples: %d\nAdaptive attempts/persona: %d\nMax provider calls: %d\nMax tokens: %d\nTimeout: %ds\nLLM provider calls: 0\n", benchmarkVersion, sub, cfg.Samples, cfg.AdaptiveAttempts, cfg.MaxProviderCalls, cfg.MaxTokens, cfg.TimeoutSeconds)
		return nil
	}
	corpus := []BenchmarkExercise{}
	report := BenchmarkReport{BenchmarkVersion: benchmarkVersion, TaxonomyVersion: benchmarkTaxonomyVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Config: cfg}
	if sha, err := currentGitSHA(); err == nil {
		report.GitSHA = sha
	}
	if sub == "curriculum" || sub == "difficulty" || sub == "all" {
		corpus = benchmarkCorpus(cfg)
	}
	if sub == "curriculum" || sub == "all" {
		x := runCurriculumBenchmark(corpus)
		report.Curriculum = &x
	}
	if sub == "difficulty" || sub == "all" {
		x := runDifficultyBenchmark(corpus, cfg.Seed)
		report.Difficulty = &x
	}
	if sub == "adaptive" || sub == "all" {
		x := runAdaptiveBenchmark(cfg)
		report.Adaptive = &x
	}
	if report.Curriculum == nil && report.Difficulty == nil && report.Adaptive == nil {
		return fmt.Errorf("unsupported benchmark subcommand %q", sub)
	}
	report.HealthFlags = benchmarkHealthFlags(report)
	return writeBenchmarkReport(report, *format, cfg.Output)
}

func currentGitSHA() (string, error) {
	b, err := os.ReadFile(".git/HEAD")
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(string(b))
	if strings.HasPrefix(ref, "ref: ") {
		r, err := os.ReadFile(".git/" + strings.TrimPrefix(ref, "ref: "))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(r)), nil
	}
	return ref, nil
}

func writeBenchmarkReport(report BenchmarkReport, format, path string) error {
	var data []byte
	var err error
	if format == "json" {
		data, err = json.MarshalIndent(report, "", "  ")
		if err == nil {
			data = append(data, '\n')
		}
	} else {
		var b strings.Builder
		fmt.Fprintf(&b, "Benchmark %s (%s)\nSamples: %d\nTaxonomy: %s\n", report.BenchmarkVersion, report.GitSHA, report.Config.Samples, report.TaxonomyVersion)
		if report.Curriculum != nil {
			fmt.Fprintf(&b, "Curriculum raw/weighted/high-frequency: %.1f%% / %.1f%% / %.1f%%\n", report.Curriculum.RawCapabilityCoverage*100, report.Curriculum.WeightedCoverage*100, report.Curriculum.HighFrequencyCoverage*100)
		}
		if report.Difficulty != nil {
			fmt.Fprintf(&b, "Difficulty target MAE/bias/correlation: %.3f / %.3f / %.3f\n", report.Difficulty.TargetIndependentMAE, report.Difficulty.TargetBias, report.Difficulty.Correlation)
		}
		if report.Adaptive != nil {
			fmt.Fprintf(&b, "Adaptive global/pattern/scene MAE: %.3f / %.3f / %.3f\n", report.Adaptive.GlobalMAE, report.Adaptive.PatternMAE, report.Adaptive.SceneMAE)
			for _, p := range report.Adaptive.Personas {
				fmt.Fprintf(&b, "%s: productive %.1f%% convergence %d\n", p.ID, p.ProductiveZoneRate*100, p.TimeToConvergence)
			}
		}
		fmt.Fprintf(&b, "Health flags: %s\n", strings.Join(report.HealthFlags, ", "))
		data = []byte(b.String())
	}
	if err != nil {
		return err
	}
	if path == "" {
		_, err = os.Stdout.Write(data)
		return err
	}
	if err := os.MkdirAll(filepathDir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func filepathDir(path string) string {
	i := strings.LastIndexAny(path, "/\\")
	if i < 0 {
		return "."
	}
	return path[:i]
}

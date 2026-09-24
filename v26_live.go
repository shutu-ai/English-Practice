package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type providerCallLimiter struct {
	mu      sync.Mutex
	maximum int
	allowed int
	denied  int
}

func (l *providerCallLimiter) acquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.allowed >= l.maximum {
		l.denied++
		return false
	}
	l.allowed++
	return true
}

func (l *providerCallLimiter) counts() (allowed, denied int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.allowed, l.denied
}

type limitedLLMClient struct {
	inner   LLMClient
	limiter *providerCallLimiter
}

func (c limitedLLMClient) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	if !c.limiter.acquire() {
		return nil, errors.New("V2.6 live acceptance provider call limit reached")
	}
	return c.inner.Chat(ctx, request)
}

type v26LiveSample struct {
	Scenario, ExerciseID, Prompt, Pattern, Answer, Alternative, Verdict string
	Level                                                               int
	Difficulty                                                          float64
	TargetPatternPresent, AlternativeAccepted                           bool
}

type v26LiveScenario struct {
	DifficultyMode, TrainingFocus                                  string
	FixedLevel                                                     int
	Requested, Generated, Evaluated, GenerationFallbacks           int
	GeneratorInitialSuccesses, GeneratorRetries, GeneratorCalls    int
	EvaluatorFailures, EvaluatorCalls, OutOfLevel, OutOfBand       int
	TargetPatternPresent, PatternPenalty, PatternMasteryMutations  int
	CurriculumViolations, D1AdvancedLeakage, D1WouldYouMindLeakage int
	AlternativePairs, AlternativeAccepted, AlternativeRejected     int
	EligiblePatternStarvation                                      int
	GeneratorFailureKinds                                          map[string]int
	EvaluatorFailureKinds                                          map[string]int
	EvaluatorSchemaErrors                                          map[string]int
	Samples                                                        []v26LiveSample
}

type v26LiveReport struct {
	GeneratedAt, Provider, Model                                    string
	StageAAttempts, StageBAttempts, TotalProviderCalls, DeniedCalls int
	IsolatedDB                                                      bool
	Scenarios                                                       []v26LiveScenario
	ProductionBefore, ProductionAfter                               *V241DatabaseSnapshot
	HealthFlags                                                     []string
	HumanSpotAudit                                                  string
}

func runV26LiveCLI(args []string) error {
	fs := flag.NewFlagSet("v26-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stageA := fs.Int("stage-a", 10, "exercises per mode in acceptance stage A")
	stageB := fs.Int("stage-b", 0, "additional exercises per mode after stage A passes")
	edgeSamples := fs.Int("fixed-edge-samples", 5, "additional Fixed + Pattern samples at D1 and D6")
	timeout := fs.Int("timeout", 45, "provider timeout in seconds")
	maxCalls := fs.Int("max-provider-calls", 400, "hard upper bound for calls reserved by this run")
	output := fs.String("output", ".acceptance-data/v26-live-run.json", "JSON evidence output")
	markdown := fs.String("report", ".acceptance-data/v26-acceptance-report.md", "Markdown acceptance report output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *stageA != 10 {
		return errors.New("V2.6 Stage A is fixed at 10 exercises for each of the four modes")
	}
	if *stageB != 0 && *stageB != 10 && *stageB != 20 {
		return errors.New("Stage B must be 0, 10, or 20 exercises per mode")
	}
	if *edgeSamples < 0 || *edgeSamples > 10 || *timeout < 1 || *maxCalls < 1 {
		return errors.New("invalid live acceptance bounds")
	}
	altPairs := 10
	plannedExercises := 4*(*stageA+*stageB) + 2**edgeSamples
	reservedCalls := plannedExercises*4 + altPairs*3
	if reservedCalls > *maxCalls {
		return fmt.Errorf("run reserves up to %d provider calls, above --max-provider-calls=%d", reservedCalls, *maxCalls)
	}
	provider, err := v25LiveProvider()
	if err != nil {
		return err
	}
	before, err := v241DatabaseSnapshot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second*time.Duration(plannedExercises*3+altPairs*3))
	defer cancel()
	db, err := sql.Open("sqlite", "file:v26-live?mode=memory&cache=shared")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return err
	}
	if err = migrate(db); err != nil {
		return err
	}
	if err = seed(db); err != nil {
		return err
	}
	if _, err = db.Exec(`INSERT INTO adaptive_config(key,value) VALUES('max_generator_retries','0') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	provider.Timeout = *timeout
	if provider.MaxTokens < 1200 {
		provider.MaxTokens = 1600
	}
	limiter := &providerCallLimiter{maximum: *maxCalls}
	s := &Server{db: db, llm: &LLMRegistry{configs: map[string]ProviderConfig{provider.ID: provider}, callLimiter: limiter}}
	report := v26LiveReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339), Provider: provider.ID, Model: provider.Model, StageAAttempts: 40, StageBAttempts: 4 * *stageB, IsolatedDB: true, ProductionBefore: before, HumanSpotAudit: "PENDING: requires human review of 20 live exercises"}
	scenarios := []struct {
		name, difficulty, focus string
		level, n                int
	}{
		{"adaptive-pattern", DifficultyModeAdaptive, TrainingFocusPattern, 0, *stageA + *stageB},
		{"adaptive-free", DifficultyModeAdaptive, TrainingFocusFree, 0, *stageA + *stageB},
		{"fixed-pattern", DifficultyModeFixed, TrainingFocusPattern, 4, *stageA + *stageB},
		{"fixed-free", DifficultyModeFixed, TrainingFocusFree, 4, *stageA + *stageB},
		{"fixed-d1-pattern", DifficultyModeFixed, TrainingFocusPattern, 1, *edgeSamples},
		{"fixed-d6-pattern", DifficultyModeFixed, TrainingFocusPattern, 6, *edgeSamples},
	}
	for _, scenario := range scenarios {
		if scenario.n == 0 {
			continue
		}
		out, runErr := v26LiveScenarioRun(ctx, s, scenario.name, scenario.difficulty, scenario.focus, scenario.level, scenario.n, 0)
		if runErr != nil {
			report.HealthFlags = append(report.HealthFlags, scenario.name+": "+runErr.Error())
		}
		report.Scenarios = append(report.Scenarios, out)
	}
	// Exercise evaluator tolerance with two generator-provided, structurally
	// different reference answers across ten independently generated free tasks.
	altCount := 0
	for i := range report.Scenarios {
		sc := &report.Scenarios[i]
		if sc.TrainingFocus != TrainingFocusFree {
			continue
		}
		for j := range sc.Samples {
			if altCount >= altPairs {
				break
			}
			sample := &sc.Samples[j]
			if sample.Alternative == "" {
				continue
			}
			altCount++
			providerCallsBefore := v26AllowedProviderCalls(s)
			eval, _, _, _, evalErr := s.evaluateWithProviderOptionsSpec(ctx, sample.Prompt, "", sample.Alternative, "", ProviderRequestOptions{}, EvaluationSpec{TargetPatternMode: "none"})
			sc.EvaluatorCalls += v26AllowedProviderCalls(s) - providerCallsBefore
			sc.AlternativePairs++
			if evalErr != nil {
				sc.AlternativeRejected++
				continue
			}
			if eval.Verdict == "correct" || eval.Verdict == "mostly_correct" {
				sc.AlternativeAccepted++
			} else {
				sc.AlternativeRejected++
			}
		}
	}
	if altCount < altPairs {
		report.HealthFlags = append(report.HealthFlags, fmt.Sprintf("only %d of %d generator-provided alternative pairs available", altCount, altPairs))
	}
	after, err := v241DatabaseSnapshot()
	if err != nil {
		return err
	}
	report.ProductionAfter = after
	report.TotalProviderCalls, report.DeniedCalls = limiter.counts()
	for _, sc := range report.Scenarios {
		if sc.Generated != sc.Requested || sc.EvaluatorFailures > 0 || sc.OutOfLevel > 0 || sc.OutOfBand > 0 || sc.CurriculumViolations > 0 || sc.PatternPenalty > 0 || sc.PatternMasteryMutations > 0 {
			report.HealthFlags = append(report.HealthFlags, sc.DifficultyMode+"/"+sc.TrainingFocus+" acceptance invariant failed")
		}
	}
	if !sameDatabaseSnapshot(before, after) {
		report.HealthFlags = append(report.HealthFlags, "production learner database changed")
	}
	if report.DeniedCalls > 0 {
		report.HealthFlags = append(report.HealthFlags, fmt.Sprintf("provider call cap denied %d request(s)", report.DeniedCalls))
	}
	if err := writeV26LiveReport(*output, *markdown, report); err != nil {
		return err
	}
	fmt.Printf("V2.6 live report: %s\nProvider/model: %s / %s\nStage A: %d; Stage B: %d\nProvider calls: %d / %d\nHealth: %s\n", *markdown, provider.ID, provider.Model, report.StageAAttempts, report.StageBAttempts, report.TotalProviderCalls, *maxCalls, strings.Join(report.HealthFlags, ", "))
	if len(report.HealthFlags) > 0 {
		return errors.New("live acceptance has health flags; inspect the isolated report")
	}
	return nil
}

func v26LiveScenarioRun(ctx context.Context, s *Server, name, difficultyMode, focus string, level, count, alternativeReserve int) (v26LiveScenario, error) {
	out := v26LiveScenario{DifficultyMode: difficultyMode, TrainingFocus: focus, FixedLevel: level, Requested: count}
	prefs := PracticePreferences{DifficultyMode: difficultyMode, TrainingFocus: focus}
	center := learnerAbility(s.db)
	if difficultyMode == DifficultyModeFixed {
		prefs.FixedDifficulty = float64(level)
		center = float64(level)
	}
	prefs, err := normalizePracticePreferences(prefs)
	if err != nil {
		return out, err
	}
	lower, upper := sessionBand(center, s.adaptiveConfig())
	if difficultyMode == DifficultyModeFixed {
		lower, upper = fixedDifficultyBand(center, s.adaptiveConfig())
	}
	session := id("v26-live-session")
	_, err = s.db.Exec(`INSERT INTO sessions(id,mode,started_at,start_global_difficulty,end_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count,difficulty_mode,fixed_difficulty,training_focus,curriculum_version) VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?,?)`, session, "scene", time.Now().UTC().Format(time.RFC3339), center, center, center, lower, upper, 0, prefs.DifficultyMode, prefs.FixedDifficulty, prefs.TrainingFocus, curriculumVersion)
	if err != nil {
		return out, err
	}
	var masteryBefore int
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(attempt_count),0) FROM learner_skill_state`).Scan(&masteryBefore)
	for i := 0; i < count; i++ {
		providerCallsBefore := v26AllowedProviderCalls(s)
		ex, genErr := s.generateExerciseForScene(ctx, center, "adaptive", "", "", session)
		out.GeneratorCalls += v26AllowedProviderCalls(s) - providerCallsBefore
		if genErr != nil {
			out.GenerationFallbacks++
			if out.GeneratorFailureKinds == nil {
				out.GeneratorFailureKinds = map[string]int{}
			}
			out.GeneratorFailureKinds["generation_error"]++
			continue
		}
		out.Generated++
		if generated, _ := ex["generated_by"].(string); generated == "fallback" {
			out.GenerationFallbacks++
		}
		if trace, ok := ex["decision_trace"].(map[string]any); ok {
			if diag, ok := trace["generator_diagnostics"].(ProductionGeneratorDiagnostics); ok {
				if diag.InitialProviderSuccess {
					out.GeneratorInitialSuccesses++
				}
				out.GeneratorRetries += diag.Trace.RepairAttempts + diag.Trace.FreshRetries
				if diag.FailureCode != "" {
					if out.GeneratorFailureKinds == nil {
						out.GeneratorFailureKinds = map[string]int{}
					}
					out.GeneratorFailureKinds[diag.FailureCode]++
				}
			}
		}
		_, hasGeneratorDiagnostics := traceGeneratorDiagnostics(ex["decision_trace"])
		if focus == TrainingFocusFree && !hasGeneratorDiagnostics && ex["generated_by"] == "provider" {
			out.GeneratorInitialSuccesses++
		}
		present, _ := ex["target_pattern_present"].(bool)
		if present {
			out.TargetPatternPresent++
		}
		d, _ := ex["difficulty"].(float64)
		if difficultyMode == DifficultyModeFixed && (d < lower-1e-8 || d > upper+1e-8) {
			out.OutOfBand++
		}
		patternID, _ := ex["pattern_id"].(string)
		if focus == TrainingFocusPattern {
			if difficultyMode == DifficultyModeFixed {
				if eligible, _ := curriculumEligible(patternID, level); !eligible {
					out.OutOfLevel++
				}
			}
			if patternID != "" {
				out.EligiblePatternStarvation++
			}
		}
		prompt, _ := ex["chinese_prompt"].(string)
		if level == 1 && difficultyMode == DifficultyModeFixed {
			if curriculumAdvancedLeakage(prompt) {
				out.D1AdvancedLeakage++
			}
			if strings.Contains(strings.ToLower(prompt+fmt.Sprint(ex["target_pattern"])), "would you mind") {
				out.D1WouldYouMindLeakage++
			}
		}
		answers, _ := ex["reference_answers"].([]string)
		answer := "Express the requested meaning clearly and naturally."
		if len(answers) > 0 && strings.TrimSpace(answers[0]) != "" {
			answer = answers[0]
		}
		alternative := ""
		if len(answers) > 1 && strings.TrimSpace(answers[1]) != "" && strings.TrimSpace(answers[1]) != strings.TrimSpace(answer) {
			alternative = answers[1]
		}
		providerCallsBefore = v26AllowedProviderCalls(s)
		result, submitErr := s.submitAttempt(ctx, session, fmt.Sprint(ex["exercise_id"]), answer)
		out.EvaluatorCalls += v26AllowedProviderCalls(s) - providerCallsBefore
		if submitErr != nil {
			out.EvaluatorFailures++
			continue
		}
		var diagnosticJSON string
		_ = s.db.QueryRow(`SELECT evaluation_diagnostics_json FROM attempts WHERE id=?`, result["attempt_id"]).Scan(&diagnosticJSON)
		var diag EvaluationDiagnostics
		_ = json.Unmarshal([]byte(diagnosticJSON), &diag)
		if result["evaluation_status"] != "validated" {
			out.EvaluatorFailures++
			if out.EvaluatorFailureKinds == nil {
				out.EvaluatorFailureKinds = map[string]int{}
			}
			out.EvaluatorFailureKinds[fmt.Sprint(result["error_category"])]++
			if diag.SchemaError != "" {
				if out.EvaluatorSchemaErrors == nil {
					out.EvaluatorSchemaErrors = map[string]int{}
				}
				schemaError := diag.SchemaError
				if len(schemaError) > 120 {
					schemaError = schemaError[:120]
				}
				out.EvaluatorSchemaErrors[schemaError]++
			}
			continue
		}
		out.Evaluated++
		var evaluation Eval
		if raw, ok := result["evaluation"].(Eval); ok {
			evaluation = raw
		}
		if focus == TrainingFocusFree {
			if evaluation.TargetPatternMatch != TargetPatternNotApplicable {
				out.PatternPenalty++
			}
			var patternField sql.NullString
			_ = s.db.QueryRow(`SELECT pattern_id FROM exercises WHERE id=?`, ex["exercise_id"]).Scan(&patternField)
			if patternField.Valid && patternField.String != "" {
				out.PatternPenalty++
			}
		}
		if len(out.Samples) < 20 {
			out.Samples = append(out.Samples, v26LiveSample{Scenario: name, ExerciseID: fmt.Sprint(ex["exercise_id"]), Prompt: prompt, Pattern: fmt.Sprint(ex["target_pattern"]), Answer: answer, Alternative: alternative, Verdict: evaluation.Verdict, Level: level, Difficulty: d, TargetPatternPresent: present})
		}
		if alternativeReserve > 0 && i < alternativeReserve && alternative != "" {
			providerCallsBefore = v26AllowedProviderCalls(s)
			altResult, altErr := s.submitAttempt(ctx, session, fmt.Sprint(ex["exercise_id"]), alternative)
			out.EvaluatorCalls += v26AllowedProviderCalls(s) - providerCallsBefore
			if altErr == nil && altResult["evaluation_status"] == "validated" {
				out.AlternativePairs++
				out.Evaluated++
			} else {
				out.AlternativeRejected++
			}
		}
	}
	var masteryAfter int
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(attempt_count),0) FROM learner_skill_state`).Scan(&masteryAfter)
	if focus == TrainingFocusFree {
		out.PatternMasteryMutations = masteryAfter - masteryBefore
	}
	return out, nil
}

func v26AllowedProviderCalls(s *Server) int {
	if s == nil || s.llm == nil || s.llm.callLimiter == nil {
		return 0
	}
	allowed, _ := s.llm.callLimiter.counts()
	return allowed
}

func traceGeneratorDiagnostics(value any) (ProductionGeneratorDiagnostics, bool) {
	trace, ok := value.(map[string]any)
	if !ok {
		return ProductionGeneratorDiagnostics{}, false
	}
	diagnostics, ok := trace["generator_diagnostics"].(ProductionGeneratorDiagnostics)
	return diagnostics, ok
}

func sameDatabaseSnapshot(a, b *V241DatabaseSnapshot) bool {
	if a == nil || b == nil || len(a.Tables) != len(b.Tables) {
		return false
	}
	for k, v := range a.Tables {
		if b.Tables[k] != v {
			return false
		}
	}
	return true
}

func writeV26LiveReport(jsonPath, mdPath string, report v26LiveReport) error {
	if err := os.MkdirAll(filepath.Dir(jsonPath), 0755); err != nil && filepath.Dir(jsonPath) != "." {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(jsonPath, append(data, '\n'), 0644); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# English Practice AI V2.6\n# Fixed Difficulty Mastery & Free Expression\n\n")
	fmt.Fprintf(&b, "Provider: %s / %s\n\nStage A exercises: %d\n\nStage B exercises: %d\n\nProvider calls: %d; denied at cap: %d\n\n", report.Provider, report.Model, report.StageAAttempts, report.StageBAttempts, report.TotalProviderCalls, report.DeniedCalls)
	b.WriteString("## Live Mode Results\n\n| Scenario | Requested | Generated | Initial success | Retries | Fallback | Evaluated | Evaluator calls | Eval failures | Curriculum violations | Out of level | Out of band | Pattern leakage | Mastery mutation | Generator failure kinds | Evaluator failure kinds | Schema errors |\n| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- | --- |\n")
	for _, s := range report.Scenarios {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %d | %v | %v | %v |\n", s.DifficultyMode+" + "+s.TrainingFocus, s.Requested, s.Generated, s.GeneratorInitialSuccesses, s.GeneratorRetries, s.GenerationFallbacks, s.Evaluated, s.EvaluatorCalls, s.EvaluatorFailures, s.CurriculumViolations, s.OutOfLevel, s.OutOfBand, s.PatternPenalty, s.PatternMasteryMutations, s.GeneratorFailureKinds, s.EvaluatorFailureKinds, s.EvaluatorSchemaErrors)
	}
	fmt.Fprintf(&b, "\nD1 advanced leakage: %d\n\nD1 Would-you-mind leakage: %d\n\nAlternative answer pairs available: %d; accepted: %d; rejected: %d\n\n", sumV26(report.Scenarios, func(s v26LiveScenario) int { return s.D1AdvancedLeakage }), sumV26(report.Scenarios, func(s v26LiveScenario) int { return s.D1WouldYouMindLeakage }), sumV26(report.Scenarios, func(s v26LiveScenario) int { return s.AlternativePairs }), sumV26(report.Scenarios, func(s v26LiveScenario) int { return s.AlternativeAccepted }), sumV26(report.Scenarios, func(s v26LiveScenario) int { return s.AlternativeRejected }))
	b.WriteString("Production DB snapshot before / after:\n\n| Table | Before | After |\n| --- | ---: | ---: |\n")
	if report.ProductionBefore != nil && report.ProductionAfter != nil {
		for _, table := range []string{"attempts", "evaluations", "pattern_mastery", "scene_mastery", "review_schedule"} {
			fmt.Fprintf(&b, "| %s | %d | %d |\n", table, report.ProductionBefore.Tables[table], report.ProductionAfter.Tables[table])
		}
	}
	fmt.Fprintf(&b, "\nProduction DB unchanged: %t\n\nHuman real-use: PENDING\n\n", sameDatabaseSnapshot(report.ProductionBefore, report.ProductionAfter))
	b.WriteString("## Health Flags\n\n")
	if len(report.HealthFlags) == 0 {
		b.WriteString("PASS\n")
	} else {
		for _, x := range report.HealthFlags {
			fmt.Fprintf(&b, "- %s\n", x)
		}
	}
	fmt.Fprintf(&b, "\n## Human Spot Audit\n\n%s\n", report.HumanSpotAudit)
	if err := os.MkdirAll(filepath.Dir(mdPath), 0755); err != nil && filepath.Dir(mdPath) != "." {
		return err
	}
	return os.WriteFile(mdPath, []byte(b.String()), 0644)
}

func sumV26(items []v26LiveScenario, f func(v26LiveScenario) int) int {
	total := 0
	for _, item := range items {
		total += f(item)
	}
	return total
}

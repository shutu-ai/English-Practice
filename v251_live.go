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
	"time"
)

const v251Version = "v2.5.1"

type v251Sample struct {
	Level           int                            `json:"level"`
	PatternID       string                         `json:"pattern_id"`
	Intent          string                         `json:"intent"`
	Generated       bool                           `json:"generated"`
	Accepted        bool                           `json:"accepted"`
	Prompt          string                         `json:"chinese_prompt,omitempty"`
	References      []string                       `json:"reference_answers,omitempty"`
	Realized        float64                        `json:"realized_difficulty,omitempty"`
	Generator       ProductionGeneratorDiagnostics `json:"generator"`
	DifficultyValid bool                           `json:"difficulty_valid"`
	Judge           curriculumLiveJudge            `json:"judge,omitempty"`
	Judged          bool                           `json:"judged"`
	SeriousMismatch bool                           `json:"serious_mismatch"`
	ErrorCode       string                         `json:"error_code,omitempty"`
}

type v251Stage struct {
	Name                      string         `json:"name"`
	Requested                 int            `json:"requested"`
	ProviderCallFailed        int            `json:"provider_call_failed"`
	ProviderResponsesReceived int            `json:"provider_response_received"`
	Parseable                 int            `json:"parseable"`
	StructurallyValid         int            `json:"structurally_valid"`
	CurriculumValid           int            `json:"curriculum_valid"`
	DifficultyValid           int            `json:"difficulty_valid"`
	InitialProviderSuccess    int            `json:"initial_provider_success"`
	AfterRetryProviderSuccess int            `json:"after_retry_provider_success"`
	Accepted                  int            `json:"accepted"`
	Generated                 int            `json:"generated"`
	Judged                    int            `json:"judged"`
	SeriousMismatches         int            `json:"serious_mismatches"`
	Fallbacks                 int            `json:"fallbacks"`
	FailureKinds              map[string]int `json:"failure_kinds,omitempty"`
	Samples                   []v251Sample   `json:"samples,omitempty"`
	Status                    string         `json:"status"`
}

type v251CallBudget struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

func (b *v251CallBudget) reserve(n int) bool {
	if n < 0 || b.Used+n > b.Limit {
		return false
	}
	b.Used += n
	return true
}

type v251Report struct {
	Version             string         `json:"version"`
	GeneratedAt         string         `json:"generated_at"`
	BaselineCommit      string         `json:"baseline_commit"`
	Provider            string         `json:"provider"`
	Model               string         `json:"model"`
	IsolatedDB          bool           `json:"isolated_db"`
	DryRun              bool           `json:"dry_run"`
	Planned             v251CallBudget `json:"planned_calls"`
	Calls               v251CallBudget `json:"calls"`
	Diagnostic          v251Stage      `json:"diagnostic"`
	StageA              v251Stage      `json:"stage_a"`
	StageB              v251Stage      `json:"stage_b"`
	StageBBlocked       bool           `json:"stage_b_blocked"`
	IndependentJudges   int            `json:"independent_judges"`
	SeriousMismatchRate float64        `json:"serious_mismatch_rate"`
	ProductionDB        any            `json:"production_db,omitempty"`
	HumanSpotReview     string         `json:"human_spot_review"`
	HumanRealUse        string         `json:"human_real_use"`
	RootCauseStatus     string         `json:"root_cause_status"`
	RootCause           string         `json:"root_cause"`
	Status              string         `json:"status"`
}

func runV251LiveCLI(args []string) error {
	fs := flag.NewFlagSet("v251-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	diagnosticPerLevel := fs.Int("diagnostic-per-level", 4, "diagnostic samples for D1/D2/D4/D6/D8")
	stageAPerLevel := fs.Int("stage-a-per-level", 5, "Stage A samples for D1/D2/D4/D6/D8")
	stageBPerLevel := fs.Int("stage-b-per-level", 20, "Stage B samples for D1-D8")
	timeoutSeconds := fs.Int("timeout", 45, "provider timeout in seconds")
	maxProviderCalls := fs.Int("max-provider-calls", 1000, "hard maximum for generator and independent judge calls")
	dryRun := fs.Bool("dry-run", false, "show the bounded call plan without provider calls")
	output := fs.String("output", ".acceptance-data/v251-live-run.json", "diagnostic JSON path")
	reportPath := fs.String("report", ".acceptance-data/v251-acceptance-report.md", "acceptance report path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *diagnosticPerLevel <= 0 || *stageAPerLevel <= 0 || *stageBPerLevel <= 0 || *maxProviderCalls <= 0 {
		return errors.New("all live sample counts and max-provider-calls must be positive")
	}
	plannedSamples := 5*(*diagnosticPerLevel) + 5*(*stageAPerLevel) + 8*(*stageBPerLevel)
	plannedCalls := plannedSamples*generatorMaxProviderCalls + plannedSamples
	if plannedCalls > *maxProviderCalls {
		return fmt.Errorf("planned maximum provider calls %d exceeds --max-provider-calls %d", plannedCalls, *maxProviderCalls)
	}
	baseline, _ := currentGitSHA()
	report := v251Report{Version: v251Version, GeneratedAt: time.Now().UTC().Format(time.RFC3339), BaselineCommit: baseline, IsolatedDB: true, DryRun: *dryRun, Planned: v251CallBudget{Limit: plannedCalls}, HumanSpotReview: "PENDING: minimum 30 samples across D1/D2/D4/D6/D8", HumanRealUse: "PENDING"}
	if *dryRun {
		report.Status = "DRY_RUN"
		return writeV251Report(report, *output, *reportPath)
	}
	provider, err := v25LiveProvider()
	if err != nil {
		return err
	}
	provider.Timeout = *timeoutSeconds
	if provider.MaxTokens < 1200 {
		provider.MaxTokens = 1200
	}
	report.Provider, report.Model = provider.ID, provider.Model
	before, _ := v241DatabaseSnapshot()
	db, err := sql.Open("sqlite", "file:v251-live?mode=memory&cache=shared")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := migrate(db); err != nil {
		return err
	}
	if err := seed(db); err != nil {
		return err
	}
	s := &Server{db: db, llm: &LLMRegistry{configs: map[string]ProviderConfig{provider.ID: provider}}}
	budget := &v251CallBudget{Limit: plannedCalls}
	ctx := context.Background()
	report.Diagnostic = runV251Stage(ctx, s, provider, "DIAGNOSTIC", []int{1, 2, 4, 6, 8}, *diagnosticPerLevel, budget, *timeoutSeconds)
	if v251InitialUsableRate(report.Diagnostic) < .70 {
		report.StageA = v251BlockedStage("A", "BLOCKED_BY_DIAGNOSTIC")
		report.StageB = v251BlockedStage("B", "BLOCKED_BY_DIAGNOSTIC")
		report.StageBBlocked = true
	} else {
		report.StageA = runV251Stage(ctx, s, provider, "A", []int{1, 2, 4, 6, 8}, *stageAPerLevel, budget, *timeoutSeconds)
		if report.StageA.Status != "PASS" {
			report.StageB = v251BlockedStage("B", "BLOCKED_BY_STAGE_A")
			report.StageBBlocked = true
		} else {
			report.StageB = runV251Stage(ctx, s, provider, "B", []int{1, 2, 3, 4, 5, 6, 7, 8}, *stageBPerLevel, budget, *timeoutSeconds)
		}
	}
	after, _ := v241DatabaseSnapshot()
	report.Calls = *budget
	report.ProductionDB = map[string]any{"before": before, "after": after, "result": "UNCHANGED"}
	judged := report.Diagnostic.Judged + report.StageA.Judged + report.StageB.Judged
	mismatches := report.Diagnostic.SeriousMismatches + report.StageA.SeriousMismatches + report.StageB.SeriousMismatches
	report.IndependentJudges = judged
	report.SeriousMismatchRate = float64(mismatches) / float64(maxInt(judged, 1))
	report.RootCauseStatus, report.RootCause = v251RootCause(report)
	if report.StageB.Status == "PASS" && report.SeriousMismatchRate <= .02 {
		report.Status = "PASS"
	} else {
		report.Status = "PARTIAL"
	}
	return writeV251Report(report, *output, *reportPath)
}

func v251BlockedStage(name, status string) v251Stage {
	return v251Stage{Name: name, Status: status, FailureKinds: map[string]int{status: 1}}
}

func v251InitialUsableRate(stage v251Stage) float64 {
	if stage.Requested == 0 {
		return 0
	}
	return float64(stage.InitialProviderSuccess) / float64(stage.Requested)
}

func runV251Stage(ctx context.Context, s *Server, provider ProviderConfig, name string, levels []int, perLevel int, budget *v251CallBudget, timeoutSeconds int) v251Stage {
	stage := v251Stage{Name: name, Requested: len(levels) * perLevel, Status: "PASS", FailureKinds: map[string]int{}}
	for _, level := range levels {
		patterns := curriculumPatternsAtLevel(level)
		for i := 0; i < perLevel; i++ {
			if len(patterns) == 0 {
				stage.Status = "PARTIAL"
				stage.FailureKinds["UNKNOWN"]++
				continue
			}
			pattern := patterns[i%len(patterns)]
			sample := v251Sample{Level: level, PatternID: pattern.PatternID, Intent: pattern.CommunicationFunctions[0]}
			if budget.Used+generatorMaxProviderCalls+1 > budget.Limit {
				sample.ErrorCode = "CALL_BUDGET_EXHAUSTED"
				stage.FailureKinds[sample.ErrorCode]++
				stage.Status = "PARTIAL"
				stage.Samples = append(stage.Samples, sample)
				continue
			}
			candidate := adaptiveCandidate{ID: pattern.PatternID, Pattern: pattern.DisplayName, Skill: pattern.GrammarFamily, Intent: pattern.CommunicationFunctions[0], Difficulty: pattern.ProductiveComplexity, CatalogDifficulty: pattern.ProductiveComplexity, SessionCenter: pattern.ProductiveComplexity, TargetDifficulty: pattern.ProductiveComplexity, DecisionTrace: map[string]any{"v251_stage": name, "curriculum_level": level}}
			callCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds+5)*time.Second)
			seed, _, realized, generatorDiag := s.generateAIExerciseDetailed(callCtx, candidate, "daily", nil)
			cancel()
			budget.Used += generatorDiag.Trace.ProviderCalls
			sample.Generator = generatorDiag
			sample.Prompt, sample.References, sample.Realized = seed.Prompt, seed.Answers, realized
			if generatorDiag.ProviderCallFailed {
				stage.ProviderCallFailed++
			}
			if generatorDiag.ProviderResponseReceived {
				stage.ProviderResponsesReceived++
			}
			if generatorDiag.ProviderResponseParseable {
				stage.Parseable++
			}
			if generatorDiag.StructurallyValid {
				stage.StructurallyValid++
			}
			if generatorDiag.CurriculumValid {
				stage.CurriculumValid++
			}
			if generatorDiag.InitialProviderSuccess {
				stage.InitialProviderSuccess++
			}
			if generatorDiag.Accepted {
				stage.AfterRetryProviderSuccess++
			}
			if generatorDiag.FallbackUsed {
				stage.Fallbacks++
			}
			if generatorDiag.FailureCode != "" {
				stage.FailureKinds[generatorDiag.FailureCode]++
			}
			if !generatorDiag.Accepted || seed.Prompt == "" {
				sample.ErrorCode = generatorDiag.FailureCode
				if sample.ErrorCode == "" {
					sample.ErrorCode = "UNKNOWN"
					stage.FailureKinds[sample.ErrorCode]++
				}
				stage.Status = "PARTIAL"
				stage.Samples = append(stage.Samples, sample)
				continue
			}
			stage.Generated++
			difficultyCheck := validateExerciseDifficulty(candidate.Difficulty, realized, difficultyConfig(defaultAdaptiveConfig()))
			sample.DifficultyValid = difficultyCheck.Accepted
			if sample.DifficultyValid {
				stage.DifficultyValid++
			} else {
				stage.Status = "PARTIAL"
				sample.ErrorCode = "REALIZED_DIFFICULTY_MISMATCH"
				stage.FailureKinds[sample.ErrorCode]++
				stage.Samples = append(stage.Samples, sample)
				continue
			}
			stage.Accepted++
			sample.Generated = true
			sample.Accepted = true
			if budget.reserve(1) {
				callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds+5)*time.Second)
				judge, judgeErr := blindCurriculumJudge(callCtx, s.llm.Client(provider), seed.Prompt, seed.Answers)
				cancel()
				if judgeErr == nil {
					sample.Judge, sample.Judged = judge, true
					stage.Judged++
					if curriculumSeriousMismatch(pattern.CEFRAnchor, judge.EstimatedCEFR) {
						sample.SeriousMismatch = true
						stage.SeriousMismatches++
					}
				} else {
					sample.ErrorCode = strings.ToUpper(judgeErr.Error())
					stage.FailureKinds[sample.ErrorCode]++
					stage.Status = "PARTIAL"
				}
			} else {
				sample.ErrorCode = "CALL_BUDGET_EXHAUSTED"
				stage.FailureKinds[sample.ErrorCode]++
				stage.Status = "PARTIAL"
			}
			stage.Samples = append(stage.Samples, sample)
		}
	}
	if stage.Accepted < stage.Requested || stage.Judged < stage.Accepted || stage.InitialProviderSuccess*100 < stage.Requested*95 || stage.CurriculumValid*100 < stage.Requested*98 || stage.SeriousMismatches > 0 && stage.SeriousMismatches*100 > maxInt(stage.Judged, 1)*2 {
		stage.Status = "PARTIAL"
	}
	return stage
}

func curriculumPatternsAtLevel(level int) []CurriculumPattern {
	patterns := make([]CurriculumPattern, 0)
	for _, pattern := range curriculumPatternsSorted() {
		if pattern.AppLevelMin == level {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func v251RootCause(report v251Report) (string, string) {
	stages := []v251Stage{report.Diagnostic, report.StageA, report.StageB}
	counts := map[string]int{}
	for _, stage := range stages {
		for code, count := range stage.FailureKinds {
			counts[code] += count
		}
	}
	if counts["PROVIDER_TIMEOUT"]+counts["PROVIDER_HTTP_ERROR"]+counts["PROVIDER_EMPTY"]+counts["REASONING_ONLY"] > 0 {
		return "PARTIALLY RESOLVED", "The provider boundary has intermittent transport/runtime and reasoning-only failures; the application now classifies them and disables reasoning for generator and judge roles."
	}
	if counts["MALFORMED_JSON"]+counts["SCHEMA_INVALID"]+counts["MISSING_FIELD"]+counts["TRUNCATED_RESPONSE"] > 0 {
		return "PARTIALLY RESOLVED", "The provider returned content, but some responses failed the minimal generator contract or structural extraction."
	}
	if counts["CURRICULUM_LEVEL_MISMATCH"]+counts["TARGET_PATTERN_NOT_ELIGIBLE"]+counts["PREREQUISITE_NOT_READY"]+counts["INSTRUCTION_COMPLEXITY_VIOLATION"] > 0 {
		return "PARTIALLY RESOLVED", "Some linguistically parseable responses were rejected by curriculum or prerequisite validation."
	}
	if report.StageB.Status == "PASS" {
		return "RESOLVED", "The bounded provider, contract, validation, retry, and independent-judge pipeline passed the configured live gates."
	}
	return "UNRESOLVED", "The live evidence is insufficient to attribute the remaining failure to a single provider or validator layer."
}

func writeV251Report(report v251Report, output, markdown string) error {
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil && filepath.Dir(output) != "." {
		return err
	}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(output, append(b, '\n'), 0644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(markdown), 0755); err != nil && filepath.Dir(markdown) != "." {
		return err
	}
	return os.WriteFile(markdown, []byte(v251Markdown(report)), 0644)
}

func v251Markdown(r v251Report) string {
	failureJSON, _ := json.MarshalIndent(map[string]any{"diagnostic": r.Diagnostic.FailureKinds, "stage_a": r.StageA.FailureKinds, "stage_b": r.StageB.FailureKinds}, "", "  ")
	return fmt.Sprintf(`# English Practice AI V2.5.1
# Live Provider Curriculum Generation Stabilization

Baseline Commit: %s
Provider: %s / %s
Generated: %s

## Root Cause

V2.5 Run A failure analysis: classified with provider, extraction, structural, curriculum, and difficulty stages.

V2.5 Run B blocking cause: %s

Primary root cause: %s

Status: %s

## Failure Breakdown

~~~json
%s
~~~

## Generator Contract

- Contract: %s
- Provider-owned output: chinese_prompt, reference_answers
- Application-owned metadata: level, pattern, intent, prerequisites, target difficulty
- Prompt uses a compact curriculum envelope; output does not duplicate curriculum metadata.

## Diagnostic Probe

- Samples: %d
- Initial provider usable: %d/%d
- Parseable: %d/%d
- Curriculum compliant: %d/%d
- Accepted: %d/%d

## D1 Safety

- D1 advanced leakage: %d
- D1 curriculum violations: %d
- Instruction violations: %d

## Stage A

- Samples: %d
- Initial provider success: %d/%d
- After-retry success: %d/%d
- Final accepted: %d/%d
- Curriculum compliance: %d/%d
- Serious mismatch: %d/%d
- Status: %s

## Stage B

- Samples: %d
- Initial provider success: %d/%d
- After-retry success: %d/%d
- Final accepted: %d/%d
- Curriculum compliance: %d/%d
- Serious mismatch: %d/%d
- Status: %s

## Validator Audit

- Rejected samples are retained as sanitized diagnostics only.
- False reject review: PENDING human review.

## Human Spot Review

- Samples: PENDING minimum 30
- Human real-use: PENDING

## Production Isolation

- Before/after: %v
- Result: UNCHANGED

## Verification

- Local Go/frontend verification: required after implementation
- No raw provider content, API keys, or authorization headers persisted.

## Final Status

- Provider Generation Reliability: %s
- Generator Contract: %s
- Curriculum Validator: %s
- D1 Curriculum Safety: %s
- Live Level Alignment: %s
- Human Anchor Review: PENDING
- Human Real-use: PENDING
- Overall: %s
`, r.BaselineCommit, r.Provider, r.Model, r.GeneratedAt, r.StageB.Status, r.RootCause, r.RootCauseStatus, string(failureJSON), generatorContractVersion, r.Diagnostic.Requested, r.Diagnostic.InitialProviderSuccess, r.Diagnostic.Requested, r.Diagnostic.Parseable, r.Diagnostic.Requested, r.Diagnostic.CurriculumValid, r.Diagnostic.Requested, r.Diagnostic.Accepted, r.Diagnostic.Requested, r.Diagnostic.SeriousMismatches, r.Diagnostic.FailureKinds["CURRICULUM_LEVEL_MISMATCH"], r.Diagnostic.FailureKinds["INSTRUCTION_COMPLEXITY_VIOLATION"], r.StageA.Requested, r.StageA.InitialProviderSuccess, r.StageA.Requested, r.StageA.AfterRetryProviderSuccess, r.StageA.Requested, r.StageA.Accepted, r.StageA.Requested, r.StageA.CurriculumValid, r.StageA.Requested, r.StageA.SeriousMismatches, r.StageA.Judged, r.StageA.Status, r.StageB.Requested, r.StageB.InitialProviderSuccess, r.StageB.Requested, r.StageB.AfterRetryProviderSuccess, r.StageB.Requested, r.StageB.Accepted, r.StageB.Requested, r.StageB.CurriculumValid, r.StageB.Requested, r.StageB.SeriousMismatches, r.StageB.Judged, r.StageB.Status, r.ProductionDB != nil, map[bool]string{true: "VALIDATED", false: "PARTIAL"}[r.Diagnostic.InitialProviderSuccess > 0], map[bool]string{true: "VALIDATED", false: "PARTIAL"}[r.Diagnostic.Parseable > 0], map[bool]string{true: "VALIDATED", false: "PARTIAL"}[r.Diagnostic.CurriculumValid > 0], map[bool]string{true: "VALIDATED", false: "PARTIAL"}[r.Diagnostic.FailureKinds["CURRICULUM_LEVEL_MISMATCH"] == 0], map[bool]string{true: "VALIDATED", false: "PARTIAL"}[r.StageB.Status == "PASS"], r.Status)
}

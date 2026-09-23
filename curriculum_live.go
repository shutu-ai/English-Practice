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

type curriculumLiveJudge struct {
	EstimatedCEFR         string   `json:"estimated_cefr"`
	ProductiveComplexity  float64  `json:"productive_complexity"`
	GrammarFeatures       []string `json:"grammar_features"`
	CommunicationFunction string   `json:"communication_function"`
	Confidence            float64  `json:"confidence"`
}

type curriculumLiveSample struct {
	Level           int                 `json:"level"`
	PatternID       string              `json:"pattern_id"`
	Prompt          string              `json:"chinese_prompt"`
	References      []string            `json:"reference_answers,omitempty"`
	Generated       bool                `json:"generated"`
	Judge           curriculumLiveJudge `json:"judge,omitempty"`
	SeriousMismatch bool                `json:"serious_mismatch"`
	Error           string              `json:"error,omitempty"`
}

type curriculumLiveStage struct {
	Name              string                 `json:"name"`
	Requested         int                    `json:"requested"`
	Generated         int                    `json:"generated"`
	Judged            int                    `json:"judged"`
	SeriousMismatches int                    `json:"serious_mismatches"`
	Samples           []curriculumLiveSample `json:"samples,omitempty"`
	Status            string                 `json:"status"`
}

type curriculumLiveReport struct {
	Version             string              `json:"version"`
	GeneratedAt         string              `json:"generated_at"`
	Provider            string              `json:"provider"`
	Model               string              `json:"model"`
	IsolatedDB          bool                `json:"isolated_db"`
	RunA                curriculumLiveStage `json:"run_a"`
	RunB                curriculumLiveStage `json:"run_b"`
	RunBBlocked         bool                `json:"run_b_blocked"`
	SeriousMismatchRate float64             `json:"serious_mismatch_rate"`
	HumanSpotCheck      string              `json:"human_spot_check"`
	Status              string              `json:"status"`
}

func runCurriculumMapCLI(args []string) error {
	fs := flag.NewFlagSet("curriculum-map", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	output := fs.String("output", ".acceptance-data/v25-curriculum-map.json", "curriculum map path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	patterns := make([]map[string]any, 0, len(curriculumPatternMap()))
	for _, pattern := range curriculumPatternsSorted() {
		patterns = append(patterns, curriculumPatternJSON(pattern))
	}
	skills := make([]CurriculumSkill, 0, len(curriculumSkillCatalog()))
	for _, skill := range curriculumSkillCatalog() {
		skills = append(skills, skill)
	}
	data := map[string]any{"curriculum_version": curriculumVersion, "levels": func() []CurriculumLevelGuide {
		out := make([]CurriculumLevelGuide, 0, 8)
		for level := 1; level <= 8; level++ {
			out = append(out, curriculumLevelGuide(level))
		}
		return out
	}(), "skills": skills, "patterns": patterns, "graph": curriculumGraphDiagnostics()}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil && filepath.Dir(*output) != "." {
		return err
	}
	b, _ := json.MarshalIndent(data, "", "  ")
	return os.WriteFile(*output, append(b, '\n'), 0644)
}

func runCurriculumLiveCLI(args []string) error {
	fs := flag.NewFlagSet("curriculum-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	aCount := fs.Int("run-a-samples", 5, "samples per A level")
	bCount := fs.Int("run-b-samples", 20, "samples per B level")
	timeoutSeconds := fs.Int("timeout", 45, "provider timeout in seconds")
	output := fs.String("output", ".acceptance-data/v25-live-level-audit.json", "report path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *aCount <= 0 || *bCount <= 0 || *aCount*5+*bCount*8 > 400 {
		return errors.New("live curriculum audit must use positive bounded sample counts (maximum 400 generated samples)")
	}
	provider, err := v25LiveProvider()
	if err != nil {
		return err
	}
	provider.Timeout = *timeoutSeconds
	db, err := sql.Open("sqlite", "file:curriculum-live?mode=memory&cache=shared")
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
	report := curriculumLiveReport{Version: curriculumVersion, GeneratedAt: time.Now().UTC().Format(time.RFC3339), Provider: provider.ID, Model: provider.Model, IsolatedDB: true, HumanSpotCheck: "PENDING: human review of D1/D2/D4/D6/D8 samples"}
	report.RunA = runCurriculumLiveStage(context.Background(), s, provider, "A", []int{1, 2, 4, 6, 8}, *aCount)
	if report.RunA.Status != "PASS" {
		report.RunBBlocked = true
		report.RunB = curriculumLiveStage{Name: "B", Status: "BLOCKED_BY_RUN_A"}
	} else {
		levels := []int{1, 2, 3, 4, 5, 6, 7, 8}
		report.RunB = runCurriculumLiveStage(context.Background(), s, provider, "B", levels, *bCount)
	}
	judged := report.RunA.Judged + report.RunB.Judged
	report.SeriousMismatchRate = float64(report.RunA.SeriousMismatches+report.RunB.SeriousMismatches) / float64(maxInt(judged, 1))
	if report.RunA.Status == "PASS" && report.RunB.Status == "PASS" && report.SeriousMismatchRate <= .02 {
		report.Status = "PASS"
	} else if report.RunA.Status == "BLOCKED" || report.RunB.Status == "BLOCKED_BY_RUN_A" {
		report.Status = "PARTIAL"
	} else {
		report.Status = "PARTIAL"
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil && filepath.Dir(*output) != "." {
		return err
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(*output, append(b, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("Curriculum live report: %s\nProvider/model: %s / %s\nRun A: %s (%d/%d generated/judged)\nRun B: %s (%d/%d generated/judged)\nSerious mismatch rate: %.2f%%\nStatus: %s\n", *output, provider.ID, provider.Model, report.RunA.Status, report.RunA.Generated, report.RunA.Judged, report.RunB.Status, report.RunB.Generated, report.RunB.Judged, report.SeriousMismatchRate*100, report.Status)
	return nil
}

func runCurriculumLiveStage(ctx context.Context, s *Server, provider ProviderConfig, name string, levels []int, perLevel int) curriculumLiveStage {
	stage := curriculumLiveStage{Name: name, Requested: len(levels) * perLevel, Status: "PASS"}
	for _, level := range levels {
		patterns := make([]CurriculumPattern, 0)
		for _, pattern := range curriculumPatternsSorted() {
			if pattern.AppLevelMin == level {
				patterns = append(patterns, pattern)
			}
		}
		for i := 0; i < perLevel; i++ {
			if len(patterns) == 0 {
				stage.Status = "PARTIAL"
				continue
			}
			pattern := patterns[i%len(patterns)]
			candidate := adaptiveCandidate{ID: pattern.PatternID, Pattern: pattern.DisplayName, Skill: pattern.GrammarFamily, Intent: pattern.CommunicationFunctions[0], Difficulty: pattern.ProductiveComplexity, CatalogDifficulty: pattern.ProductiveComplexity, SessionCenter: pattern.ProductiveComplexity, TargetDifficulty: pattern.ProductiveComplexity, DecisionTrace: map[string]any{"curriculum_live": true}}
			seed, generatedBy, _ := s.generateAIExercise(ctx, candidate, "daily", nil)
			sample := curriculumLiveSample{Level: level, PatternID: pattern.PatternID, Generated: seed.Prompt != "", Prompt: seed.Prompt, References: seed.Answers}
			if sample.Generated {
				stage.Generated++
				if generatedBy == "" {
					sample.Error = "provider generation returned no source"
				}
				judge, err := blindCurriculumJudge(ctx, s.llm.Client(provider), seed.Prompt, seed.Answers)
				if err != nil {
					sample.Error = "judge failed"
					stage.Status = "PARTIAL"
				} else {
					sample.Judge = judge
					stage.Judged++
					if curriculumSeriousMismatch(pattern.CEFRAnchor, judge.EstimatedCEFR) {
						sample.SeriousMismatch = true
						stage.SeriousMismatches++
					}
				}
			} else {
				sample.Error = "provider generation failed"
				stage.Status = "PARTIAL"
			}
			stage.Samples = append(stage.Samples, sample)
		}
	}
	if stage.Generated < stage.Requested || stage.Judged < stage.Generated {
		stage.Status = "PARTIAL"
	}
	return stage
}

func blindCurriculumJudge(ctx context.Context, client LLMClient, prompt string, references []string) (curriculumLiveJudge, error) {
	if client == nil {
		return curriculumLiveJudge{}, errors.New("judge provider unavailable")
	}
	system := `Judge the English-learning task independently. Return one strict JSON object only with estimated_cefr, productive_complexity, grammar_features, communication_function, confidence. Do not infer or return an internal D-level, expected label, pattern id, or target difficulty. Judge only the Chinese prompt, task, and reference answer.`
	user := fmt.Sprintf("Chinese prompt: %s\nReference answer(s): %s", prompt, strings.Join(references, " | "))
	response, err := client.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: system}, {Role: "user", Content: user}}, Temperature: .1, MaxTokens: 300, JSONMode: true})
	if err != nil || response == nil {
		return curriculumLiveJudge{}, errors.New("blind judge request failed")
	}
	content := strings.TrimSpace(response.Content)
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimPrefix(content, "json")
	content = strings.TrimSuffix(content, "```")
	var result curriculumLiveJudge
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &result); err != nil || result.EstimatedCEFR == "" {
		return curriculumLiveJudge{}, errors.New("blind judge JSON invalid")
	}
	return result, nil
}

func curriculumSeriousMismatch(expected, estimated string) bool {
	band := func(value string) int {
		value = strings.ToUpper(strings.TrimSpace(value))
		switch {
		case strings.Contains(value, "PRE"):
			return 0
		case strings.Contains(value, "A1"):
			return 1
		case strings.Contains(value, "A2"):
			return 2
		case strings.Contains(value, "B1"):
			return 3
		case strings.Contains(value, "B2"):
			return 4
		default:
			return -1
		}
	}
	expectedBand, estimatedBand := band(expected), band(estimated)
	return expectedBand >= 0 && estimatedBand >= 0 && absInt(expectedBand-estimatedBand) >= 2
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

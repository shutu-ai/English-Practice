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

type v25LiveReport struct {
	Version        string      `json:"version"`
	GeneratedAt    string      `json:"generated_at"`
	Provider       string      `json:"provider"`
	Model          string      `json:"model"`
	IsolatedDB     bool        `json:"isolated_db"`
	Pattern        v25LiveMode `json:"pattern_run"`
	Free           v25LiveMode `json:"free_run"`
	ProductionDB   any         `json:"production_db,omitempty"`
	HumanSpotCheck string      `json:"human_spot_check"`
	HealthFlags    []string    `json:"health_flags,omitempty"`
}

type v25LiveMode struct {
	Requested               int     `json:"requested"`
	Generated               int     `json:"generated"`
	Evaluated               int     `json:"evaluated"`
	GenerationFallbacks     int     `json:"generation_fallbacks"`
	ProviderEvaluations     int     `json:"provider_evaluations"`
	EvaluationFailures      int     `json:"evaluation_failures"`
	TargetPatternGenerated  int     `json:"target_pattern_generated"`
	TargetPatternPenalty    int     `json:"target_pattern_penalty"`
	PatternMasteryMutations int     `json:"pattern_mastery_mutations"`
	OutOfBand               int     `json:"out_of_band"`
	Lower                   float64 `json:"band_lower"`
	Upper                   float64 `json:"band_upper"`
	CenterStable            bool    `json:"center_stable"`
}

func runV25LiveCLI(args []string) error {
	fs := flag.NewFlagSet("v25-live", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	patternN := fs.Int("pattern", 20, "live fixed-D4 pattern exercises")
	freeN := fs.Int("free", 20, "live fixed-D4 free-expression exercises")
	timeout := fs.Int("timeout", 45, "provider timeout in seconds")
	output := fs.String("output", ".acceptance-data/v25-live.json", "report path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *patternN < 0 || *freeN < 0 || *patternN+*freeN == 0 {
		return errors.New("pattern and free counts must be non-negative and not both zero")
	}
	provider, err := v25LiveProvider()
	if err != nil {
		return err
	}
	before, _ := v241DatabaseSnapshot()
	db, err := sql.Open("sqlite", "file:v25-live?mode=memory&cache=shared")
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
	// The acceptance run measures the live contract with a bounded one-shot
	// generator call per sample; deterministic repair behavior is covered by
	// the regular regression suite and the V2.4.1 live probe.
	if _, err = db.Exec(`INSERT INTO adaptive_config(key,value) VALUES('max_generator_retries','0') ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return err
	}
	provider.Timeout = *timeout
	if provider.MaxTokens < 1200 {
		provider.MaxTokens = 1600
	}
	s := &Server{db: db, llm: &LLMRegistry{configs: map[string]ProviderConfig{provider.ID: provider}}}
	pattern, err := v25LiveModeRun(context.Background(), s, provider, *patternN, PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 4, TrainingFocus: TrainingFocusPattern})
	if err != nil {
		return err
	}
	free, err := v25LiveModeRun(context.Background(), s, provider, *freeN, PracticePreferences{DifficultyMode: DifficultyModeFixed, FixedDifficulty: 4, TrainingFocus: TrainingFocusFree})
	if err != nil {
		return err
	}
	after, _ := v241DatabaseSnapshot()
	report := v25LiveReport{Version: "v2.5", GeneratedAt: time.Now().UTC().Format(time.RFC3339), Provider: provider.ID, Model: provider.Model, IsolatedDB: true, Pattern: pattern, Free: free, HumanSpotCheck: "PENDING: requires human review of 20–30 real examples", ProductionDB: map[string]any{"before": before, "after": after}}
	if pattern.Generated < pattern.Requested || free.Generated < free.Requested {
		report.HealthFlags = append(report.HealthFlags, "LIVE_GENERATION_PARTIAL")
	}
	if pattern.EvaluationFailures > 0 || free.EvaluationFailures > 0 {
		report.HealthFlags = append(report.HealthFlags, "LIVE_EVALUATION_FAILURES")
	}
	if free.TargetPatternGenerated != 0 || free.TargetPatternPenalty != 0 || free.PatternMasteryMutations != 0 {
		report.HealthFlags = append(report.HealthFlags, "FREE_PATTERN_SAFETY_FAILURE")
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil && filepath.Dir(*output) != "." {
		return err
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(*output, append(b, '\n'), 0644); err != nil {
		return err
	}
	fmt.Printf("V2.5 live report: %s\nProvider/model: %s / %s\nPattern generated/evaluated: %d/%d\nFree generated/evaluated: %d/%d\nHealth: %s\n", *output, provider.ID, provider.Model, pattern.Generated, pattern.Evaluated, free.Generated, free.Evaluated, strings.Join(report.HealthFlags, ", "))
	return nil
}

func v25LiveProvider() (ProviderConfig, error) {
	path := simulationProviderDBPath()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return ProviderConfig{}, err
	}
	defer db.Close()
	var c ProviderConfig
	var enabled int
	err = db.QueryRow(`SELECT id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled FROM llm_providers WHERE enabled=1 ORDER BY id LIMIT 1`).Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Model, &c.Timeout, &c.Temperature, &c.MaxTokens, &enabled)
	if err != nil {
		return ProviderConfig{}, fmt.Errorf("no enabled live provider: %w", err)
	}
	c.Enabled = enabled == 1
	return c, nil
}

func v25LiveModeRun(ctx context.Context, s *Server, provider ProviderConfig, n int, prefs PracticePreferences) (v25LiveMode, error) {
	out := v25LiveMode{Requested: n, Lower: fixedLower(prefs.FixedDifficulty), Upper: fixedUpper(prefs.FixedDifficulty), CenterStable: true}
	if n == 0 {
		return out, nil
	}
	session := id("v25-live-session")
	_, err := s.db.Exec(`INSERT INTO sessions(id,mode,started_at,end_global_difficulty,start_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count,scene_id,subscene_id,practice_scope,difficulty_mode,fixed_difficulty,training_focus) VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?,?,?,?)`, session, "scene", time.Now().UTC().Format(time.RFC3339), 4, 4, 4, out.Lower, out.Upper, 0, "", "", "global", prefs.DifficultyMode, prefs.FixedDifficulty, prefs.TrainingFocus)
	if err != nil {
		return out, err
	}
	var masteryBefore int
	_ = s.db.QueryRow("SELECT COALESCE(SUM(attempt_count),0) FROM learner_skill_state").Scan(&masteryBefore)
	for i := 0; i < n; i++ {
		ex, genErr := s.generateExerciseForScene(ctx, prefs.FixedDifficulty, "adaptive", "", "", session)
		if genErr != nil {
			continue
		}
		out.Generated++
		if generated, _ := ex["generated_by"].(string); generated == "fallback" {
			out.GenerationFallbacks++
		}
		if d, ok := ex["difficulty"].(float64); ok && (d < out.Lower || d > out.Upper) {
			out.OutOfBand++
		}
		if prefs.TrainingFocus == TrainingFocusPattern {
			if present, _ := ex["target_pattern_present"].(bool); present {
				out.TargetPatternGenerated++
			}
		} else if present, _ := ex["target_pattern_present"].(bool); present {
			out.TargetPatternGenerated++
		}
		exID, _ := ex["exercise_id"].(string)
		answer := "I would like to express this idea clearly in a natural way."
		if refs, ok := ex["reference_answers"].([]string); ok && len(refs) > 0 {
			answer = refs[0]
		}
		result, submitErr := s.submitAttempt(ctx, session, exID, answer)
		if submitErr != nil {
			out.EvaluationFailures++
			continue
		}
		out.Evaluated++
		if result != nil {
			out.ProviderEvaluations++
		}
	}
	if prefs.TrainingFocus == TrainingFocusFree {
		var masteryAfter int
		_ = s.db.QueryRow("SELECT COALESCE(SUM(attempt_count),0) FROM learner_skill_state").Scan(&masteryAfter)
		out.PatternMasteryMutations = masteryAfter - masteryBefore
	}
	return out, nil
}

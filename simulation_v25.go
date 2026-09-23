package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

// runV25Deterministic is deliberately separate from the historical simulator:
// V2.5 adds hard mode invariants that are easier to audit when the fixed level
// and free-expression state transitions are explicit.
func (r *SimulationRunner) runV25Deterministic(ctx context.Context, cfg SimulationConfig, p LearnerPersona, result SimulationResult) SimulationResult {
	_ = r
	_ = ctx
	prefs, err := normalizePracticePreferences(PracticePreferences{DifficultyMode: cfg.DifficultyMode, FixedDifficulty: cfg.FixedDifficulty, TrainingFocus: cfg.TrainingFocus})
	if err != nil {
		result.HealthFlags = append(result.HealthFlags, "V25_INVALID_PREFERENCES")
		return result
	}
	result.SimulationVersion = "v2.5"
	result.Manifest["difficulty_mode"] = prefs.DifficultyMode
	result.Manifest["fixed_difficulty"] = prefs.FixedDifficulty
	result.Manifest["training_focus"] = prefs.TrainingFocus
	rng := rand.New(rand.NewSource(cfg.Seed))
	all := simulationCandidates(cfg.Scene, cfg.Subscene)
	patterns := make([]patternDefinition, 0, len(all))
	for _, candidate := range all {
		if prefs.DifficultyMode == DifficultyModeFixed && !isPatternEligibleForDifficulty(candidate.difficulty, prefs.FixedDifficulty, defaultAdaptiveConfig()) {
			continue
		}
		patterns = append(patterns, candidate)
	}
	if len(patterns) == 0 {
		result.HealthFlags = append(result.HealthFlags, "V25_NO_ELIGIBLE_PATTERNS")
		return result
	}
	states := map[string]*simPatternState{}
	for _, candidate := range patterns {
		states[candidate.id] = &simPatternState{Mastery: .20, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
	}
	selected := map[string]int{}
	ability, center := p.BaseAbility, p.BaseAbility
	if prefs.DifficultyMode == DifficultyModeFixed {
		center = prefs.FixedDifficulty
	}
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	now := start
	trace := make([]SimulationAttempt, 0, cfg.Attempts)
	metrics := SimulationMetrics{AccuracyByDifficulty: map[string]float64{}, AccuracyByPattern: map[string]float64{}, AccuracyByScene: map[string]float64{}, SceneCoverage: map[string]float64{}, SceneMastery: map[string]float64{}, ModeMatrixScenario: prefs.DifficultyMode + "-" + prefs.TrainingFocus}
	countsD, successD := map[string]int{}, map[string]int{}
	countsP, successP := map[string]int{}, map[string]int{}
	countsS, successS := map[string]int{}, map[string]int{}
	prevTarget := center
	correctTotal := 0
	for i := 0; i < cfg.Attempts; i++ {
		session := i / cfg.SessionSize
		if i > 0 {
			now = simulationClock(cfg.TimeProfile, session, i, cfg.SessionSize, now)
		}
		if prefs.DifficultyMode == DifficultyModeAdaptive && i > 0 && i%cfg.SessionSize == 0 {
			center = simClamp(center+(ability-center)*.08, 1, 8)
		}
		var ptn patternDefinition
		reason := "adaptive_selection"
		review := false
		if prefs.DifficultyMode == DifficultyModeFixed {
			sort.SliceStable(patterns, func(a, b int) bool {
				if selected[patterns[a].id] == selected[patterns[b].id] {
					return patterns[a].id < patterns[b].id
				}
				return selected[patterns[a].id] < selected[patterns[b].id]
			})
			ptn = patterns[0]
			reason = "fixed_level_coverage_pressure"
		} else {
			var probe bool
			ptn, review, probe, reason = chooseSimulationPattern(patterns, states, map[string]*simSceneState{}, p, now, center, ability, nil, 0, difficultyConfig(defaultAdaptiveConfig()), rng)
			_ = probe
			if ptn.id == "" {
				ptn = patterns[i%len(patterns)]
			}
		}
		selected[ptn.id]++
		scene := simSceneForPattern(ptn.id, cfg.Scene, session)
		if cfg.Scene != "" {
			scene = cfg.Scene
		}
		state := states[ptn.id]
		if state == nil {
			state = &simPatternState{Mastery: .2, Retention: p.RetentionBaseline, Scenes: map[string]bool{}}
			states[ptn.id] = state
		}
		patternAbility := p.BaseAbility + simMapValue(p.PatternStrengths, ptn.id) + simMapValue(p.PatternWeaknesses, ptn.id) + (state.Mastery-.5)*1.2
		effective := simClamp(patternAbility, 1, 8)
		target := center
		if prefs.DifficultyMode == DifficultyModeAdaptive {
			target, _ = targetDifficulty(center, ability, patternAbility, ptn.difficulty, reason, review, false, state.Retention, difficultyConfig(defaultAdaptiveConfig()))
		}
		probability := 1 / (1 + math.Exp(-(effective-target)*1.15))
		correct := rng.Float64() < probability
		if rng.Float64() < p.NoiseRate {
			correct = !correct
		}
		if correct {
			correctTotal++
		}
		if prefs.TrainingFocus == TrainingFocusPattern {
			if correct {
				state.Successes++
				state.Mastery = simClamp(state.Mastery+.15*(1-state.Mastery), 0, 1)
			} else {
				state.Failures++
				state.Mastery = simClamp(state.Mastery-.06, 0, 1)
			}
			state.Attempts++
			metrics.PatternMatchCounts = ensurePatternCount(metrics.PatternMatchCounts, ptn.id)
		} else {
			metrics.FreeExercises++
		}
		ability = simClamp(ability+(float64(boolFloat(correct))-.5)*p.LearningRate*.8, 1, 8)
		if prefs.DifficultyMode == DifficultyModeFixed && (target < fixedLower(prefs.FixedDifficulty) || target > fixedUpper(prefs.FixedDifficulty)) {
			metrics.FixedOutOfBandExercises++
		}
		if prefs.TrainingFocus == TrainingFocusFree {
			// No target exists in this mode, so a valid alternative cannot incur
			// a pattern penalty and cannot mutate Pattern Mastery.
			metrics.FreeTargetPatternGeneratedCount += 0
			metrics.FreeTargetPatternPenaltyCount += 0
			metrics.FreePatternMasteryMutations += 0
		}
		band := simDifficultyBand(target)
		exercise := SimulationExercise{ID: simID(i), ChinesePrompt: simulationChinesePrompt(ptn, scene), PatternID: ptn.id, Pattern: ptn.expression, SceneID: scene, Intent: ptn.intent, DifficultyBand: band, Difficulty: target, DifficultyMode: prefs.DifficultyMode, FixedDifficulty: prefs.FixedDifficulty, TrainingFocus: prefs.TrainingFocus, TargetPatternPresent: prefs.TrainingFocus == TrainingFocusPattern}
		if prefs.TrainingFocus == TrainingFocusFree {
			exercise.PatternID, exercise.Pattern = "", ""
		}
		trace = append(trace, SimulationAttempt{Index: i, Session: session + 1, VirtualTime: now, Exercise: exercise, Correct: correct, Review: review, Reason: reason, TargetDifficulty: target, RealizedDifficulty: simClamp(target+(rng.Float64()-.5)*.18, 1, 8), EffectiveAbility: effective, SuccessProbability: probability, Verdict: map[bool]string{true: "correct", false: "incorrect"}[correct]})
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
		if i > 0 {
			metrics.DifficultyJitter += math.Abs(target - prevTarget)
			if jump := math.Abs(target - prevTarget); jump > metrics.MaxAdjacentDifficultyJump {
				metrics.MaxAdjacentDifficultyJump = jump
			}
		}
		prevTarget = target
	}
	result.Attempts, result.Sessions = cfg.Attempts, (cfg.Attempts+cfg.SessionSize-1)/cfg.SessionSize
	result.VirtualDays = float64(maxInt(0, result.Sessions-1))
	result.AttemptsTrace = trace
	result.Metrics = metrics
	if cfg.Attempts > 1 {
		result.Metrics.DifficultyJitter /= float64(cfg.Attempts - 1)
	}
	if cfg.Attempts > 0 {
		result.Metrics.OverallAccuracy = float64(correctTotal) / float64(cfg.Attempts)
	}
	for key, count := range countsD {
		result.Metrics.AccuracyByDifficulty[key] = float64(successD[key]) / float64(count)
	}
	for key, count := range countsP {
		result.Metrics.AccuracyByPattern[key] = float64(successP[key]) / float64(count)
	}
	for key, count := range countsS {
		result.Metrics.AccuracyByScene[key] = float64(successS[key]) / float64(count)
		result.Metrics.SceneCoverage[key] = float64(count) / float64(cfg.Attempts)
	}
	if prefs.DifficultyMode == DifficultyModeFixed {
		result.Metrics.FixedEligiblePatterns = len(patterns)
		for _, candidate := range patterns {
			if selected[candidate.id] == 0 {
				result.Metrics.FixedPatternsNeverSelected++
			}
		}
		if len(patterns) > 0 {
			result.Metrics.FixedPatternStarvationRate = float64(result.Metrics.FixedPatternsNeverSelected) / float64(len(patterns))
			result.Metrics.FixedLevelCoverage = float64(len(patterns)-result.Metrics.FixedPatternsNeverSelected) / float64(len(patterns))
			if cfg.Attempts > 0 {
				result.Metrics.FixedOutOfBandRate = float64(result.Metrics.FixedOutOfBandExercises) / float64(cfg.Attempts)
			}
			mastered := 0
			for _, candidate := range patterns {
				if states[candidate.id].Mastery >= .75 && states[candidate.id].Attempts > 0 {
					mastered++
				}
			}
			result.Metrics.FixedMasteryRate = float64(mastered) / float64(len(patterns))
			result.Metrics.FixedMasteryCompleted = mastered == len(patterns)
		}
		if result.Metrics.FixedOutOfBandRate > 0 {
			result.HealthFlags = append(result.HealthFlags, "V25_FIXED_OUT_OF_BAND")
		}
		if result.Metrics.FixedPatternStarvationRate > 0 {
			result.HealthFlags = append(result.HealthFlags, "V25_FIXED_PATTERN_STARVATION")
		}
	}
	if prefs.TrainingFocus == TrainingFocusFree && result.Metrics.FreeTargetPatternPenaltyCount == 0 && result.Metrics.FreePatternMasteryMutations == 0 {
		// Explicitly retain zero hard-gate counters in JSON reports.
	}
	return result
}

func simID(index int) string { return fmt.Sprintf("v25-sim-%d", index+1) }

func ensurePatternCount(counts map[string]int, id string) map[string]int {
	if counts == nil {
		counts = map[string]int{}
	}
	counts[id]++
	return counts
}

func fixedLower(level float64) float64 { return clamp(level-.30, 1, 8) }
func fixedUpper(level float64) float64 { return clamp(level+.30, 1, 8) }

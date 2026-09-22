package main

import "math"

// DifficultySimulationResult is the deterministic acceptance harness used by
// calibration tests and the report. It models policy behavior only; it never
// calls an LLM or mutates the learner database.
type DifficultySimulationResult struct {
	Persona                 string    `json:"persona"`
	Attempts                int       `json:"attempts"`
	AbilityStart            float64   `json:"ability_start"`
	AbilityEnd              float64   `json:"ability_end"`
	SessionCenterStart      float64   `json:"session_center_start"`
	SessionCenterEnd        float64   `json:"session_center_end"`
	DifficultyJitter        float64   `json:"difficulty_jitter"`
	ProductiveZoneRatio     float64   `json:"productive_zone_ratio"`
	MaxSingleStepChange     float64   `json:"max_single_step_change"`
	ProbeRatio              float64   `json:"probe_ratio"`
	ProbeRecoveryRate       float64   `json:"probe_recovery_rate"`
	AbilityTrajectory       []float64 `json:"ability_trajectory,omitempty"`
	SessionCenterTrajectory []float64 `json:"session_center_trajectory,omitempty"`
}

func runDifficultySimulation(persona string, attempts int, cfg AdaptiveConfig) DifficultySimulationResult {
	cfg = difficultyConfig(cfg)
	if attempts <= 0 {
		attempts = 500
	}
	ability, center := 5.0, 5.0
	switch persona {
	case "strong_but_uneven", "strong-uneven":
		ability, center = 6.2, 6.0
	case "noisy_learner", "noisy":
		ability, center = 4.8, 4.8
	case "fast_learner", "fast":
		ability, center = 3.0, 3.0
	case "struggling_learner", "struggling":
		ability, center = 3.2, 3.2
	case "stable_intermediate", "stable", "":
		ability, center = 5.0, 5.0
	}
	startAbility, startCenter := ability, center
	previous := center
	maxStep, jitter := 0.0, 0.0
	productive, probes, probeRecovered := 0, 0, 0
	rolling := []float64{}
	abilityTrajectory := make([]float64, 0, attempts)
	centerTrajectory := make([]float64, 0, attempts)
	for i := 0; i < attempts; i++ {
		patternAbility := center
		if persona == "strong_but_uneven" || persona == "strong-uneven" {
			if i%5 == 0 || i%7 == 0 {
				patternAbility = 4.3
			}
		}
		probe := false
		if i >= 10 && i%17 == 0 && float64(probes)/float64(i+1) < cfg.ProbeMaxRatio {
			probe = true
		}
		target, _ := targetDifficulty(center, ability, patternAbility, patternAbility, "current_level", false, probe, 0, cfg)
		if !probe && math.Abs(target-ability) <= .75 {
			productive++
		}
		if probe {
			probes++
		}
		// Deterministic bounded noise models real-use variation without a
		// random seed or provider dependency.
		noise := math.Sin(float64(i)*1.71+float64(len(persona))) * .08
		if persona == "noisy_learner" || persona == "noisy" {
			noise += math.Sin(float64(i)*.37) * .22
		}
		if persona == "struggling_learner" || persona == "struggling" {
			noise -= .10
		}
		if persona == "fast_learner" || persona == "fast" {
			noise += .08
		}
		performance := clamp(.80+(ability-target)*.16+noise, 0, 1)
		if probe {
			if performance >= cfg.DeadbandLow {
				probeRecovered++
			}
		} else {
			rolling = append(rolling, performance)
			if len(rolling) > cfg.RecentWindowSize {
				rolling = rolling[len(rolling)-cfg.RecentWindowSize:]
			}
			if len(rolling) >= 3 {
				avg := meanEWMA(rolling, cfg.EWMAAlpha)
				center, _ = boundedControllerStepWithEvidence(center, avg, cfg, cfg.MaxSessionCenterStep, len(rolling))
				ability, _ = boundedControllerStepWithEvidence(ability, avg, cfg, cfg.MaxAbilityStep, len(rolling))
			}
		}
		step := math.Abs(center - previous)
		if step > maxStep {
			maxStep = step
		}
		if i > 0 {
			jitter += math.Abs(target - previous)
		}
		previous = target
		abilityTrajectory = append(abilityTrajectory, ability)
		centerTrajectory = append(centerTrajectory, center)
	}
	if attempts > 1 {
		jitter /= float64(attempts - 1)
	}
	result := DifficultySimulationResult{Persona: persona, Attempts: attempts, AbilityStart: startAbility, AbilityEnd: ability, SessionCenterStart: startCenter, SessionCenterEnd: center, DifficultyJitter: jitter, ProductiveZoneRatio: float64(productive) / float64(attempts), MaxSingleStepChange: maxStep, AbilityTrajectory: abilityTrajectory, SessionCenterTrajectory: centerTrajectory}
	if attempts > 0 {
		result.ProbeRatio = float64(probes) / float64(attempts)
	}
	if probes > 0 {
		result.ProbeRecoveryRate = float64(probeRecovered) / float64(probes)
	}
	return result
}

func difficultySimulationReport(cfg AdaptiveConfig, attempts int) []DifficultySimulationResult {
	personas := []string{"stable_intermediate", "strong_but_uneven", "noisy_learner", "fast_learner", "struggling_learner"}
	out := make([]DifficultySimulationResult, 0, len(personas))
	for _, persona := range personas {
		out = append(out, runDifficultySimulation(persona, attempts, cfg))
	}
	return out
}

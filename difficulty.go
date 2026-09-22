package main

// Difficulty V2.2 keeps three values separate:
//   - learner ability: a slow-moving long-term estimate;
//   - session center: the stable center used by one practice session;
//   - exercise difficulty: a pattern-aware target and a validated realization.
//
// The policy is intentionally small, deterministic, and configurable.  This
// file owns the controller so selection, persistence, reports, and simulation
// use the same rules.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const difficultyPolicyVersion = "difficulty-v2.2"

type difficultyValidation struct {
	Target   float64            `json:"target_difficulty"`
	Realized float64            `json:"realized_difficulty"`
	Delta    float64            `json:"difficulty_delta"`
	Accepted bool               `json:"accepted"`
	Status   string             `json:"status"`
	Reason   string             `json:"reason"`
	Features map[string]float64 `json:"features,omitempty"`
}

type sessionDifficultyState struct {
	ID         string
	Center     float64
	Lower      float64
	Upper      float64
	Confidence float64
	Evidence   int
}

func difficultyConfig(cfg AdaptiveConfig) AdaptiveConfig {
	if cfg.DifficultyPolicyVersion == "" {
		cfg.DifficultyPolicyVersion = difficultyPolicyVersion
	}
	if cfg.DeadbandLow <= 0 {
		cfg.DeadbandLow = cfg.TargetSuccessMin
	}
	if cfg.DeadbandHigh <= 0 {
		cfg.DeadbandHigh = cfg.TargetSuccessMax
	}
	if cfg.MaxAbilityStep <= 0 {
		cfg.MaxAbilityStep = .12
	}
	if cfg.MaxSessionCenterStep <= 0 {
		cfg.MaxSessionCenterStep = .10
	}
	if cfg.MaxPatternTargetStep <= 0 {
		cfg.MaxPatternTargetStep = .20
	}
	if cfg.RecentWindowSize <= 0 {
		cfg.RecentWindowSize = 10
	}
	if cfg.EWMAAlpha <= 0 || cfg.EWMAAlpha > 1 {
		cfg.EWMAAlpha = .35
	}
	if cfg.SessionBandLower <= 0 {
		cfg.SessionBandLower = .45
	}
	if cfg.SessionBandUpper <= 0 {
		cfg.SessionBandUpper = .45
	}
	if cfg.ProbeMinRatio <= 0 {
		cfg.ProbeMinRatio = .05
	}
	if cfg.ProbeMaxRatio <= 0 {
		cfg.ProbeMaxRatio = .15
	}
	if cfg.ProbeDelta <= 0 {
		cfg.ProbeDelta = .40
	}
	if cfg.DifficultyMismatchThreshold <= 0 {
		cfg.DifficultyMismatchThreshold = .60
	}
	if cfg.MaxGeneratorRetries < 0 {
		cfg.MaxGeneratorRetries = 0
	}
	if cfg.MinimumProductiveChallenge <= 0 {
		cfg.MinimumProductiveChallenge = .35
	}
	return cfg
}

func sessionBand(center float64, cfg AdaptiveConfig) (float64, float64) {
	cfg = difficultyConfig(cfg)
	return clamp(center-cfg.SessionBandLower, 1, 8), clamp(center+cfg.SessionBandUpper, 1, 8)
}

func learnerAbility(db *sql.DB) float64 {
	var value float64
	if db.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&value) != nil || value <= 0 {
		return 3
	}
	return clamp(value, 1, 8)
}

func loadSessionDifficulty(db *sql.DB, sessionID string) (sessionDifficultyState, error) {
	if strings.TrimSpace(sessionID) == "" {
		ability := learnerAbility(db)
		lower, upper := sessionBand(ability, defaultAdaptiveConfig())
		return sessionDifficultyState{Center: ability, Lower: lower, Upper: upper}, nil
	}
	var state sessionDifficultyState
	state.ID = sessionID
	err := db.QueryRow(`SELECT session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count FROM sessions WHERE id=?`, sessionID).
		Scan(&state.Center, &state.Lower, &state.Upper, &state.Confidence, &state.Evidence)
	if err != nil {
		return state, err
	}
	ability := learnerAbility(db)
	if state.Center <= 0 {
		state.Center = ability
	}
	if state.Lower <= 0 || state.Upper <= 0 {
		state.Lower, state.Upper = sessionBand(state.Center, defaultAdaptiveConfig())
	}
	return state, nil
}

func ensureSessionDifficulty(db *sql.DB, sessionID string, mode string) (sessionDifficultyState, error) {
	ability := learnerAbility(db)
	cfg := difficultyConfig(defaultAdaptiveConfig())
	if state, err := loadSessionDifficulty(db, sessionID); err == nil {
		if state.Center > 0 {
			return state, nil
		}
	}
	center := ability
	lower, upper := sessionBand(center, cfg)
	_, err := db.Exec(`INSERT OR IGNORE INTO sessions(id,mode,started_at,start_global_difficulty,end_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count) VALUES(?,?,?,?,?,?,?,?,?,0)`, sessionID, mode, time.Now().UTC().Format(time.RFC3339), ability, ability, center, lower, upper, 0)
	if err != nil {
		return sessionDifficultyState{}, err
	}
	return sessionDifficultyState{ID: sessionID, Center: center, Lower: lower, Upper: upper}, nil
}

func targetDifficulty(center, ability, patternAbility, catalog float64, reason string, review, probe bool, retention float64, cfg AdaptiveConfig) (float64, string) {
	cfg = difficultyConfig(cfg)
	if center <= 0 {
		center = ability
	}
	if ability <= 0 {
		ability = center
	}
	if patternAbility <= 0 {
		patternAbility = catalog
	}
	if catalog <= 0 {
		catalog = patternAbility
	}
	target := .55*center + .25*ability + .15*patternAbility + .05*catalog
	adjustment := "session_center_weighted_pattern_target"
	switch reason {
	case "weak_skill":
		target = math.Max(patternAbility+cfg.MinimumProductiveChallenge, center-(cfg.SessionBandLower*.75))
		adjustment = "weak_skill_productive_challenge"
	case "scheduled_review", "retention_check":
		// Recovery is controlled; a long retention interval can add a small
		// challenge, but never bypasses the normal envelope by itself.
		target = .55*center + .35*patternAbility + .10*catalog
		if retention > cfg.RetentionThreshold {
			target += .10
		}
		adjustment = "review_retention_interval"
	case "new_skill":
		target = .65*center + .20*catalog + .15*patternAbility
		adjustment = "new_skill_session_anchor"
	}
	lower, upper := sessionBand(center, cfg)
	if probe {
		target = clamp(center+cfg.ProbeDelta, 1, 8)
		return target, "probe_isolated_from_session_envelope"
	}
	return clamp(target, lower, upper), adjustment
}

func patternTargetStep(previous, proposed, maxStep float64) float64 {
	if previous <= 0 {
		return clamp(proposed, 1, 8)
	}
	return clamp(previous+clamp(proposed-previous, -maxStep, maxStep), 1, 8)
}

func scoreFromEval(meaning, grammar, naturalness, pattern float64) float64 {
	return clamp(.35*meaning+.20*grammar+.20*naturalness+.25*pattern, 0, 1)
}

func scoreFromVerdict(verdict string) float64 {
	switch verdict {
	case "correct":
		return 1
	case "mostly_correct":
		return .78
	case "needs_improvement":
		return .45
	default:
		return 0
	}
}

func confidenceFromEvidence(n int) float64 {
	if n <= 0 {
		return 0
	}
	return clamp(1-math.Exp(-float64(n)/5), 0, 1)
}

func recentPerformanceTx(tx *sql.Tx, where string, args []any, limit int) ([]float64, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `SELECT v.meaning_score,v.grammar_score,v.naturalness_score,v.pattern_score,v.verdict FROM attempts a JOIN evaluations v ON v.attempt_id=a.id WHERE a.evaluation_status='validated' AND a.is_probe=0`
	if strings.TrimSpace(where) != "" {
		query += " AND " + where
	}
	query += " ORDER BY a.submitted_at DESC,a.id DESC LIMIT ?"
	args = append(append([]any(nil), args...), limit)
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []float64
	for rows.Next() {
		var meaning, grammar, naturalness, pattern float64
		var verdict string
		if err := rows.Scan(&meaning, &grammar, &naturalness, &pattern, &verdict); err != nil {
			return nil, err
		}
		if meaning == 0 && grammar == 0 && naturalness == 0 && pattern == 0 {
			out = append(out, scoreFromVerdict(verdict))
		} else {
			out = append(out, scoreFromEval(meaning, grammar, naturalness, pattern))
		}
	}
	return out, rows.Err()
}

func meanEWMA(values []float64, alpha float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if alpha <= 0 || alpha > 1 {
		alpha = .35
	}
	// Values are newest first; reverse so the EWMA follows time.
	ordered := append([]float64(nil), values...)
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	result := ordered[0]
	for _, value := range ordered[1:] {
		result = alpha*value + (1-alpha)*result
	}
	return result
}

func boundedControllerStep(current, performance float64, cfg AdaptiveConfig, maxStep float64) (float64, string) {
	return boundedControllerStepWithEvidence(current, performance, cfg, maxStep, 5)
}

func boundedControllerStepWithEvidence(current, performance float64, cfg AdaptiveConfig, maxStep float64, evidence int) (float64, string) {
	cfg = difficultyConfig(cfg)
	if performance >= cfg.DeadbandLow && performance <= cfg.DeadbandHigh {
		return current, "deadband_hold"
	}
	direction := 1.0
	reason := "sustained_high_performance"
	if performance < cfg.DeadbandLow {
		direction = -1
		reason = "sustained_low_performance"
	}
	confidence := confidenceFromEvidence(evidence)
	step := maxStep * (1 - .45*confidence)
	return clamp(current+direction*math.Min(maxStep, step), 1, 8), reason
}

func updateLearnerAbilityTx(tx *sql.Tx, cfg AdaptiveConfig) (float64, string, error) {
	var current float64
	if err := tx.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&current); err != nil {
		return 0, "", err
	}
	values, err := recentPerformanceTx(tx, "", nil, difficultyConfig(cfg).RecentWindowSize)
	if err != nil {
		return current, "", err
	}
	if len(values) < 3 {
		return current, "insufficient_rolling_evidence", nil
	}
	performance := meanEWMA(values, cfg.EWMAAlpha)
	next, reason := boundedControllerStepWithEvidence(current, performance, cfg, cfg.MaxAbilityStep, len(values))
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE user_profile SET global_difficulty=?,updated_at=? WHERE id='default'`, next, now); err != nil {
		return current, reason, err
	}
	_, err = tx.Exec(`INSERT INTO difficulty_state(scope,entity_id,difficulty,success_rate,attempts,empirical_difficulty,confidence,updated_at) VALUES('global','default',?,?,?,?,?,?) ON CONFLICT(scope,entity_id) DO UPDATE SET difficulty=excluded.difficulty,success_rate=excluded.success_rate,attempts=excluded.attempts,confidence=excluded.confidence,updated_at=excluded.updated_at`, next, performance, len(values), next, confidenceFromEvidence(len(values)), now)
	return next, reason, err
}

func updateSessionCenterTx(tx *sql.Tx, sessionID string, cfg AdaptiveConfig) (sessionDifficultyState, string, error) {
	var state sessionDifficultyState
	state.ID = sessionID
	var ability float64
	if err := tx.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&ability); err != nil {
		return state, "", err
	}
	err := tx.QueryRow(`SELECT session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count FROM sessions WHERE id=?`, sessionID).
		Scan(&state.Center, &state.Lower, &state.Upper, &state.Confidence, &state.Evidence)
	if err == sql.ErrNoRows {
		state.Center = ability
		state.Lower, state.Upper = sessionBand(state.Center, cfg)
		_, err = tx.Exec(`INSERT INTO sessions(id,mode,started_at,start_global_difficulty,end_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count) VALUES(?,?,?,?,?,?,?,?,?,0)`, sessionID, "adaptive", time.Now().UTC().Format(time.RFC3339), ability, ability, state.Center, state.Lower, state.Upper, 0)
	}
	if err != nil {
		return state, "", err
	}
	values, err := recentPerformanceTx(tx, "a.session_id=?", []any{sessionID}, difficultyConfig(cfg).RecentWindowSize)
	if err != nil {
		return state, "", err
	}
	state.Evidence = len(values)
	state.Confidence = confidenceFromEvidence(len(values))
	reason := "insufficient_rolling_evidence"
	if len(values) >= 3 {
		performance := meanEWMA(values, cfg.EWMAAlpha)
		next, stepReason := boundedControllerStepWithEvidence(state.Center, performance, cfg, cfg.MaxSessionCenterStep, len(values))
		if next != state.Center {
			reason = stepReason
		} else {
			reason = "deadband_hold"
		}
		state.Center = next
	}
	state.Lower, state.Upper = sessionBand(state.Center, cfg)
	_, err = tx.Exec(`UPDATE sessions SET session_difficulty_center=?,session_band_lower=?,session_band_upper=?,session_center_confidence=?,session_evidence_count=? WHERE id=?`, state.Center, state.Lower, state.Upper, state.Confidence, state.Evidence, sessionID)
	return state, reason, err
}

func loadSessionDifficultyTx(tx *sql.Tx, sessionID string) (sessionDifficultyState, error) {
	var state sessionDifficultyState
	state.ID = sessionID
	err := tx.QueryRow(`SELECT session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count FROM sessions WHERE id=?`, sessionID).
		Scan(&state.Center, &state.Lower, &state.Upper, &state.Confidence, &state.Evidence)
	return state, err
}

func validationStatusForAttempt(tx *sql.Tx, attempt string) string {
	var status string
	_ = tx.QueryRow(`SELECT COALESCE(difficulty_validation_status,'unknown') FROM attempts WHERE id=?`, attempt).Scan(&status)
	if status == "" {
		status = "unknown"
	}
	return status
}

func shouldServeProbe(db *sql.DB, cfg AdaptiveConfig) bool {
	cfg = difficultyConfig(cfg)
	var attempts, probes int
	_ = db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE evaluation_status='validated'`).Scan(&attempts)
	_ = db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE evaluation_status='validated' AND is_probe=1`).Scan(&probes)
	if attempts == 0 {
		return false
	}
	ratio := float64(probes) / float64(attempts)
	if ratio >= cfg.ProbeMaxRatio {
		return false
	}
	// Keep a deterministic floor while allowing stable sessions to use the
	// configured range instead of a fixed every-N rule.
	if ratio < cfg.ProbeMinRatio {
		return attempts >= 4
	}
	return attempts%int(math.Max(1, math.Round(1/cfg.ProbeMaxRatio))) == 0
}

func validateExerciseDifficulty(target, realized float64, cfg AdaptiveConfig) difficultyValidation {
	cfg = difficultyConfig(cfg)
	if target <= 0 {
		target = realized
	}
	if realized <= 0 {
		return difficultyValidation{Target: target, Realized: realized, Delta: math.Abs(target - realized), Status: "rejected", Accepted: false, Reason: "missing_realized_difficulty"}
	}
	delta := math.Abs(realized - target)
	validation := difficultyValidation{Target: target, Realized: realized, Delta: delta, Features: map[string]float64{}}
	if delta > cfg.DifficultyMismatchThreshold {
		validation.Status = "difficulty_validation_failed"
		validation.Reason = fmt.Sprintf("realized difficulty %.2f is outside target %.2f ± %.2f", realized, target, cfg.DifficultyMismatchThreshold)
		return validation
	}
	validation.Accepted = true
	validation.Status = "validated"
	validation.Reason = "within_configured_difficulty_band"
	return validation
}

// validateExerciseHeuristics is deliberately local and explainable. Providers
// may report an estimate, but the validator still records the features used by
// a deterministic fallback and can be extended without an NLP dependency.
func validateExerciseHeuristics(text, pattern string, target float64) (float64, map[string]float64) {
	text = strings.TrimSpace(text)
	words := 0
	if text != "" {
		words = len(strings.Fields(text))
	}
	clauses := 1 + strings.Count(text, ",") + strings.Count(strings.ToLower(text), " because ") + strings.Count(strings.ToLower(text), " if ")
	features := map[string]float64{
		"sentence_length": float64(words), "clause_count": float64(clauses),
		"pattern_complexity": 1, "tense_aspect_complexity": 1,
		"conditional_structures": 0, "modal_complexity": 0,
		"semantic_relations": float64(clauses), "lexical_complexity": float64(words),
		"register": 1, "required_information_units": float64(words),
	}
	lower := strings.ToLower(text + " " + pattern)
	if strings.Contains(lower, "conditional") || strings.Contains(lower, " if ") {
		features["conditional_structures"] = 1
		features["pattern_complexity"] += 1
	}
	if strings.Contains(lower, "perfect") || strings.Contains(lower, "had ") || strings.Contains(lower, "would have") {
		features["tense_aspect_complexity"] += 1
	}
	if strings.Contains(lower, "might") || strings.Contains(lower, "must") || strings.Contains(lower, "could") {
		features["modal_complexity"] = 1
	}
	if strings.Contains(lower, "please") || strings.Contains(lower, "would it be") {
		features["register"] = 2
	}
	// This is a sanity estimate, not a replacement for a provider estimate.
	if target <= 0 {
		target = 3
	}
	bonus := .04*float64(maxInt(0, words-8)) + .12*float64(clauses-1) + .15*features["conditional_structures"] + .10*features["modal_complexity"]
	return clamp(target+bonus, 1, 8), features
}

func recordDifficultyValidation(db *sql.DB, exerciseID string, result difficultyValidation, retry int) {
	_, _ = db.Exec(`INSERT INTO difficulty_validation_events(id,exercise_id,target_difficulty,realized_difficulty,difficulty_delta,status,reason,retry_count,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, id("difficulty_validation"), exerciseID, result.Target, result.Realized, result.Delta, result.Status, result.Reason, retry, time.Now().UTC().Format(time.RFC3339))
}

func difficultyPolicyTrace(ability float64, session sessionDifficultyState, patternAbility, target, realized float64, reason, adjustment string, probe bool, cfg AdaptiveConfig) map[string]any {
	return map[string]any{
		"difficulty_policy_version": difficultyConfig(cfg).DifficultyPolicyVersion,
		"learner_ability":           ability, "session_center": session.Center, "session_band_lower": session.Lower, "session_band_upper": session.Upper,
		"pattern_ability": patternAbility, "target_difficulty": target, "realized_difficulty": realized,
		"difficulty_delta": math.Abs(realized - target), "selection_reason": reason, "is_probe": probe,
		"difficulty_adjustment_reason": adjustment,
	}
}

func sortedFloatPercentile(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	copyValues := append([]float64(nil), values...)
	sort.Float64s(copyValues)
	index := int(math.Round(percentile * float64(len(copyValues)-1)))
	return copyValues[clampInt(index, 0, len(copyValues)-1)]
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func difficultyConfigMap(cfg AdaptiveConfig) map[string]any {
	b, _ := json.Marshal(difficultyConfig(cfg))
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func difficultySentenceLength(target float64) string {
	switch {
	case target < 2.5:
		return "6-10 words"
	case target < 4:
		return "9-15 words"
	case target < 5.5:
		return "12-20 words"
	default:
		return "16-26 words"
	}
}

func difficultyGrammarComplexity(pattern string) string {
	pattern = strings.ToLower(pattern)
	if strings.Contains(pattern, "conditional") || strings.Contains(pattern, "perfect") || strings.Contains(pattern, "passive") {
		return "multi-clause or advanced tense as required by pattern"
	}
	return "single main clause with one controlled pattern"
}

func difficultyMaxClauses(target float64) int {
	switch {
	case target < 3:
		return 1
	case target < 5:
		return 2
	default:
		return 3
	}
}

func difficultyLexicalComplexity(target float64) string {
	switch {
	case target < 3:
		return "common everyday vocabulary"
	case target < 5:
		return "common vocabulary with one functional phrase"
	default:
		return "precise adult vocabulary with controlled register"
	}
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	DifficultyModeAdaptive = "adaptive"
	DifficultyModeFixed    = "fixed"
	TrainingFocusPattern   = "pattern"
	TrainingFocusFree      = "free_expression"
	TargetPatternNA        = "not_applicable"
)

// PracticePreferences is the single source of truth for the two user-facing
// practice controls. Empty/omitted values are normalized to the historical
// Adaptive + Sentence Pattern behavior.
type PracticePreferences struct {
	DifficultyMode       string  `json:"difficulty_mode"`
	FixedDifficulty      float64 `json:"fixed_difficulty,omitempty"`
	TrainingFocus        string  `json:"training_focus"`
	TargetPatternEnabled bool    `json:"target_pattern_enabled"`
}

func defaultPracticePreferences() PracticePreferences {
	return PracticePreferences{DifficultyMode: DifficultyModeAdaptive, TrainingFocus: TrainingFocusPattern, TargetPatternEnabled: true}
}

func normalizePracticePreferences(p PracticePreferences) (PracticePreferences, error) {
	out := p
	out.DifficultyMode = strings.ToLower(strings.TrimSpace(out.DifficultyMode))
	out.TrainingFocus = strings.ToLower(strings.TrimSpace(out.TrainingFocus))
	if out.DifficultyMode == "" {
		out.DifficultyMode = DifficultyModeAdaptive
	}
	if out.TrainingFocus == "" {
		out.TrainingFocus = TrainingFocusPattern
	}
	if out.DifficultyMode != DifficultyModeAdaptive && out.DifficultyMode != DifficultyModeFixed {
		return PracticePreferences{}, fmt.Errorf("difficulty_mode must be adaptive or fixed")
	}
	if out.TrainingFocus != TrainingFocusPattern && out.TrainingFocus != TrainingFocusFree {
		return PracticePreferences{}, fmt.Errorf("training_focus must be pattern or free_expression")
	}
	if out.DifficultyMode == DifficultyModeAdaptive {
		out.FixedDifficulty = 0
	} else {
		if out.FixedDifficulty < 1 || out.FixedDifficulty > 8 || math.IsNaN(out.FixedDifficulty) || math.IsInf(out.FixedDifficulty, 0) {
			return PracticePreferences{}, fmt.Errorf("fixed_difficulty must be between 1 and 8")
		}
		out.FixedDifficulty = math.Round(out.FixedDifficulty*10) / 10
	}
	out.TargetPatternEnabled = out.TrainingFocus == TrainingFocusPattern
	return out, nil
}

func practicePreferencesFromSession(db *sql.DB, sessionID string) (PracticePreferences, error) {
	p := defaultPracticePreferences()
	if strings.TrimSpace(sessionID) == "" {
		return p, nil
	}
	var mode, focus string
	var fixed float64
	if err := db.QueryRow(`SELECT COALESCE(difficulty_mode,'adaptive'),COALESCE(fixed_difficulty,0),COALESCE(training_focus,'pattern') FROM sessions WHERE id=?`, sessionID).Scan(&mode, &fixed, &focus); err != nil {
		return p, err
	}
	p.DifficultyMode, p.FixedDifficulty, p.TrainingFocus = mode, fixed, focus
	return normalizePracticePreferences(p)
}

func writePracticePreferences(db *sql.DB, sessionID string, p PracticePreferences) error {
	p, err := normalizePracticePreferences(p)
	if err != nil {
		return err
	}
	if p.DifficultyMode == DifficultyModeFixed {
		lower, upper := fixedDifficultyBand(p.FixedDifficulty, defaultAdaptiveConfig())
		_, err = db.Exec(`UPDATE sessions SET difficulty_mode=?,fixed_difficulty=?,training_focus=?,session_difficulty_center=?,session_band_lower=?,session_band_upper=? WHERE id=?`, p.DifficultyMode, p.FixedDifficulty, p.TrainingFocus, p.FixedDifficulty, lower, upper, sessionID)
		return err
	}
	var ability float64
	_ = db.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&ability)
	if ability <= 0 {
		ability = 3
	}
	lower, upper := sessionBand(ability, defaultAdaptiveConfig())
	_, err = db.Exec(`UPDATE sessions SET difficulty_mode=?,fixed_difficulty=0,training_focus=?,session_difficulty_center=?,session_band_lower=?,session_band_upper=? WHERE id=?`, p.DifficultyMode, p.TrainingFocus, ability, lower, upper, sessionID)
	return err
}

func fixedDifficultyBand(level float64, cfg AdaptiveConfig) (float64, float64) {
	cfg = difficultyConfig(cfg)
	width := cfg.FixedDifficultyBand
	if width <= 0 {
		width = .30
	}
	return clamp(level-width, 1, 8), clamp(level+width, 1, 8)
}

// IsPatternEligibleForDifficulty uses the catalog difficulty, not a learner's
// mutable empirical difficulty. This keeps a fixed level stable while still
// allowing patterns at band edges to overlap a level.
func IsPatternEligibleForDifficulty(patternDifficulty, selectedLevel float64) bool {
	lower, upper := fixedDifficultyBand(selectedLevel, defaultAdaptiveConfig())
	return patternDifficulty >= lower-1e-9 && patternDifficulty <= upper+1e-9
}

func isPatternEligibleForDifficulty(patternDifficulty, selectedLevel float64, cfg AdaptiveConfig) bool {
	lower, upper := fixedDifficultyBand(selectedLevel, cfg)
	return patternDifficulty >= lower-1e-9 && patternDifficulty <= upper+1e-9
}

func difficultyLevelLabel(level float64) string {
	return fmt.Sprintf("D%d", int(math.Round(level)))
}

type fixedPatternRow struct {
	ID, Pattern, Intent, Skill, State string
	Difficulty, Mastery, Retention    float64
	Attempts, Correct                 int
}

func (s *Server) fixedSelect(level float64, mode, scene string) (adaptiveCandidate, error) {
	cfg := s.adaptiveConfig()
	lower, upper := fixedDifficultyBand(level, cfg)
	constraint, err := s.sceneConstraint(scene)
	if err != nil {
		return adaptiveCandidate{}, err
	}
	var recent []string
	rr, _ := s.db.Query(`SELECT e.pattern_id FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' AND e.pattern_id<>'' ORDER BY a.submitted_at DESC,a.id DESC LIMIT ?`, cfg.RecentPatternWindow)
	if rr != nil {
		for rr.Next() {
			var id string
			_ = rr.Scan(&id)
			recent = append(recent, id)
		}
		rr.Close()
	}
	recentCounts := map[string]int{}
	for _, id := range recent {
		recentCounts[id]++
	}
	due := map[string]float64{}
	dr, _ := s.db.Query(`SELECT pattern_id,priority FROM review_schedule WHERE due_at<=?`, time.Now().UTC().Format(time.RFC3339))
	if dr != nil {
		for dr.Next() {
			var id string
			var priority float64
			_ = dr.Scan(&id, &priority)
			due[id] = priority
		}
		dr.Close()
	}
	ability := learnerAbility(s.db)
	rows, err := s.db.Query(`SELECT p.id,p.pattern,i.id,COALESCE(ps.skill_id,''),COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(ls.mastery,COALESCE(pm.mastery,.25)),COALESCE(ls.retention,0),COALESCE(ls.attempt_count,0),COALESCE(ls.success_count,0),COALESCE(ls.state,'') FROM sentence_patterns p JOIN communication_intents i ON i.id=p.intent_id LEFT JOIN (SELECT pattern_id,MIN(skill_id) AS skill_id FROM pattern_skills GROUP BY pattern_id) ps ON ps.pattern_id=p.id LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' LEFT JOIN pattern_mastery pm ON pm.pattern_id=p.id WHERE COALESCE(p.catalog_difficulty,p.difficulty)>=? AND COALESCE(p.catalog_difficulty,p.difficulty)<=?`, lower, upper)
	if err != nil {
		return adaptiveCandidate{}, err
	}
	defer rows.Close()
	var candidates []adaptiveCandidate
	for rows.Next() {
		var x fixedPatternRow
		if err := rows.Scan(&x.ID, &x.Pattern, &x.Intent, &x.Skill, &x.Difficulty, &x.Mastery, &x.Retention, &x.Attempts, &x.Correct, &x.State); err != nil {
			return adaptiveCandidate{}, err
		}
		if len(constraint.PatternIDs) > 0 && !constraint.PatternIDs[x.ID] {
			continue
		}
		if mode == "weak" && (x.Attempts == 0 || x.Mastery >= cfg.WeakSkillThreshold) {
			continue
		}
		if mode == "review" && due[x.ID] <= 0 {
			continue
		}
		state := stateFromRow(x.Attempts, x.Correct, x.Mastery, x.State, cfg)
		weak := 1 - x.Mastery
		if x.Attempts == 0 {
			weak = 0 // UNKNOWN is not WEAK.
			state = stateUnknown
		}
		coveragePressure := 0.0
		if x.Attempts == 0 {
			coveragePressure = 4.0
		}
		repeatPenalty := .35 * float64(recentCounts[x.ID])
		if len(recent) > 0 && recent[0] == x.ID {
			repeatPenalty += 1.5
		}
		review := due[x.ID] > 0
		score := coveragePressure + 1.2*weak + 1.5*due[x.ID] - repeatPenalty
		if state == stateMastered {
			score -= .35
		}
		c := adaptiveCandidate{ID: x.ID, Pattern: x.Pattern, Intent: x.Intent, Skill: x.Skill, Difficulty: level, CatalogDifficulty: x.Difficulty, PatternAbility: x.Difficulty, Mastery: x.Mastery, Retention: x.Retention, Attempts: x.Attempts, Correct: x.Correct, State: state, Score: score, Review: review, New: x.Attempts == 0, ReviewTiming: map[bool]string{true: "overdue", false: ""}[review]}
		c.Reason = "fixed_level_coverage"
		if review {
			c.Reason = "fixed_level_review_due"
		}
		c.DecisionTrace = map[string]any{"difficulty_mode": DifficultyModeFixed, "fixed_difficulty": level, "fixed_band_lower": lower, "fixed_band_upper": upper, "difficulty_eligibility": true, "coverage_pressure": coveragePressure, "weakness_score": weak, "review_urgency": due[x.ID], "repeat_penalty": repeatPenalty, "candidate_score": score, "target_difficulty": level}
		c.SceneID, c.SubsceneID = constraint.RootID, constraint.SubsceneID
		c.ScopeRelaxed, c.RelaxationReason = constraint.ScopeRelaxed, constraint.RelaxationReason
		c.SessionCenter, c.TargetDifficulty, c.LearnerAbility = level, level, ability
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return adaptiveCandidate{}, err
	}
	if len(candidates) == 0 {
		return adaptiveCandidate{}, fmt.Errorf("no patterns eligible for %s", difficultyLevelLabel(level))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score == candidates[j].Score {
			return candidates[i].ID < candidates[j].ID
		}
		return candidates[i].Score > candidates[j].Score
	})
	return candidates[0], nil
}

func freeExpressionPrompt(intent, scene string, index int) string {
	key := strings.ToLower(strings.TrimSpace(intent))
	prompts := map[string][]string{
		"disagreement": {"请在讨论中礼貌地表达不同意见，并说明一个理由。", "请对一个提案表示保留，同时保持友好。"},
		"request":      {"请在这个场景中礼貌地提出一个实际请求。", "请向对方提出一个清晰、自然的请求。"},
		"condition":    {"请说明一个条件和在该条件下会采取的行动。", "请表达一个假设情况以及可能的结果。"},
		"opinion":      {"请对这个情况表达一个谨慎而有依据的看法。", "请自然地说明你对这个决定的观点。"},
		"contrast":     {"请表达两个方面的对比，并说明你的结论。", "请承认一个观点后，自然地补充你的不同看法。"},
	}
	if list := prompts[key]; len(list) > 0 {
		return list[index%len(list)]
	}
	if scene != "" {
		return fmt.Sprintf("请在%s场景中自然表达这个意思，并让对方理解你的沟通目的。", scene)
	}
	return "请用自然英语表达这个意思，并清楚完成沟通目的。"
}

func (s *Server) generateFreeAIExercise(ctx context.Context, c adaptiveCandidate, scene string, recent []string) (exerciseSeed, string, float64) {
	s.llm.mu.RLock()
	var pc ProviderConfig
	for _, x := range s.llm.configs {
		if x.Enabled {
			pc = x
			break
		}
	}
	s.llm.mu.RUnlock()
	if pc.ID == "" {
		return exerciseSeed{}, "", 0
	}
	lower, upper := fixedDifficultyBand(c.SessionCenter, s.adaptiveConfig())
	if v, ok := c.DecisionTrace["fixed_band_lower"].(float64); ok {
		lower = v
	}
	if v, ok := c.DecisionTrace["fixed_band_upper"].(float64); ok {
		upper = v
	}
	sys := `Generate one open-ended English practice exercise as strict JSON only. Fields: chinese_prompt, scene, intent, estimated_difficulty, reference_answers, alternative_answers. The task must have one clear communication intent and multiple valid natural English answers. There is no required target sentence pattern: do not name, imply, or secretly enforce a sentence pattern. Do not include target_pattern or pattern_id fields. Do not reveal an English answer in chinese_prompt.`
	user := fmt.Sprintf("scene=%s intent=%s target_difficulty=%.1f allowed_difficulty_range=%.1f-%.1f sentence_length_target=%s lexical_complexity=%s recent_prompts=%s", scene, c.Intent, c.SessionCenter, lower, upper, difficultySentenceLength(c.SessionCenter), difficultyLexicalComplexity(c.SessionCenter), strings.Join(recent, " | "))
	r, err := s.llm.Client(pc).Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: sys}, {Role: "user", Content: user}}, Temperature: pc.Temperature, MaxTokens: maxInt(pc.MaxTokens, 500), JSONMode: true})
	if err != nil || r == nil {
		return exerciseSeed{}, "", 0
	}
	var out struct {
		Prompt       string   `json:"chinese_prompt"`
		Difficulty   float64  `json:"estimated_difficulty"`
		Answers      []string `json:"reference_answers"`
		Alternatives []string `json:"alternative_answers"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(r.Content)), &out) != nil || strings.TrimSpace(out.Prompt) == "" {
		return exerciseSeed{}, "", 0
	}
	answers := append([]string{}, out.Answers...)
	answers = append(answers, out.Alternatives...)
	if out.Difficulty <= 0 {
		out.Difficulty = c.SessionCenter
	}
	return exerciseSeed{Prompt: out.Prompt, Answers: answers}, "provider", out.Difficulty
}

func (s *Server) generateFreeExerciseForScene(ctx context.Context, c adaptiveCandidate, scene string, subscene string, prefs PracticePreferences) (map[string]any, error) {
	if scene == "" {
		scene = c.SceneID
	}
	if scene == "" {
		scene = "daily"
	}
	if subscene == "" {
		subscene = c.SubsceneID
	}
	var recent []string
	rows, _ := s.db.Query(`SELECT e.chinese_prompt FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' AND e.training_focus='free_expression' ORDER BY a.submitted_at DESC LIMIT 5`)
	if rows != nil {
		for rows.Next() {
			var prompt string
			_ = rows.Scan(&prompt)
			recent = append(recent, prompt)
		}
		_ = rows.Close()
	}
	seed, generatedBy, realized := s.generateFreeAIExercise(ctx, c, scene, recent)
	if seed.Prompt == "" {
		seed = exerciseSeed{Prompt: freeExpressionPrompt(c.Intent, scene, len(recent)), Answers: nil}
		generatedBy = "fallback"
		realized = c.SessionCenter
	}
	if realized <= 0 || math.Abs(realized-c.SessionCenter) > s.adaptiveConfig().DifficultyMismatchThreshold {
		realized = c.SessionCenter
	}
	hash := normalizeChineseHash(seed.Prompt)
	metaMap := map[string]any{
		"mode": "free_expression", "difficulty_mode": prefs.DifficultyMode, "fixed_difficulty": prefs.FixedDifficulty,
		"training_focus": TrainingFocusFree, "target_pattern_mode": TargetPatternNA, "target_pattern_id": nil,
		"selection_reason": c.Reason, "generated_by": generatedBy, "reference_answers": seed.Answers,
		"target_difficulty": c.SessionCenter, "realized_difficulty": realized, "difficulty_delta": math.Abs(realized - c.SessionCenter),
		"difficulty_validation_status": "validated", "difficulty_validation_reason": "within_configured_difficulty_band",
		"difficulty_policy_version": difficultyConfig(s.adaptiveConfig()).DifficultyPolicyVersion, "normalized_chinese_hash": hash,
		"scene_id": scene, "subscene_id": subscene, "intent_id": c.Intent, "decision_trace_version": 4,
		"decision_trace": map[string]any{"difficulty_mode": prefs.DifficultyMode, "fixed_difficulty": prefs.FixedDifficulty, "training_focus": TrainingFocusFree, "target_pattern_present": false, "session_center": c.SessionCenter, "fixed_band_lower": c.DecisionTrace["fixed_band_lower"], "fixed_band_upper": c.DecisionTrace["fixed_band_upper"]},
	}
	meta, _ := json.Marshal(metaMap)
	trace, _ := json.Marshal(metaMap["decision_trace"])
	exID := id("exercise")
	_, err := s.db.Exec(`INSERT INTO exercises(id,chinese_prompt,pattern_id,scene_id,subscene_id,intent_id,difficulty,target_difficulty,realized_difficulty,difficulty_delta,difficulty_validation_status,difficulty_validation_reason,difficulty_policy_version,metadata_json,created_at,normalized_chinese_hash,generated_by,decision_trace_json,training_focus,difficulty_mode,fixed_difficulty,target_pattern_present) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, exID, seed.Prompt, "", scene, subscene, c.Intent, c.SessionCenter, c.SessionCenter, realized, math.Abs(realized-c.SessionCenter), "validated", "within_configured_difficulty_band", difficultyConfig(s.adaptiveConfig()).DifficultyPolicyVersion, string(meta), time.Now().UTC().Format(time.RFC3339), hash, generatedBy, string(trace), TrainingFocusFree, prefs.DifficultyMode, prefs.FixedDifficulty, 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"exercise_id": exID, "chinese_prompt": seed.Prompt, "target_pattern": nil, "pattern_id": nil, "scene_id": scene, "subscene_id": subscene, "communication_intent": c.Intent, "difficulty": c.SessionCenter, "target_difficulty": c.SessionCenter, "realized_difficulty": realized, "difficulty_delta": math.Abs(realized - c.SessionCenter), "difficulty_validation_status": "validated", "selection_reason": c.Reason, "difficulty_mode": prefs.DifficultyMode, "fixed_difficulty": prefs.FixedDifficulty, "training_focus": TrainingFocusFree, "target_pattern_enabled": false, "target_pattern_present": false, "reference_answers": seed.Answers, "decision_trace": metaMap["decision_trace"]}, nil
}

func (s *Server) fixedDifficultyMastery(level float64) map[string]any {
	cfg := s.adaptiveConfig()
	lower, upper := fixedDifficultyBand(level, cfg)
	rows, err := s.db.Query(`SELECT p.id,COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(ls.mastery,COALESCE(pm.mastery,.25)),COALESCE(ls.attempt_count,0),COALESCE(ls.state,'UNKNOWN'),COALESCE(ls.next_review_at,'') FROM sentence_patterns p LEFT JOIN pattern_mastery pm ON pm.pattern_id=p.id LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' WHERE COALESCE(p.catalog_difficulty,p.difficulty)>=? AND COALESCE(p.catalog_difficulty,p.difficulty)<=? ORDER BY p.id`, lower, upper)
	if err != nil {
		return map[string]any{"level": level, "label": difficultyLevelLabel(level), "lower": lower, "upper": upper, "eligible": 0, "error": err.Error()}
	}
	defer rows.Close()
	counts := map[string]int{"UNSEEN": 0, "LEARNING": 0, "WEAK": 0, "REVIEW_DUE": 0, "MASTERED": 0}
	eligible, seen, mastered := 0, 0, 0
	now := time.Now().UTC()
	for rows.Next() {
		var id, state, next string
		var difficulty, mastery float64
		var attempts int
		if rows.Scan(&id, &difficulty, &mastery, &attempts, &state, &next) != nil {
			continue
		}
		eligible++
		if attempts > 0 {
			seen++
		}
		if attempts == 0 {
			state = stateUnknown
		} else {
			state = stateFromRow(attempts, 0, mastery, state, cfg)
		}
		if next != "" {
			if parsed, parseErr := time.Parse(time.RFC3339, next); parseErr == nil && !parsed.After(now) && state == stateMastered {
				state = "REVIEW_DUE"
			}
		}
		if _, ok := counts[state]; !ok {
			state = "LEARNING"
		}
		counts[state]++
		if state == stateMastered {
			mastered++
		}
	}
	coverage := 0.0
	masteryRate := 0.0
	if eligible > 0 {
		coverage = float64(seen) / float64(eligible)
		masteryRate = float64(mastered) / float64(eligible)
	}
	return map[string]any{"level": level, "label": difficultyLevelLabel(level), "lower": lower, "upper": upper, "eligible": eligible, "seen": seen, "unseen": counts["UNSEEN"], "learning": counts["LEARNING"], "weak": counts["WEAK"], "review_due": counts["REVIEW_DUE"], "mastered": mastered, "coverage_rate": coverage, "mastery_rate": masteryRate, "completed": eligible > 0 && mastered == eligible, "states": counts}
}

func validatePracticePreferences(p PracticePreferences) error {
	_, err := normalizePracticePreferences(p)
	return err
}

var errInvalidPracticePreferences = errors.New("invalid practice preferences")

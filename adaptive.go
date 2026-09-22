package main

// Adaptive Learning Engine V2.  The policy in this file is deliberately
// deterministic and explainable.  Providers generate language, while this
// package owns eligibility, scheduling, difficulty, and state transitions.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type AdaptiveConfig struct {
	CurrentZoneWeight   float64 `json:"current_zone_weight"`
	WeakWeight          float64 `json:"weak_weight"`
	ReviewWeight        float64 `json:"review_weight"`
	MaintenanceWeight   float64 `json:"maintenance_weight"`
	ProbeWeight         float64 `json:"probe_weight"`
	RecentPatternWindow int     `json:"recent_pattern_window"`
	MaxPatternRepeats   int     `json:"max_pattern_repeats"`
	TargetSuccessMin    float64 `json:"target_success_min"`
	TargetSuccessMax    float64 `json:"target_success_max"`
	ProbeRatio          float64 `json:"probe_ratio"`
	NewSkillRatio       float64 `json:"new_skill_ratio"`
	WeakSkillThreshold  float64 `json:"weak_skill_threshold"`
	MasteryThreshold    float64 `json:"mastery_threshold"`
	RetentionThreshold  float64 `json:"retention_threshold"`
	FailureShortMinutes int     `json:"failure_short_minutes"`
	FailureRepeatHours  int     `json:"failure_repeat_hours"`
	SuccessFirstDays    int     `json:"success_first_days"`
	SuccessTwoDays      int     `json:"success_two_days"`
	SuccessThreeDays    int     `json:"success_three_days"`
	SuccessFiveDays     int     `json:"success_five_days"`
	SuccessLongDays     int     `json:"success_long_days"`
}

func defaultAdaptiveConfig() AdaptiveConfig {
	return AdaptiveConfig{CurrentZoneWeight: .40, WeakWeight: .25, ReviewWeight: .15, MaintenanceWeight: .10, ProbeWeight: .10, RecentPatternWindow: 5, MaxPatternRepeats: 2, TargetSuccessMin: .70, TargetSuccessMax: .85, ProbeRatio: .10, NewSkillRatio: .15, WeakSkillThreshold: .55, MasteryThreshold: .75, RetentionThreshold: .65, FailureShortMinutes: 10, FailureRepeatHours: 2, SuccessFirstDays: 1, SuccessTwoDays: 3, SuccessThreeDays: 7, SuccessFiveDays: 14, SuccessLongDays: 30}
}

func seedAdaptiveData(db *sql.DB) error {
	skills := []struct {
		id, n, d string
		level    int
	}{
		{"foundations", "Communication foundations", "Simple identity, possession, and description", 1},
		{"preferences", "Preferences", "Likes, wants, and personal choices", 1},
		{"ability", "Ability", "Can and cannot express ability", 1},
		{"daily_life", "Daily life", "Routines, activities, and everyday needs", 1},
		{"questions", "Questions", "Ask clear everyday questions", 1},
		{"advice", "Advice", "Give practical advice", 1},
		{"obligation", "Obligation", "Express duties and responsibilities", 1},
		{"experience", "Experience", "Talk about life experience", 2},
		{"suggestions", "Suggestions", "Make collaborative suggestions", 1},
		{"comparison", "Comparison", "Compare choices and options", 2},
		{"contrast", "Contrast", "Connect contrasting ideas", 2},
		{"reporting", "Reporting", "Report information from other people", 3},
		{"passive", "Passive voice", "Describe processes and events", 3},
		{"perfect_modals", "Perfect modals", "Reflect on past possibilities and duties", 3},
		{"professional", "Professional communication", "Meetings, negotiation, and clarification", 3},
		{"nuance", "Nuanced opinions", "Hedge and qualify opinions", 4},
		{"basic_reason", "Basic reason clauses", "because / so explanations", 1},
		{"because", "Because clauses", "Explain reasons in context", 2},
		{"conditionals", "Conditionals", "Conditional reasoning", 2},
		{"polite_request", "Polite requests", "Requests with appropriate register", 1},
		{"polite_refusal", "Polite refusal", "Decline while preserving rapport", 1},
		{"past_tense", "Past tense", "Past events and sequence", 1},
		{"past_perfect", "Past perfect", "Earlier past events", 2},
		{"plans", "Plans and intentions", "Intentions and plans", 1},
	}
	for _, s := range skills {
		if _, err := db.Exec(`INSERT OR IGNORE INTO skills(id,name,description,level) VALUES(?,?,?,?)`, s.id, s.n, s.d, s.level); err != nil {
			return err
		}
	}
	edges := [][4]any{
		{"foundations", "daily_life", "next", 1}, {"foundations", "preferences", "next", .9}, {"foundations", "ability", "next", .8}, {"foundations", "basic_reason", "next", .8}, {"foundations", "past_tense", "next", .7},
		{"daily_life", "questions", "next", 1}, {"daily_life", "advice", "next", .7}, {"daily_life", "obligation", "next", .7}, {"daily_life", "preferences", "related", .7},
		{"questions", "polite_request", "next", 1}, {"preferences", "plans", "next", .8}, {"preferences", "suggestions", "next", .7}, {"ability", "obligation", "related", .5},
		{"basic_reason", "because", "next", 1}, {"because", "conditionals", "next", .7}, {"past_tense", "past_perfect", "next", 1}, {"past_tense", "experience", "next", .7},
		{"suggestions", "comparison", "next", .7}, {"comparison", "contrast", "next", .7}, {"conditionals", "perfect_modals", "next", .8}, {"conditionals", "professional", "next", .7},
		{"experience", "reporting", "next", .6}, {"contrast", "professional", "next", .7}, {"reporting", "passive", "next", .7}, {"polite_request", "polite_refusal", "next", .6},
		{"professional", "nuance", "next", 1}, {"experience", "professional", "related", .5}, {"contrast", "professional", "related", .5},
	}
	for _, e := range edges {
		if _, err := db.Exec(`INSERT OR IGNORE INTO skill_edges(id,from_skill_id,to_skill_id,relation,weight) VALUES(?,?,?,?,?)`, fmt.Sprintf("%s-%s-%s", e[0], e[1], e[2]), e[0], e[1], e[2], e[3]); err != nil {
			return err
		}
	}
	for _, scene := range catalogScenes() {
		if _, err := db.Exec(`INSERT OR IGNORE INTO scenes(id,name,description) VALUES(?,?,?)`, scene[0], scene[1], scene[2]); err != nil {
			return err
		}
	}
	for _, intent := range catalogIntents() {
		if _, err := db.Exec(`INSERT OR IGNORE INTO communication_intents(id,name) VALUES(?,?)`, intent[0], intent[1]); err != nil {
			return err
		}
	}
	for _, p := range patternCatalog() {
		if _, err := db.Exec(`INSERT OR IGNORE INTO sentence_patterns(id,pattern,intent_id,difficulty,catalog_difficulty) VALUES(?,?,?,?,?)`, p.id, p.expression, p.intent, p.difficulty, p.difficulty); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE sentence_patterns SET catalog_difficulty=difficulty WHERE id=? AND catalog_difficulty=0`, p.id); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO pattern_mastery(pattern_id,attempts,correct,recent_accuracy,long_term_accuracy,consecutive_correct,mastery) VALUES(?,0,0,0,0,0,0.25)`, p.id); err != nil {
			return err
		}
		if len(adaptiveSeeds[p.id]) == 0 && len(p.seeds) > 0 {
			adaptiveSeeds[p.id] = p.seeds
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO pattern_skills(pattern_id,skill_id,weight) VALUES(?,?,1)`, p.id, p.skill); err != nil {
			return err
		}
		if p.id == "because" {
			// Keep the catalog item visible in both the foundational reason
			// route and the more specific because family.
			if _, err := db.Exec(`INSERT OR IGNORE INTO pattern_skills(pattern_id,skill_id,weight) VALUES(?,?,?)`, p.id, "basic_reason", .8); err != nil {
				return err
			}
		}
	}
	cfg := defaultAdaptiveConfig()
	raw, _ := json.Marshal(cfg)
	var vals map[string]any
	_ = json.Unmarshal(raw, &vals)
	for k, v := range vals {
		b, _ := json.Marshal(v)
		if _, err := db.Exec(`INSERT OR IGNORE INTO adaptive_config(key,value) VALUES(?,?)`, k, string(b)); err != nil {
			return err
		}
	}
	// Backfill a unified state for every existing V1 pattern without touching history.
	type backfill struct {
		pattern, skill      string
		mastery, difficulty float64
		attempts, correct   int
	}
	rows, err := db.Query(`SELECT p.id,COALESCE(ps.skill_id,''),COALESCE(m.mastery,.25),COALESCE(p.difficulty,1),COALESCE(m.attempts,0),COALESCE(m.correct,0) FROM sentence_patterns p LEFT JOIN pattern_skills ps ON ps.pattern_id=p.id LEFT JOIN pattern_mastery m ON m.pattern_id=p.id`)
	if err != nil {
		return err
	}
	var pending []backfill
	for rows.Next() {
		var x backfill
		if err := rows.Scan(&x.pattern, &x.skill, &x.mastery, &x.difficulty, &x.attempts, &x.correct); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, x)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, x := range pending {
		state, confidence := learnerStateFromEvidence(x.attempts, x.mastery, x.correct, defaultAdaptiveConfig())
		if _, err := db.Exec(`INSERT OR IGNORE INTO learner_skill_state(user_id,skill_id,pattern_id,mastery,current_difficulty,max_success_difficulty,attempt_count,success_count,evidence_count,state_confidence,state,updated_at) VALUES('default',?,?,?,?,?,?,?,?,?,?,?)`, x.skill, x.pattern, x.mastery, x.difficulty, x.difficulty, x.attempts, x.correct, x.attempts, confidence, state, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO difficulty_state(scope,entity_id,difficulty,empirical_difficulty,confidence,updated_at) VALUES('pattern',?,?,?,?,?)`, x.pattern, x.difficulty, x.difficulty, minConfidence(x.attempts), time.Now().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	// Older V2 databases may already have learner rows but not the calibration
	// columns. Reclassify from stored evidence without changing any mastery or
	// attempt history; zero-evidence rows remain UNKNOWN.
	legacyRows, err := db.Query(`SELECT pattern_id,attempt_count,success_count,mastery,state FROM learner_skill_state WHERE user_id='default'`)
	if err != nil {
		return err
	}
	type legacyState struct {
		pattern, stored   string
		attempts, correct int
		mastery           float64
	}
	var legacy []legacyState
	for legacyRows.Next() {
		var x legacyState
		if err := legacyRows.Scan(&x.pattern, &x.attempts, &x.correct, &x.mastery, &x.stored); err != nil {
			legacyRows.Close()
			return err
		}
		legacy = append(legacy, x)
	}
	if err := legacyRows.Close(); err != nil {
		return err
	}
	for _, x := range legacy {
		state, confidence := learnerStateFromEvidence(x.attempts, x.mastery, x.correct, defaultAdaptiveConfig())
		if _, err := db.Exec(`UPDATE learner_skill_state SET evidence_count=?,state_confidence=?,state=? WHERE user_id='default' AND pattern_id=?`, x.attempts, confidence, state, x.pattern); err != nil {
			return err
		}
	}
	_, _ = db.Exec(`INSERT OR IGNORE INTO difficulty_state(scope,entity_id,difficulty,success_rate,updated_at) VALUES('global','default',3,.5,?)`, time.Now().UTC().Format(time.RFC3339))
	return nil
}

func (s *Server) adaptiveConfig() AdaptiveConfig {
	c := defaultAdaptiveConfig()
	rows, err := s.db.Query(`SELECT key,value FROM adaptive_config`)
	if err != nil {
		return c
	}
	defer rows.Close()
	var k, v string
	for rows.Next() {
		if rows.Scan(&k, &v) != nil {
			continue
		}
		var n float64
		if json.Unmarshal([]byte(v), &n) == nil {
			switch k {
			case "recent_pattern_window":
				c.RecentPatternWindow = int(n)
			case "max_pattern_repeats":
				c.MaxPatternRepeats = int(n)
			case "failure_short_minutes":
				c.FailureShortMinutes = int(n)
			case "failure_repeat_hours":
				c.FailureRepeatHours = int(n)
			case "success_first_days":
				c.SuccessFirstDays = int(n)
			case "success_two_days":
				c.SuccessTwoDays = int(n)
			case "success_three_days":
				c.SuccessThreeDays = int(n)
			case "success_five_days":
				c.SuccessFiveDays = int(n)
			case "success_long_days":
				c.SuccessLongDays = int(n)
			case "current_zone_weight":
				c.CurrentZoneWeight = n
			case "weak_weight":
				c.WeakWeight = n
			case "review_weight":
				c.ReviewWeight = n
			case "maintenance_weight":
				c.MaintenanceWeight = n
			case "probe_weight":
				c.ProbeWeight = n
			case "target_success_min":
				c.TargetSuccessMin = n
			case "target_success_max":
				c.TargetSuccessMax = n
			case "probe_ratio":
				c.ProbeRatio = n
			case "new_skill_ratio":
				c.NewSkillRatio = n
			case "weak_skill_threshold":
				c.WeakSkillThreshold = n
			case "mastery_threshold":
				c.MasteryThreshold = n
			case "retention_threshold":
				c.RetentionThreshold = n
			}
		} else {
			var i int
			if json.Unmarshal([]byte(v), &i) == nil {
				if k == "recent_pattern_window" {
					c.RecentPatternWindow = i
				}
				if k == "max_pattern_repeats" {
					c.MaxPatternRepeats = i
				}
			}
		}
	}
	return c
}

type adaptiveCandidate struct {
	ID, Pattern, Intent, Skill            string
	Difficulty, Mastery, Retention, Score float64
	Attempts, Correct                     int
	State                                 string
	Reason                                string
	ReviewTiming                          string
	Review, Probe, New                    bool
	DecisionTrace                         map[string]float64
}

func (s *Server) adaptiveSelect(diff float64, mode, scene string) (adaptiveCandidate, error) {
	cfg := s.adaptiveConfig()
	now := time.Now().UTC()
	var recent []string
	rr, _ := s.db.Query(`SELECT e.pattern_id FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' ORDER BY a.submitted_at DESC,a.id DESC LIMIT ?`, cfg.RecentPatternWindow)
	if rr != nil {
		for rr.Next() {
			var p string
			_ = rr.Scan(&p)
			recent = append(recent, p)
		}
		rr.Close()
	}
	counts := map[string]int{}
	for _, p := range recent {
		counts[p]++
	}
	due := map[string]float64{}
	dueAt := map[string]string{}
	dr, _ := s.db.Query(`SELECT pattern_id,priority,due_at FROM review_schedule WHERE due_at<=?`, now.Format(time.RFC3339))
	if dr != nil {
		for dr.Next() {
			var p string
			var x float64
			var at string
			_ = dr.Scan(&p, &x, &at)
			due[p] = x
			dueAt[p] = at
		}
		dr.Close()
	}
	lockedSkills := map[string]bool{}
	pr, _ := s.db.Query(`SELECT e.to_skill_id, COALESCE(MAX(CASE WHEN ls.attempt_count>0 THEN ls.mastery ELSE 0 END),0) FROM skill_edges e JOIN skills target ON target.id=e.to_skill_id LEFT JOIN pattern_skills ps ON ps.skill_id=e.from_skill_id LEFT JOIN learner_skill_state ls ON ls.pattern_id=ps.pattern_id AND ls.user_id='default' WHERE e.relation='next' AND target.level>2 GROUP BY e.to_skill_id`)
	if pr != nil {
		for pr.Next() {
			var skill string
			var mastery float64
			if pr.Scan(&skill, &mastery) == nil && mastery < cfg.MasteryThreshold {
				lockedSkills[skill] = true
			}
		}
		pr.Close()
	}
	var observedPatterns int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM learner_skill_state WHERE user_id='default' AND attempt_count>0`).Scan(&observedPatterns)
	rows, err := s.db.Query(`SELECT p.id,p.pattern,i.id,COALESCE(ps.skill_id,''),COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(ls.mastery,COALESCE(pm.mastery,.25)),COALESCE(ls.retention,0),COALESCE(ds.difficulty,COALESCE(p.catalog_difficulty,p.difficulty)),COALESCE(ls.attempt_count,0),COALESCE(ls.success_count,0),COALESCE(ls.state,'') FROM sentence_patterns p JOIN communication_intents i ON i.id=p.intent_id LEFT JOIN (SELECT pattern_id,MIN(skill_id) AS skill_id FROM pattern_skills GROUP BY pattern_id) ps ON ps.pattern_id=p.id LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' LEFT JOIN pattern_mastery pm ON pm.pattern_id=p.id LEFT JOIN difficulty_state ds ON ds.scope='pattern' AND ds.entity_id=p.id`)
	if err != nil {
		return adaptiveCandidate{}, err
	}
	defer rows.Close()
	cs := []adaptiveCandidate{}
	for rows.Next() {
		var x adaptiveCandidate
		var entityDifficulty float64
		if err := rows.Scan(&x.ID, &x.Pattern, &x.Intent, &x.Skill, &x.Difficulty, &x.Mastery, &x.Retention, &entityDifficulty, &x.Attempts, &x.Correct, &x.State); err != nil {
			return adaptiveCandidate{}, err
		}
		x.State = stateFromRow(x.Attempts, x.Correct, x.Mastery, x.State, cfg)
		if entityDifficulty > 0 {
			x.Difficulty = entityDifficulty
		}
		if x.Attempts == 0 && mode == "adaptive" && observedPatterns > 0 && diff-x.Difficulty > 1.0 {
			continue
		}
		if x.Attempts == 0 && mode == "adaptive" && diff < 4 && x.Difficulty > diff+1.6 {
			continue
		}
		if len(recent) > 0 && recent[0] == x.ID {
			continue
		}
		if lockedSkills[x.Skill] && mode != "assessment" && mode != "probe" {
			continue
		}
		if (mode == "weak" || mode == "review") && x.Attempts == 0 {
			continue
		}
		fit := 1 - math.Min(1, math.Abs(x.Difficulty-diff)/4)
		weak := 1 - x.Mastery
		if x.Attempts == 0 {
			weak = 0 // UNKNOWN is not Weak, regardless of its initial mastery prior.
			x.New = true
		}
		urgency := due[x.ID]
		div := 0.0
		if len(recent) == 0 || recent[0] != x.ID {
			div += .2
		}
		if mode == "weak" {
			x.Reason = "weak_skill"
		}
		if x.Attempts == 0 && x.Reason == "" {
			x.Reason = "probe_eligibility"
		}
		if mode == "review" && urgency > 0 {
			x.Reason = "scheduled_review"
		}
		if x.Reason == "" {
			x.Reason = "current_level"
		}
		repeatPenalty := 0.25 * float64(counts[x.ID])
		recencyPenalty := 0.0
		if len(recent) > 0 && recent[0] == x.ID {
			recencyPenalty = 1
		}
		x.DecisionTrace = map[string]float64{"candidate_score": 0, "review_urgency": urgency, "weakness_score": weak, "difficulty_fit": fit, "recency_penalty": recencyPenalty, "repeat_penalty": repeatPenalty, "diversity_bonus": div, "graph_readiness": 1, "probe_factor": 0}
		x.Score = cfg.CurrentZoneWeight*fit + cfg.WeakWeight*weak + cfg.ReviewWeight*urgency + div - repeatPenalty
		if x.Attempts == 0 {
			// New curriculum is explored deliberately. A strong learner can
			// still reach a new high-level pattern, while an unknown low-level
			// item does not crowd out productive work.
			x.Score += cfg.ProbeWeight + cfg.NewSkillRatio*3 - .2
			x.DecisionTrace["probe_factor"] = cfg.ProbeWeight + cfg.NewSkillRatio*3 - .2
			if diff-x.Difficulty > 1.2 && len(recent) > 0 {
				x.Score -= .5
			}
		}
		if mode == "review" {
			x.Score += urgency
		}
		if mode == "weak" {
			x.Score += weak
		}
		x.DecisionTrace["candidate_score"] = x.Score
		cs = append(cs, x)
	}
	rows.Close()
	if len(cs) == 0 {
		return adaptiveCandidate{}, sql.ErrNoRows
	}
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Score == cs[j].Score {
			return cs[i].ID < cs[j].ID
		}
		return cs[i].Score > cs[j].Score
	})
	allowed := make([]adaptiveCandidate, 0, len(cs))
	for _, candidate := range cs {
		if counts[candidate.ID] < cfg.MaxPatternRepeats || mode == "assessment" {
			allowed = append(allowed, candidate)
		}
	}
	if len(allowed) == 0 {
		allowed = cs
	}
	chosen := allowed[0]
	if mode == "assessment" {
		var index int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE practice_mode='assessment' AND evaluation_status='validated'`).Scan(&index)
		anchor, anchorReason := adaptiveAssessmentAnchor(s.db, index)
		for _, candidate := range allowed {
			if candidate.ID == anchor {
				chosen = candidate
				chosen.Reason = anchorReason
				chosen.DecisionTrace["graph_readiness"] = 1
				break
			}
		}
	}
	if mode != "weak" && mode != "assessment" {
		for _, candidate := range allowed {
			if due[candidate.ID] > due[chosen.ID] {
				chosen = candidate
			}
		}
	}
	// A small, controlled probe budget explores the upper boundary without changing the policy.
	var attempts int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE evaluation_status='validated'`).Scan(&attempts)
	if mode != "review" && mode != "assessment" && cfg.ProbeRatio > 0 && attempts > 0 && attempts%int(math.Max(1, math.Round(1/cfg.ProbeRatio))) == 0 {
		chosen.Probe = true
		chosen.Reason = "probe"
		chosen.Difficulty = clamp(chosen.Difficulty+.4, 1, 8)
		chosen.DecisionTrace["probe_factor"] += .4
	}
	if _, ok := due[chosen.ID]; ok {
		chosen.Review = true
		chosenReviewTiming := "overdue"
		if dueAt[chosen.ID] != "" {
			if dueTime, parseErr := time.Parse(time.RFC3339, dueAt[chosen.ID]); parseErr == nil && dueTime.After(now.Add(-24*time.Hour)) {
				chosenReviewTiming = "near_due"
			}
		}
		chosen.ReviewTiming = chosenReviewTiming
		if chosen.Reason == "current_level" {
			chosen.Reason = "scheduled_review"
		}
	}
	if scene != "" {
		var sd float64
		if s.db.QueryRow(`SELECT difficulty FROM difficulty_state WHERE scope='scene' AND entity_id=?`, scene).Scan(&sd) == nil && sd > 0 {
			chosen.Difficulty = .7*chosen.Difficulty + .3*sd
		}
	}
	if chosen.Attempts == 0 {
		chosen.New = true
		if chosen.Reason == "current_level" {
			chosen.Reason = "new_skill"
		}
	}
	return chosen, nil
}

type exerciseSeed struct {
	Prompt, Context string
	Answers         []string
}

var adaptiveSeeds = map[string][]exerciseSeed{
	"because":           {{"\u6211\u6ca1\u6709\u53c2\u52a0\u4f1a\u8bae\uff0c\u56e0\u4e3a\u4e34\u65f6\u6709\u4e8b\u3002", "meeting / unexpected issue", []string{"I didn't attend the meeting because something came up."}}, {"\u6211\u4eec\u5f85\u5728\u5bb6\u91cc\uff0c\u56e0\u4e3a\u5916\u9762\u4e00\u76f4\u5728\u4e0b\u96e8\u3002", "home / rain", []string{"We stayed home because it kept raining outside."}}, {"\u5979\u6ca1\u6709\u9a6c\u4e0a\u56de\u590d\uff0c\u56e0\u4e3a\u5979\u6b63\u5728\u5f00\u8f66\u3002", "reply / driving", nil}},
	"conditional":       {{"\u5982\u679c\u660e\u5929\u6709\u7a7a\uff0c\u6211\u5c31\u5e2e\u4f60\u68c0\u67e5\u3002", "tomorrow / offer", nil}, {"\u5982\u679c\u65e9\u70b9\u77e5\u9053\uff0c\u6211\u5c31\u4e0d\u4f1a\u8d70\u9519\u8def\u4e86\u3002", "past condition / route", nil}},
	"going-to":          {{"\u6211\u672c\u6765\u6253\u7b97\u6628\u665a\u7ed9\u4f60\u6253\u7535\u8bdd\u3002", "yesterday / phone", nil}, {"\u6211\u4eec\u539f\u672c\u51c6\u5907\u5468\u672b\u53bb\u6d77\u8fb9\u3002", "weekend / beach", nil}},
	"modal-possibility": {{"\u6211\u4eca\u5929\u53ef\u80fd\u4f1a\u665a\u5230\u4e00\u4f1a\u513f\u3002", "late / today", nil}, {"\u4ed6\u53ef\u80fd\u5df2\u7ecf\u5728\u706b\u8f66\u4e0a\u4e86\u3002", "train / already", nil}},
	"polite-refusal":    {{"\u6050\u6015\u6211\u8fd9\u5468\u6ca1\u6709\u65f6\u95f4\u53c2\u52a0\u3002", "time / this week", nil}, {"\u8c22\u8c22\u4f60\u7684\u9080\u8bf7\uff0c\u4f46\u6211\u53ef\u80fd\u53bb\u4e0d\u4e86\u3002", "invitation / decline", nil}},
	"polite-request":    {{"\u4f60\u80fd\u628a\u4f1a\u8bae\u79fb\u5230\u4e0b\u5348\u5417\uff1f", "meeting / afternoon", nil}, {"\u4f60\u4ecb\u610f\u6211\u660e\u5929\u518d\u56de\u590d\u5417\uff1f", "reply / tomorrow", nil}},
	"past-perfect":      {{"\u5979\u5230\u8fbe\u65f6\uff0c\u6211\u5df2\u7ecf\u5403\u8fc7\u996d\u4e86\u3002", "arrival / meal", nil}, {"\u4f1a\u8bae\u5f00\u59cb\u524d\uff0c\u6211\u4eec\u5df2\u7ecf\u51c6\u5907\u597d\u4e86\u6750\u6599\u3002", "meeting / preparation", nil}},
	"wish-past":         {{"\u6211\u771f\u5e0c\u671b\u5f53\u65f6\u542c\u4e86\u4f60\u7684\u5efa\u8bae\u3002", "advice / regret", nil}},
}

// When the small curated pool is exhausted, use a natural context variation
// instead of exposing an internal uniqueness marker in the learner's prompt.
var adaptiveFallbackVariants = map[string][]string{
	"because":           {"\u6211\u6ca1\u6709\u53c2\u52a0\u5468\u4e94\u7684\u4f1a\u8bae\uff0c\u56e0\u4e3a\u4e34\u65f6\u6709\u4e8b\u3002", "\u6211\u4eec\u63d0\u524d\u79bb\u5f00\u4e86\uff0c\u56e0\u4e3a\u5b69\u5b50\u4e0d\u8212\u670d\u3002", "\u5979\u6ca1\u6709\u53ca\u65f6\u56de\u590d\uff0c\u56e0\u4e3a\u5979\u6b63\u5728\u5f00\u8f66\u3002"},
	"conditional":       {"\u5982\u679c\u660e\u5929\u5929\u6c14\u597d\uff0c\u6211\u4eec\u5c31\u53bb\u516c\u56ed\u3002", "\u5982\u679c\u4f60\u65e9\u70b9\u544a\u8bc9\u6211\uff0c\u6211\u5c31\u80fd\u5e2e\u4e0a\u5fd9\u3002", "\u5982\u679c\u4ed6\u6709\u65f6\u95f4\uff0c\u4ed6\u4f1a\u6765\u53c2\u52a0\u3002"},
	"going-to":          {"\u6211\u539f\u672c\u51c6\u5907\u5468\u672b\u53bb\u770b\u671b\u7236\u6bcd\u3002", "\u5979\u672c\u6765\u6253\u7b97\u4e0b\u73ed\u540e\u53bb\u4e70\u83dc\u3002", "\u6211\u4eec\u539f\u672c\u51c6\u5907\u4eca\u665a\u5728\u5bb6\u505a\u996d\u3002"},
	"modal-possibility": {"\u4ed6\u4eca\u665a\u53ef\u80fd\u4f1a\u7ed9\u4f60\u6253\u7535\u8bdd\u3002", "\u660e\u5929\u53ef\u80fd\u4f1a\u4e0b\u96e8\u3002", "\u5979\u53ef\u80fd\u5df2\u7ecf\u5230\u5bb6\u4e86\u3002"},
	"polite-refusal":    {"\u6050\u6015\u6211\u8fd9\u5468\u6ca1\u6709\u65f6\u95f4\u53c2\u52a0\u5468\u4e94\u7684\u4f1a\u8bae\u3002", "\u5f88\u62b1\u6b49\uff0c\u6211\u6050\u6015\u65e0\u6cd5\u53c2\u52a0\u8fd9\u6b21\u57f9\u8bad\u3002", "\u8c22\u8c22\u4f60\u7684\u9080\u8bf7\uff0c\u4f46\u6211\u8fd9\u5468\u5b9e\u5728\u62bd\u4e0d\u51fa\u65f6\u95f4\u3002"},
	"polite-request":    {"\u4f60\u65b9\u4fbf\u628a\u6587\u4ef6\u53d1\u7ed9\u6211\u5417\uff1f", "\u80fd\u8bf7\u4f60\u7a0d\u540e\u56de\u590d\u6211\u5417\uff1f", "\u4f60\u4ecb\u610f\u6211\u4eec\u628a\u4f1a\u8bae\u63a8\u8fdf\u534a\u5c0f\u65f6\u5417\uff1f"},
	"past-perfect":      {"\u706b\u8f66\u5230\u7ad9\u65f6\uff0c\u6211\u5df2\u7ecf\u79bb\u5f00\u4e86\u3002", "\u8001\u677f\u5230\u529e\u516c\u5ba4\u65f6\uff0c\u6211\u4eec\u5df2\u7ecf\u5f00\u5b8c\u4f1a\u4e86\u3002", "\u5979\u5230\u5bb6\u65f6\uff0c\u5b69\u5b50\u5df2\u7ecf\u7761\u7740\u4e86\u3002"},
	"wish-past":         {"\u6211\u771f\u5e0c\u671b\u5f53\u65f6\u6ca1\u6709\u8bf4\u90a3\u53e5\u8bdd\u3002", "\u5979\u5e0c\u671b\u81ea\u5df1\u5f53\u65f6\u65e9\u4e00\u70b9\u51fa\u53d1\u3002", "\u6211\u771f\u5e0c\u671b\u90a3\u5929\u542c\u4e86\u4f60\u7684\u5efa\u8bae\u3002"},
}

func (s *Server) unusedFallbackVariant(pattern, base string, recent []string) string {
	baseHash := normalizeChineseHash(base)
	for _, candidate := range adaptiveFallbackVariants[pattern] {
		if normalizeChineseHash(candidate) == baseHash {
			continue
		}
		seen := false
		for _, prompt := range recent {
			if normalizeChineseHash(prompt) == normalizeChineseHash(candidate) {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		var count int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM exercises WHERE normalized_chinese_hash=? AND created_at>=?`, normalizeChineseHash(candidate), time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339)).Scan(&count)
		if count == 0 {
			return candidate
		}
	}
	var expression string
	_ = s.db.QueryRow(`SELECT pattern FROM sentence_patterns WHERE id=?`, pattern).Scan(&expression)
	for _, context := range []string{"早上在咖啡店安排今天的事", "和同事讨论一个工作安排", "为周末出行或家庭事务做计划", "在服务柜台解决一个实际问题", "和朋友讨论一个日常选择"} {
		candidate := fmt.Sprintf("请在%s中自然表达这个意思，使用句型“%s”。", context, expression)
		if normalizeChineseHash(candidate) == baseHash {
			continue
		}
		seen := false
		for _, prompt := range recent {
			if normalizeChineseHash(prompt) == normalizeChineseHash(candidate) {
				seen = true
				break
			}
		}
		if seen {
			continue
		}
		var count int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM exercises WHERE normalized_chinese_hash=? AND created_at>=?`, normalizeChineseHash(candidate), time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339)).Scan(&count)
		if count == 0 {
			return candidate
		}
	}
	return ""
}

func normalizeChineseHash(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "\t", "", "\n", "", ".", "", ",", "", "!", "", "?", "").Replace(s)
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
func (s *Server) generateAIExercise(ctx context.Context, c adaptiveCandidate, scene string, recent []string) (exerciseSeed, string) {
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
		return exerciseSeed{}, ""
	}
	sys := `Generate one English practice exercise as strict JSON only. Fields: chinese_prompt, target_pattern, scene, intent, estimated_difficulty, reference_answers (array of strings). The target pattern and difficulty are constraints. Make a substantially different context from the recent exercises; do not reveal the answer before the learner responds.`
	user := fmt.Sprintf("pattern=%s skill=%s scene=%s intent=%s target_difficulty=%.1f selection_reason=%s recent=%s", c.Pattern, c.Skill, scene, c.Intent, c.Difficulty, c.Reason, strings.Join(recent, " | "))
	r, err := s.llm.Client(pc).Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "system", Content: sys}, {Role: "user", Content: user}}, Temperature: pc.Temperature, MaxTokens: maxInt(pc.MaxTokens, 500), JSONMode: true})
	if err != nil {
		return exerciseSeed{}, ""
	}
	var out struct {
		Prompt     string   `json:"chinese_prompt"`
		Pattern    string   `json:"target_pattern"`
		Scene      string   `json:"scene"`
		Difficulty float64  `json:"estimated_difficulty"`
		Answers    []string `json:"reference_answers"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(r.Content)), &out) != nil || strings.TrimSpace(out.Prompt) == "" || out.Pattern == "" {
		return exerciseSeed{}, ""
	}
	if out.Pattern != c.Pattern {
		out.Pattern = c.Pattern
	}
	if out.Difficulty <= 0 {
		out.Difficulty = c.Difficulty
	}
	return exerciseSeed{Prompt: out.Prompt, Answers: out.Answers}, "provider"
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *Server) generateExercise(ctx context.Context, diff float64, mode, scene string) (map[string]any, error) {
	ensureCatalogFallbackSeeds()
	c, err := s.adaptiveSelect(diff, mode, scene)
	if err != nil {
		if mode == "weak" || mode == "review" {
			return nil, err
		}
		return s.generateExerciseEmergency(diff, mode, scene)
	}
	chosenScene := chooseScene(scene)
	if scene == "" {
		chosenScene = s.adaptiveChooseScene(c.ID)
	}
	var recent []string
	rows, _ := s.db.Query(`SELECT e.chinese_prompt FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE e.pattern_id=? ORDER BY a.submitted_at DESC LIMIT 5`, c.ID)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var p string
			_ = rows.Scan(&p)
			recent = append(recent, p)
		}
	}
	seed, generatedBy := s.generateAIExercise(ctx, c, chosenScene, recent)
	if seed.Prompt == "" {
		pool := adaptiveSeeds[c.ID]
		if len(pool) == 0 {
			pool = []exerciseSeed{{"Express this idea in natural English.", "generic", nil}}
		}
		start := 0
		if len(pool) > 1 {
			start = int(time.Now().UnixNano() % int64(len(pool)))
			found := false
			for i := 0; i < len(pool); i++ {
				candidate := pool[(start+i)%len(pool)]
				seen := false
				for _, p := range recent {
					if normalizeChineseHash(p) == normalizeChineseHash(candidate.Prompt) {
						seen = true
					}
				}
				if !seen {
					start = (start + i) % len(pool)
					found = true
					break
				}
			}
			if !found {
				pool[start].Prompt = pool[start].Prompt + ""
			}
		}
		seed = pool[start]
		if len(pool) > 1 {
			allSeen := true
			for _, p := range pool {
				seen := false
				for _, recentPrompt := range recent {
					if normalizeChineseHash(p.Prompt) == normalizeChineseHash(recentPrompt) {
						seen = true
						break
					}
				}
				if !seen {
					allSeen = false
					break
				}
			}
			if allSeen {
				if variant := s.unusedFallbackVariant(c.ID, seed.Prompt, recent); variant != "" {
					seed.Prompt = variant
				}
			}
		}
		generatedBy = "fallback"
	}
	hash := normalizeChineseHash(seed.Prompt) // exact repeat guard; rotate curated candidates when needed.
	var same int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM exercises WHERE normalized_chinese_hash=? AND created_at>=?`, hash, time.Now().UTC().Add(-30*24*time.Hour).Format(time.RFC3339)).Scan(&same)
	if same > 0 {
		if variant := s.unusedFallbackVariant(c.ID, seed.Prompt, recent); variant != "" {
			seed.Prompt = variant
			hash = normalizeChineseHash(seed.Prompt)
			generatedBy = "fallback"
		} else if pool := adaptiveSeeds[c.ID]; len(pool) > 1 {
			for _, p := range pool {
				if normalizeChineseHash(p.Prompt) != hash {
					seed = p
					hash = normalizeChineseHash(seed.Prompt)
					generatedBy = "fallback"
					break
				}
			}
		} else if generatedBy == "provider" {
			seed = exerciseSeed{Prompt: "Express this idea in a different natural context.", Context: "dedup fallback"}
			hash = normalizeChineseHash(seed.Prompt)
			generatedBy = "fallback"
		}
	}
	exID := id("exercise")
	metaMap := map[string]any{"mode": mode, "selection_reason": c.Reason, "is_review": c.Review, "is_probe": c.Probe, "is_new_skill": c.New, "generated_by": generatedBy, "reference_answers": seed.Answers, "target_difficulty": c.Difficulty, "normalized_chinese_hash": hash, "recent_contexts": recent, "review_timing": c.ReviewTiming, "decision_trace_version": 1, "decision_trace": c.DecisionTrace}
	meta, _ := json.Marshal(metaMap)
	trace, _ := json.Marshal(c.DecisionTrace)
	if _, err := s.db.Exec(`INSERT INTO exercises(id,chinese_prompt,pattern_id,scene_id,intent_id,difficulty,metadata_json,created_at,normalized_chinese_hash,generated_by,decision_trace_json) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, exID, seed.Prompt, c.ID, chosenScene, c.Intent, c.Difficulty, string(meta), time.Now().UTC().Format(time.RFC3339), hash, generatedBy, string(trace)); err != nil {
		return nil, err
	}
	return map[string]any{"exercise_id": exID, "chinese_prompt": seed.Prompt, "target_pattern": c.Pattern, "pattern_id": c.ID, "scene_id": chosenScene, "communication_intent": c.Intent, "difficulty": c.Difficulty, "selection_reason": c.Reason, "is_review": c.Review, "is_probe": c.Probe, "is_new_skill": c.New, "generated_by": generatedBy, "reference_answers": seed.Answers, "decision_trace": c.DecisionTrace}, nil
}

// Emergency selection is used only when the normal candidate query cannot
// produce a row. It still uses the same metadata contract and never falls
// back to a difficulty-only ORDER BY query.
func (s *Server) generateExerciseEmergency(diff float64, mode, scene string) (map[string]any, error) {
	rows, err := s.db.Query(`SELECT p.id,p.pattern,i.id,p.difficulty FROM sentence_patterns p JOIN communication_intents i ON i.id=p.intent_id ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	type row struct {
		id, pattern, intent string
		difficulty          float64
	}
	var candidates []row
	for rows.Next() {
		var x row
		if rows.Scan(&x.id, &x.pattern, &x.intent, &x.difficulty) == nil {
			candidates = append(candidates, x)
		}
	}
	rows.Close()
	if len(candidates) == 0 {
		return nil, sql.ErrNoRows
	}
	var previous string
	_ = s.db.QueryRow(`SELECT e.pattern_id FROM attempts a JOIN exercises e ON e.id=a.exercise_id ORDER BY a.submitted_at DESC,a.id DESC LIMIT 1`).Scan(&previous)
	chosen := candidates[0]
	for _, x := range candidates {
		if x.id != previous {
			chosen = x
			break
		}
	}
	prompt := "\u8bf7\u7528\u81ea\u7136\u82f1\u8bed\u8868\u8fbe\u8fd9\u53e5\u8bdd\u3002"
	if pool := adaptiveSeeds[chosen.id]; len(pool) > 0 {
		prompt = pool[0].Prompt
	}
	chosenScene := chooseScene(scene)
	if scene == "" {
		chosenScene = s.adaptiveChooseScene(chosen.id)
	}
	hash := normalizeChineseHash(prompt)
	meta, _ := json.Marshal(map[string]any{"mode": mode, "selection_reason": "recovery", "generated_by": "fallback", "normalized_chinese_hash": hash})
	exID := id("exercise")
	if _, err = s.db.Exec(`INSERT INTO exercises(id,chinese_prompt,pattern_id,scene_id,intent_id,difficulty,metadata_json,created_at,normalized_chinese_hash,generated_by) VALUES(?,?,?,?,?,?,?,?,?,?)`, exID, prompt, chosen.id, chosenScene, chosen.intent, chosen.difficulty, string(meta), time.Now().UTC().Format(time.RFC3339), hash, "fallback"); err != nil {
		return nil, err
	}
	return map[string]any{"exercise_id": exID, "chinese_prompt": prompt, "target_pattern": chosen.pattern, "pattern_id": chosen.id, "scene_id": chosenScene, "communication_intent": chosen.intent, "difficulty": chosen.difficulty, "selection_reason": "recovery", "generated_by": "fallback"}, nil
}

func (s *Server) adaptiveChooseScene(pattern string) string {
	recent := map[string]bool{}
	r, _ := s.db.Query(`SELECT e.scene_id FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' ORDER BY a.submitted_at DESC LIMIT 3`)
	if r != nil {
		for r.Next() {
			var x string
			if r.Scan(&x) == nil {
				recent[x] = true
			}
		}
		r.Close()
	}
	rows, _ := s.db.Query(`SELECT id FROM scenes ORDER BY id`)
	if rows == nil {
		return "daily"
	}
	defer rows.Close()
	var available []string
	for rows.Next() {
		var x string
		if rows.Scan(&x) == nil && !recent[x] {
			available = append(available, x)
		}
	}
	if len(available) == 0 {
		for x := range recent {
			return x
		}
	}
	if len(available) > 0 {
		idx := int(time.Now().UnixNano() % int64(len(available)))
		return available[idx]
	}
	return "daily"
}

func adaptiveAttemptMetadata(tx *sql.Tx, attempt string) (mode, reason string, review, probe, newSkill bool, hash string, err error) {
	var meta string
	var exercise string
	err = tx.QueryRow(`SELECT exercise_id FROM attempts WHERE id=?`, attempt).Scan(&exercise)
	if err != nil {
		return
	}
	err = tx.QueryRow(`SELECT metadata_json FROM exercises WHERE id=?`, exercise).Scan(&meta)
	if err != nil {
		return
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(meta), &m)
	mode, _ = m["mode"].(string)
	reason, _ = m["selection_reason"].(string)
	review, _ = m["is_review"].(bool)
	probe, _ = m["is_probe"].(bool)
	newSkill, _ = m["is_new_skill"].(bool)
	hash, _ = m["normalized_chinese_hash"].(string)
	return
}

func (s *Server) populateAttemptMetadata(attempt string) error {
	var exercise, meta string
	if err := s.db.QueryRow(`SELECT exercise_id FROM attempts WHERE id=?`, attempt).Scan(&exercise); err != nil {
		return err
	}
	if err := s.db.QueryRow(`SELECT metadata_json FROM exercises WHERE id=?`, exercise).Scan(&meta); err != nil {
		return err
	}
	var m map[string]any
	_ = json.Unmarshal([]byte(meta), &m)
	mode, _ := m["mode"].(string)
	reason, _ := m["selection_reason"].(string)
	hash, _ := m["normalized_chinese_hash"].(string)
	generated, _ := m["generated_by"].(string)
	if generated == "" {
		generated = "fallback"
	}
	review, _ := m["is_review"].(bool)
	probe, _ := m["is_probe"].(bool)
	newSkill, _ := m["is_new_skill"].(bool)
	reviewTiming, _ := m["review_timing"].(string)
	_, err := s.db.Exec(`UPDATE attempts SET intent_id=(SELECT intent_id FROM exercises WHERE id=?),exercise_difficulty=(SELECT difficulty FROM exercises WHERE id=?),practice_mode=?,selection_reason=?,is_review=?,is_probe=?,is_new_skill=?,generated_by=?,normalized_chinese_hash=?,review_timing=? WHERE id=?`, exercise, exercise, mode, reason, boolInt(review), boolInt(probe), boolInt(newSkill), generated, hash, reviewTiming, attempt)
	return err
}

func updateAdaptiveStateTx(tx *sql.Tx, attempt, pattern, scene string, difficulty float64, e Eval) error {
	now := time.Now().UTC()
	mode, reason, review, probe, newSkill, hash, err := adaptiveAttemptMetadata(tx, attempt)
	if err != nil {
		return err
	}
	_ = mode
	_ = newSkill
	var intent string
	_ = tx.QueryRow(`SELECT intent_id FROM exercises WHERE id=(SELECT exercise_id FROM attempts WHERE id=?)`, attempt).Scan(&intent)
	var skill string
	_ = tx.QueryRow(`SELECT COALESCE(skill_id,'') FROM pattern_skills WHERE pattern_id=? LIMIT 1`, pattern).Scan(&skill)
	var a, su, fa, css, cf int
	var acq, ret, tr, mastery, cur, maxd, mem, stab float64
	var last, next sql.NullString
	var scenesJSON, intentsJSON string
	err = tx.QueryRow(`SELECT attempt_count,success_count,failure_count,consecutive_success,consecutive_failure,acquisition,retention,transfer,mastery,current_difficulty,max_success_difficulty,memory_strength,stability,last_seen_at,next_review_at,scene_coverage,intent_coverage FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, pattern).Scan(&a, &su, &fa, &css, &cf, &acq, &ret, &tr, &mastery, &cur, &maxd, &mem, &stab, &last, &next, &scenesJSON, &intentsJSON)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	ok := e.Verdict == "correct" || e.Verdict == "mostly_correct"
	a++
	if ok {
		su++
		css++
		cf = 0
	} else {
		fa++
		cf++
		css = 0
	}
	score := clamp(.45*e.PatternScore+.35*e.MeaningScore+.20*e.GrammarScore, 0, 1)
	if a == 1 {
		acq = score
	} else {
		acq = .65*acq + .35*score
	}
	if review || (last.Valid && now.Sub(parseTime(last.String)) > 24*time.Hour) {
		if a == 1 {
			ret = score
		} else {
			ret = .75*ret + .25*score
		}
	} else if ret == 0 {
		ret = score
	}
	tr = .7*tr + .3*e.NaturalnessScore
	if a == 1 {
		tr = e.NaturalnessScore
	}
	if !ok {
		penalty := .12
		if probe {
			penalty = .035
		}
		mastery = clamp(mastery*.72+score*.28-penalty, 0, 1)
	} else {
		mastery = clamp(.4*acq+.3*ret+.2*tr+.1*math.Min(1, float64(css)/5), 0, 1)
	}
	if difficulty > cur {
		cur = .65*cur + .35*difficulty
	}
	if ok && difficulty > maxd {
		maxd = difficulty
	}
	if cur == 0 {
		cur = difficulty
	}
	mem = clamp(.65*mem+.35*score, 0, 1)
	if ok {
		stab = clamp(stab+.15*score, 0, 1)
	} else {
		stab = clamp(stab*.7, 0, 1)
	}
	interval := adaptiveReviewIntervalFromTx(tx, ok, css, cf, ret, review)
	nextAt := now.Add(interval)
	scenes := map[string]int{}
	_ = json.Unmarshal([]byte(scenesJSON), &scenes)
	scenes[scene]++
	intents := map[string]int{}
	_ = json.Unmarshal([]byte(intentsJSON), &intents)
	intents[intent]++
	sceneCount := 0
	for _, n := range scenes {
		sceneCount += n
	}
	div := 0.0
	if sceneCount > 0 {
		div = float64(len(scenes)) / float64(sceneCount)
	}
	scJSON, _ := json.Marshal(scenes)
	inJSON, _ := json.Marshal(intents)
	if _, err = tx.Exec(`INSERT INTO learner_skill_state(user_id,skill_id,pattern_id,mastery,acquisition,retention,transfer,attempt_count,success_count,failure_count,recent_accuracy,long_term_accuracy,current_difficulty,max_success_difficulty,consecutive_success,consecutive_failure,last_seen_at,last_success_at,last_failure_at,next_review_at,scene_coverage,intent_coverage,context_diversity,memory_strength,stability,state_version,updated_at) VALUES('default',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(user_id,pattern_id) DO UPDATE SET skill_id=excluded.skill_id,mastery=excluded.mastery,acquisition=excluded.acquisition,retention=excluded.retention,transfer=excluded.transfer,attempt_count=excluded.attempt_count,success_count=excluded.success_count,failure_count=excluded.failure_count,recent_accuracy=excluded.recent_accuracy,long_term_accuracy=excluded.long_term_accuracy,current_difficulty=excluded.current_difficulty,max_success_difficulty=excluded.max_success_difficulty,consecutive_success=excluded.consecutive_success,consecutive_failure=excluded.consecutive_failure,last_seen_at=excluded.last_seen_at,last_success_at=excluded.last_success_at,last_failure_at=excluded.last_failure_at,next_review_at=excluded.next_review_at,scene_coverage=excluded.scene_coverage,intent_coverage=excluded.intent_coverage,context_diversity=excluded.context_diversity,memory_strength=excluded.memory_strength,stability=excluded.stability,state_version=excluded.state_version,updated_at=excluded.updated_at`, skill, pattern, mastery, acq, ret, tr, a, su, fa, score, score, cur, maxd, css, cf, now.Format(time.RFC3339), nullableTime(ok, now), nullableTime(!ok, now), nextAt.Format(time.RFC3339), string(scJSON), string(inJSON), div, mem, stab, 2, now.Format(time.RFC3339)); err != nil {
		return err
	}
	state, confidence := learnerStateFromEvidence(a, mastery, su, defaultAdaptiveConfig())
	if _, err = tx.Exec(`UPDATE learner_skill_state SET evidence_count=?,state_confidence=?,state=? WHERE user_id='default' AND pattern_id=?`, a, confidence, state, pattern); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE attempts SET intent_id=?,exercise_difficulty=?,practice_mode=?,selection_reason=?,is_review=?,is_probe=?,is_new_skill=?,evaluation_scores_json=?,error_types_json=?,error_severity=?,generated_by=?,normalized_chinese_hash=? WHERE id=?`, intent, difficulty, mode, reason, boolInt(review), boolInt(probe), boolInt(newSkill), mustJSON(map[string]float64{"meaning": e.MeaningScore, "grammar": e.GrammarScore, "naturalness": e.NaturalnessScore, "pattern": e.PatternScore}), mustJSON(errorTypes(e.Errors)), maxErrorSeverity(e.Errors), generatedByFromMeta(tx, attempt), hash, attempt); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO review_schedule(id,pattern_id,due_at,priority,reason,last_practiced) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET due_at=excluded.due_at,priority=excluded.priority,reason=excluded.reason,last_practiced=excluded.last_practiced`, "review_"+pattern, pattern, nextAt.Format(time.RFC3339), 1-mastery, reason, now.Format(time.RFC3339)); err != nil {
		return err
	}
	var priorAttempts int
	_ = tx.QueryRow(`SELECT COALESCE(attempts,0) FROM difficulty_state WHERE scope='pattern' AND entity_id=?`, pattern).Scan(&priorAttempts)
	catalog := difficulty
	_ = tx.QueryRow(`SELECT COALESCE(catalog_difficulty,difficulty) FROM sentence_patterns WHERE id=?`, pattern).Scan(&catalog)
	successRate := 0.0
	if ok {
		successRate = 1
	}
	observed := empiricalDifficulty(catalog, successRate, score, 1)
	effective := (catalog*5 + observed*float64(priorAttempts)) / float64(5+priorAttempts)
	_, err = tx.Exec(`INSERT INTO difficulty_state(scope,entity_id,difficulty,success_rate,attempts,empirical_difficulty,confidence,updated_at) VALUES('pattern',?,?,?,?,?,?,?) ON CONFLICT(scope,entity_id) DO UPDATE SET difficulty=excluded.difficulty,success_rate=(difficulty_state.success_rate*difficulty_state.attempts+excluded.success_rate)/(difficulty_state.attempts+1),attempts=difficulty_state.attempts+1,empirical_difficulty=excluded.empirical_difficulty,confidence=excluded.confidence,updated_at=excluded.updated_at`, pattern, effective, successRate, 1, observed, minConfidence(priorAttempts+1), now.Format(time.RFC3339))
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO difficulty_state(scope,entity_id,difficulty,success_rate,attempts,updated_at) VALUES('scene',?,?,?,1,?) ON CONFLICT(scope,entity_id) DO UPDATE SET difficulty=excluded.difficulty,success_rate=(difficulty_state.success_rate*difficulty_state.attempts+excluded.success_rate)/(difficulty_state.attempts+1),attempts=difficulty_state.attempts+1,updated_at=excluded.updated_at`, scene, difficulty, score, now.Format(time.RFC3339))
	return err
}
func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339, s); return t }
func nullableTime(ok bool, t time.Time) any {
	if ok {
		return t.Format(time.RFC3339)
	}
	return nil
}
func adaptiveReviewInterval(ok bool, success, failure int, ret float64, review bool) time.Duration {
	if !ok {
		if failure >= 2 {
			return 2 * time.Hour
		}
		return 10 * time.Minute
	}
	if review && ret > .8 {
		return 7 * 24 * time.Hour
	}
	switch {
	case success >= 5:
		return 30 * 24 * time.Hour
	case success >= 3:
		return 7 * 24 * time.Hour
	case success >= 2:
		return 3 * 24 * time.Hour
	default:
		return 24 * time.Hour
	}
}

func adaptiveReviewIntervalFromTx(tx *sql.Tx, ok bool, success, failure int, ret float64, review bool) time.Duration {
	cfg := defaultAdaptiveConfig()
	rows, err := tx.Query(`SELECT key,value FROM adaptive_config`)
	if err == nil {
		for rows.Next() {
			var key, value string
			if rows.Scan(&key, &value) != nil {
				continue
			}
			var n float64
			if json.Unmarshal([]byte(value), &n) != nil {
				continue
			}
			switch key {
			case "failure_short_minutes":
				cfg.FailureShortMinutes = int(n)
			case "failure_repeat_hours":
				cfg.FailureRepeatHours = int(n)
			case "success_first_days":
				cfg.SuccessFirstDays = int(n)
			case "success_two_days":
				cfg.SuccessTwoDays = int(n)
			case "success_three_days":
				cfg.SuccessThreeDays = int(n)
			case "success_five_days":
				cfg.SuccessFiveDays = int(n)
			case "success_long_days":
				cfg.SuccessLongDays = int(n)
			}
		}
		rows.Close()
	}
	if !ok {
		if failure >= 2 {
			return time.Duration(cfg.FailureRepeatHours) * time.Hour
		}
		return time.Duration(cfg.FailureShortMinutes) * time.Minute
	}
	if review && ret > .8 {
		return time.Duration(cfg.SuccessThreeDays) * 24 * time.Hour
	}
	switch {
	case success >= 5:
		return time.Duration(cfg.SuccessLongDays) * 24 * time.Hour
	case success >= 4:
		return time.Duration(cfg.SuccessFiveDays) * 24 * time.Hour
	case success >= 3:
		return time.Duration(cfg.SuccessThreeDays) * 24 * time.Hour
	case success >= 2:
		return time.Duration(cfg.SuccessTwoDays) * 24 * time.Hour
	default:
		return time.Duration(cfg.SuccessFirstDays) * 24 * time.Hour
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func errorTypes(es []map[string]any) []string {
	out := []string{}
	for _, e := range es {
		if t, ok := e["type"].(string); ok {
			out = append(out, t)
		}
	}
	return out
}
func maxErrorSeverity(es []map[string]any) string {
	best := ""
	rank := map[string]int{"minor": 1, "moderate": 2, "major": 3}
	for _, e := range es {
		if x, ok := e["severity"].(string); ok && rank[x] > rank[best] {
			best = x
		}
	}
	return best
}
func generatedByFromMeta(tx *sql.Tx, attempt string) string {
	var x string
	_ = tx.QueryRow(`SELECT json_extract(metadata_json,'$.generated_by') FROM exercises WHERE id=(SELECT exercise_id FROM attempts WHERE id=?)`, attempt).Scan(&x)
	if x == "" {
		x = "fallback"
	}
	return x
}

func (s *Server) rebuildLearnerState() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM learner_skill_state`, `DELETE FROM difficulty_state`, `DELETE FROM review_schedule`, `DELETE FROM error_stats`, `DELETE FROM pattern_mastery`, `DELETE FROM scene_mastery`, `INSERT INTO pattern_mastery(pattern_id,attempts,correct,recent_accuracy,long_term_accuracy,consecutive_correct,mastery) SELECT id,0,0,0,0,0,.25 FROM sentence_patterns`} {
		if _, err = tx.Exec(q); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO learner_skill_state(user_id,skill_id,pattern_id,mastery,current_difficulty,max_success_difficulty,evidence_count,state_confidence,state,updated_at) SELECT 'default',COALESCE(ps.skill_id,''),p.id,.25,COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(p.catalog_difficulty,p.difficulty),0,0,'UNKNOWN',? FROM sentence_patterns p LEFT JOIN pattern_skills ps ON ps.pattern_id=p.id`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE user_profile SET global_difficulty=3,updated_at=? WHERE id='default'`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO difficulty_state(scope,entity_id,difficulty,success_rate,updated_at) VALUES('global','default',3,.5,?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT a.id,e.pattern_id,e.scene_id,e.difficulty,v.verdict,v.meaning_score,v.grammar_score,v.naturalness_score,v.pattern_score,v.errors_json FROM attempts a JOIN exercises e ON e.id=a.exercise_id JOIN evaluations v ON v.attempt_id=a.id WHERE a.evaluation_status='validated' ORDER BY a.submitted_at,a.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var attempt, pattern, scene, errorsJSON, verdict string
		var d, m, g, n, p float64
		if err := rows.Scan(&attempt, &pattern, &scene, &d, &verdict, &m, &g, &n, &p, &errorsJSON); err != nil {
			return err
		}
		var errorsList []map[string]any
		_ = json.Unmarshal([]byte(errorsJSON), &errorsList)
		ev := Eval{Verdict: verdict, MeaningScore: m, GrammarScore: g, NaturalnessScore: n, PatternScore: p, Errors: errorsList}
		if err = updateMasteryTx(tx, pattern, scene, d, ev); err != nil {
			return err
		}
		if err = updateAdaptiveStateTx(tx, attempt, pattern, scene, d, ev); err != nil {
			return err
		}
		if err = updateProfileTx(tx, ev, d); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return tx.Commit()
}

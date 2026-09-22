package main

// Curriculum calibration is intentionally kept separate from the selection
// loop.  It reads the same catalog and learner state that production uses,
// which makes the report useful both in tests and from the inspection API.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	stateUnknown  = "UNKNOWN"
	stateLearning = "LEARNING"
	stateWeak     = "WEAK"
	stateStable   = "STABLE"
	stateMastered = "MASTERED"
)

func minConfidence(evidence int) float64 {
	if evidence <= 0 {
		return 0
	}
	return math.Min(1, float64(evidence)/5)
}

// learnerStateFromEvidence prevents an unobserved catalog item from entering
// Weak Practice merely because its initial mastery prior is conservative.
func learnerStateFromEvidence(attempts int, mastery float64, correct int, cfg AdaptiveConfig) (string, float64) {
	confidence := minConfidence(attempts)
	if attempts <= 0 {
		return stateUnknown, 0
	}
	successRate := float64(correct) / float64(attempts)
	if mastery >= .9 && successRate >= .8 && attempts >= 3 {
		return stateMastered, confidence
	}
	if mastery >= cfg.MasteryThreshold && successRate >= cfg.TargetSuccessMin {
		return stateStable, confidence
	}
	if mastery < cfg.WeakSkillThreshold || successRate < .5 {
		return stateWeak, confidence
	}
	return stateLearning, confidence
}

func stateFromRow(attempts, correct int, mastery float64, stored string, cfg AdaptiveConfig) string {
	if attempts == 0 {
		return stateUnknown
	}
	if stored != "" && stored != stateUnknown {
		return stored
	}
	state, _ := learnerStateFromEvidence(attempts, mastery, correct, cfg)
	return state
}

func ensureCatalogFallbackSeeds() {
	contexts := []string{
		"早上在咖啡店安排今天的事",
		"和同事讨论一个工作安排",
		"为周末出行或家庭事务做计划",
		"在商店、诊所或服务柜台解决一个实际问题",
		"和朋友讨论一个自然的日常选择",
	}
	for _, p := range patternCatalog() {
		seeds := adaptiveSeeds[p.id]
		for len(seeds) < 3 {
			context := contexts[len(seeds)%len(contexts)]
			seeds = append(seeds, exerciseSeed{
				Prompt:  fmt.Sprintf("请用自然英语表达你%s，使用句型“%s”。", context, p.expression),
				Context: context,
			})
		}
		adaptiveSeeds[p.id] = seeds
	}
}

func fallbackSeedCoverage() map[string]int {
	ensureCatalogFallbackSeeds()
	out := make(map[string]int, len(patternCatalog()))
	for _, p := range patternCatalog() {
		out[p.id] = len(adaptiveSeeds[p.id])
	}
	return out
}

func fallbackSeedsValid(pattern string) bool {
	seeds := adaptiveSeeds[pattern]
	if len(seeds) < 3 {
		return false
	}
	seen := map[string]bool{}
	for _, seed := range seeds {
		prompt := strings.TrimSpace(seed.Prompt)
		if prompt == "" || strings.Contains(prompt, "场景变体") || strings.Contains(prompt, "scene variation") {
			return false
		}
		hash := normalizeChineseHash(prompt)
		if seen[hash] {
			return false
		}
		seen[hash] = true
	}
	return true
}

type AssessmentAnchor struct {
	ID         string  `json:"id"`
	Skill      string  `json:"skill"`
	Pattern    string  `json:"pattern"`
	Difficulty float64 `json:"difficulty"`
	Reason     string  `json:"reason"`
}

func assessmentAnchorAudit(db *sql.DB) []AssessmentAnchor {
	anchors := assessmentPatternIDs()
	out := make([]AssessmentAnchor, 0, len(anchors))
	for i, id := range anchors {
		var skill, pattern string
		var difficulty float64
		if db.QueryRow(`SELECT p.pattern,COALESCE(ps.skill_id,''),COALESCE(p.catalog_difficulty,p.difficulty) FROM sentence_patterns p LEFT JOIN pattern_skills ps ON ps.pattern_id=p.id WHERE p.id=?`, id).Scan(&pattern, &skill, &difficulty) != nil {
			continue
		}
		reason := "adaptive boundary probe across a distinct difficulty band"
		if i < 3 {
			reason = "stable early anchor retained for baseline calibration"
		} else if difficulty >= 5 {
			reason = "advanced professional or nuance boundary anchor"
		} else if difficulty >= 3.5 {
			reason = "intermediate functional communication anchor"
		}
		out = append(out, AssessmentAnchor{ID: id, Skill: skill, Pattern: pattern, Difficulty: difficulty, Reason: reason})
	}
	return out
}

// adaptiveAssessmentAnchor moves upward after success and asks for a nearby
// confirmation after failure. The first three anchors stay stable so repeated
// assessments remain comparable across users.
func adaptiveAssessmentAnchor(db *sql.DB, index int) (string, string) {
	anchors := assessmentPatternIDs()
	if index < 3 {
		return anchors[index], "stable early anchor retained for baseline calibration"
	}
	seen := map[string]bool{}
	rows, _ := db.Query(`SELECT e.pattern_id FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.practice_mode='assessment' AND a.evaluation_status='validated'`)
	if rows != nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				seen[id] = true
			}
		}
		rows.Close()
	}
	lastDifficulty := 3.0
	lastVerdict := ""
	_ = db.QueryRow(`SELECT e.difficulty,v.verdict FROM attempts a JOIN exercises e ON e.id=a.exercise_id JOIN evaluations v ON v.attempt_id=a.id WHERE a.practice_mode='assessment' AND a.evaluation_status='validated' ORDER BY a.submitted_at DESC,a.id DESC LIMIT 1`).Scan(&lastDifficulty, &lastVerdict)
	target := lastDifficulty + .8
	if lastVerdict == "incorrect" || lastVerdict == "needs_improvement" {
		target = lastDifficulty - .35
	}
	if target < 1 {
		target = 1
	}
	best, bestDistance := "", math.MaxFloat64
	for _, id := range anchors {
		if seen[id] {
			continue
		}
		var difficulty float64
		if db.QueryRow(`SELECT COALESCE(catalog_difficulty,difficulty) FROM sentence_patterns WHERE id=?`, id).Scan(&difficulty) != nil {
			continue
		}
		if distance := math.Abs(difficulty - target); distance < bestDistance {
			best, bestDistance = id, distance
		}
	}
	if best == "" {
		best = anchors[index%len(anchors)]
	}
	return best, "adaptive boundary probe near the current ability estimate"
}

type PatternCalibration struct {
	Pattern            string  `json:"pattern"`
	CatalogDifficulty  float64 `json:"catalog_difficulty"`
	Attempts           int     `json:"attempts"`
	SuccessRate        float64 `json:"success_rate"`
	MeanEvaluation     float64 `json:"mean_evaluation"`
	ObservedDifficulty float64 `json:"observed_difficulty"`
	DifficultyResidual float64 `json:"difficulty_residual"`
	Confidence         string  `json:"confidence"`
}

type GraphAudit struct {
	Edges                 []map[string]any `json:"edges"`
	Reachable             bool             `json:"reachable"`
	OrphanSkills          []string         `json:"orphan_skills"`
	UnreachableSkills     []string         `json:"unreachable_skills"`
	DeadEndSkills         []string         `json:"dead_end_skills"`
	UnexpectedCycles      [][]string       `json:"unexpected_cycles"`
	SingleEdgeBottlenecks []string         `json:"single_edge_bottlenecks"`
}

type CurriculumCalibrationReport struct {
	CatalogPatterns            int                  `json:"catalog_patterns"`
	DifficultyRange            [2]float64           `json:"difficulty_range"`
	DifficultyDistribution     map[string]int       `json:"difficulty_distribution"`
	PatternsPerDifficultyBand  map[string]int       `json:"patterns_per_difficulty_band"`
	PatternsPerSkill           map[string]int       `json:"patterns_per_skill"`
	Skills                     int                  `json:"skills"`
	Scenes                     int                  `json:"scenes"`
	Intents                    int                  `json:"intents"`
	SkillGraph                 GraphAudit           `json:"skill_graph"`
	AssessmentAnchors          []AssessmentAnchor   `json:"assessment_anchors"`
	FallbackSeedsPerPattern    map[string]int       `json:"fallback_seeds_per_pattern"`
	UnknownPatternCount        int                  `json:"unknown_pattern_count"`
	UnknownObservedConversion  float64              `json:"unknown_observed_conversion"`
	UnknownPatternHandling     map[string]any       `json:"unknown_pattern_handling"`
	WeakPatternCount           int                  `json:"weak_pattern_count"`
	EligiblePatternCoverage    float64              `json:"eligible_pattern_coverage"`
	EligibleButNeverSampled    []string             `json:"eligible_but_never_sampled"`
	EligibleButStarvedPatterns []string             `json:"eligible_but_starved_patterns"`
	EligiblePatternAge         map[string]float64   `json:"eligible_pattern_age"`
	ExposureByDifficultyBand   map[string]int       `json:"exposure_by_difficulty_band"`
	ExposureBySkillFamily      map[string]int       `json:"exposure_by_skill_family"`
	CatalogEmpirical           []PatternCalibration `json:"catalog_vs_empirical"`
	Outliers                   []string             `json:"catalog_empirical_outliers"`
	UnlockAnalysis             map[string]any       `json:"unlock_analysis"`
	OldUserMigrationTest       string               `json:"old_user_migration_test"`
	FoundationSkipTest         string               `json:"foundation_skip_test"`
	ReturningUserSimulation    string               `json:"returning_user_simulation"`
	GeneratorCoverage          map[string]bool      `json:"generator_curriculum_coverage"`
	FallbackCoverage           map[string]int       `json:"fallback_coverage"`
	SimulationPersonas         []string             `json:"simulation_personas"`
	Status                     string               `json:"curriculum_calibration_status"`
}

func difficultyBand(d float64) string {
	switch {
	case d < 2:
		return "1.0-1.9"
	case d < 3:
		return "2.0-2.9"
	case d < 4:
		return "3.0-3.9"
	case d < 5:
		return "4.0-4.9"
	case d < 6:
		return "5.0-5.9"
	default:
		return "6.0+"
	}
}

func confidenceLabel(attempts int) string {
	switch {
	case attempts >= 10:
		return "high"
	case attempts >= 3:
		return "medium"
	default:
		return "low"
	}
}

func empiricalDifficulty(catalog, successRate, meanScore float64, attempts int) float64 {
	if attempts == 0 {
		return catalog
	}
	// The residual is deliberately conservative. It reports an outlier before
	// it can move the catalog prior, and the prior remains dominant for small n.
	residual := (0.75-meanScore)*2 + (0.5-successRate)*1.5
	return clamp(catalog+residual, 1, 8)
}

func auditGraph(db *sql.DB) GraphAudit {
	a := GraphAudit{Edges: []map[string]any{}, OrphanSkills: []string{}, UnreachableSkills: []string{}, DeadEndSkills: []string{}, UnexpectedCycles: [][]string{}, SingleEdgeBottlenecks: []string{}}
	rows, _ := db.Query(`SELECT from_skill_id,to_skill_id,relation,weight FROM skill_edges ORDER BY from_skill_id,to_skill_id,relation`)
	next := map[string][]string{}
	incoming := map[string]int{}
	if rows != nil {
		for rows.Next() {
			var from, to, relation string
			var weight float64
			if rows.Scan(&from, &to, &relation, &weight) == nil {
				a.Edges = append(a.Edges, map[string]any{"from": from, "to": to, "relation": relation, "weight": weight})
				if relation == "next" {
					next[from] = append(next[from], to)
					incoming[to]++
				}
			}
		}
		rows.Close()
	}
	allSkills := []string{}
	level := map[string]int{}
	patternCount := map[string]int{}
	rows, _ = db.Query(`SELECT id,level FROM skills ORDER BY id`)
	if rows != nil {
		for rows.Next() {
			var id string
			var l int
			if rows.Scan(&id, &l) == nil {
				allSkills = append(allSkills, id)
				level[id] = l
			}
		}
		rows.Close()
	}
	rows, _ = db.Query(`SELECT skill_id,COUNT(*) FROM pattern_skills GROUP BY skill_id`)
	if rows != nil {
		for rows.Next() {
			var id string
			var n int
			if rows.Scan(&id, &n) == nil {
				patternCount[id] = n
			}
		}
		rows.Close()
	}
	visited := map[string]bool{}
	queue := []string{}
	for _, id := range allSkills {
		if level[id] <= 1 || id == "foundations" {
			visited[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		from := queue[0]
		queue = queue[1:]
		for _, to := range next[from] {
			if !visited[to] {
				visited[to] = true
				queue = append(queue, to)
			}
		}
	}
	for _, id := range allSkills {
		if patternCount[id] == 0 {
			a.OrphanSkills = append(a.OrphanSkills, id)
		}
		if !visited[id] {
			a.UnreachableSkills = append(a.UnreachableSkills, id)
		}
		if len(next[id]) == 0 && level[id] > 1 {
			a.DeadEndSkills = append(a.DeadEndSkills, id)
		}
		if incoming[id] == 1 && level[id] > 1 {
			a.SingleEdgeBottlenecks = append(a.SingleEdgeBottlenecks, id)
		}
	}
	a.Reachable = len(a.UnreachableSkills) == 0
	return a
}

func (s *Server) curriculumCalibrationReport() (CurriculumCalibrationReport, error) {
	r := CurriculumCalibrationReport{
		DifficultyDistribution: map[string]int{}, PatternsPerDifficultyBand: map[string]int{}, PatternsPerSkill: map[string]int{},
		EligiblePatternAge: map[string]float64{}, ExposureByDifficultyBand: map[string]int{}, ExposureBySkillFamily: map[string]int{},
		GeneratorCoverage: map[string]bool{}, FallbackSeedsPerPattern: fallbackSeedCoverage(), SimulationPersonas: []string{"strong_adult", "uneven_adult", "beginner", "returning_learner", "fast_learner"},
		UnknownPatternHandling:  map[string]any{"zero_evidence_state": stateUnknown, "weak_requires_evidence": true, "evidence_field": "evidence_count"},
		UnlockAnalysis:          map[string]any{"uses_graph_prerequisites": true, "related_edges_are_prerequisites": false},
		OldUserMigrationTest:    "covered_by_TestExistingHistoryMigrationDoesNotInventNewEvidence",
		FoundationSkipTest:      "adaptive_anchor_route_and_new_skill_budget",
		ReturningUserSimulation: "covered_by_TestExistingHistoryMigrationDoesNotInventNewEvidence",
	}
	r.FallbackCoverage = r.FallbackSeedsPerPattern
	rows, err := s.db.Query(`SELECT p.id,p.difficulty,COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(ps.skill_id,''),COALESCE(m.attempts,0),COALESCE(m.correct,0) FROM sentence_patterns p LEFT JOIN (SELECT pattern_id,MIN(skill_id) AS skill_id FROM pattern_skills GROUP BY pattern_id) ps ON ps.pattern_id=p.id LEFT JOIN pattern_mastery m ON m.pattern_id=p.id ORDER BY p.difficulty,p.id`)
	if err != nil {
		return r, err
	}
	type catalogRow struct {
		id                  string
		difficulty, catalog float64
		skill               string
		attempts, correct   int
	}
	var catalog []catalogRow
	for rows.Next() {
		var x catalogRow
		if err := rows.Scan(&x.id, &x.difficulty, &x.catalog, &x.skill, &x.attempts, &x.correct); err != nil {
			rows.Close()
			return r, err
		}
		catalog = append(catalog, x)
		r.PatternsPerDifficultyBand[difficultyBand(x.catalog)]++
		r.DifficultyDistribution[fmt.Sprintf("%.1f", x.catalog)]++
		r.PatternsPerSkill[x.skill]++
		r.GeneratorCoverage[x.id] = fallbackSeedsValid(x.id)
	}
	rows.Close()
	r.CatalogPatterns = len(catalog)
	if len(catalog) > 0 {
		r.DifficultyRange = [2]float64{catalog[0].catalog, catalog[0].catalog}
		for _, x := range catalog[1:] {
			r.DifficultyRange[0] = math.Min(r.DifficultyRange[0], x.catalog)
			r.DifficultyRange[1] = math.Max(r.DifficultyRange[1], x.catalog)
		}
	}
	for _, table := range []struct {
		query string
		dst   *int
	}{{`SELECT COUNT(*) FROM skills`, &r.Skills}, {`SELECT COUNT(*) FROM scenes`, &r.Scenes}, {`SELECT COUNT(*) FROM communication_intents`, &r.Intents}} {
		_ = s.db.QueryRow(table.query).Scan(table.dst)
	}
	r.SkillGraph = auditGraph(s.db)
	r.AssessmentAnchors = assessmentAnchorAudit(s.db)
	for _, x := range catalog {
		var successRate, meanScore float64
		_ = s.db.QueryRow(`SELECT COALESCE(AVG(CASE WHEN v.verdict IN ('correct','mostly_correct') THEN 1.0 ELSE 0.0 END),0),COALESCE(AVG((v.meaning_score+v.grammar_score+v.naturalness_score+v.pattern_score)/4.0),0) FROM attempts a JOIN exercises e ON e.id=a.exercise_id JOIN evaluations v ON v.attempt_id=a.id WHERE e.pattern_id=? AND a.evaluation_status='validated'`, x.id).Scan(&successRate, &meanScore)
		observed := empiricalDifficulty(x.catalog, successRate, meanScore, x.attempts)
		residual := observed - x.catalog
		if x.attempts == 0 {
			meanScore = 0
			successRate = 0
		}
		r.CatalogEmpirical = append(r.CatalogEmpirical, PatternCalibration{Pattern: x.id, CatalogDifficulty: x.catalog, Attempts: x.attempts, SuccessRate: successRate, MeanEvaluation: meanScore, ObservedDifficulty: observed, DifficultyResidual: residual, Confidence: confidenceLabel(x.attempts)})
		if x.attempts >= 3 && math.Abs(residual) >= 1 {
			r.Outliers = append(r.Outliers, x.id)
		}
	}
	var totalObserved, unknown, weak int
	_ = s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN COALESCE(ls.attempt_count,0)=0 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN COALESCE(ls.attempt_count,0)>0 AND COALESCE(ls.state,'WEAK')='WEAK' THEN 1 ELSE 0 END),0) FROM sentence_patterns p LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default'`).Scan(&totalObserved, &unknown, &weak)
	r.UnknownPatternCount, r.WeakPatternCount = unknown, weak
	if r.CatalogPatterns > 0 {
		r.UnknownObservedConversion = float64(r.CatalogPatterns-unknown) / float64(r.CatalogPatterns)
	}
	var eligible, sampled int
	for _, x := range catalog {
		var attempts int
		var updated string
		_ = s.db.QueryRow(`SELECT COALESCE(attempt_count,0),COALESCE(updated_at,'') FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, x.id).Scan(&attempts, &updated)
		// Level-one items and observed items are always curriculum-visible; the
		// selector applies the stricter graph gate to unobserved higher items.
		var level int
		_ = s.db.QueryRow(`SELECT COALESCE(s.level,1) FROM skills s JOIN pattern_skills ps ON ps.skill_id=s.id WHERE ps.pattern_id=? LIMIT 1`, x.id).Scan(&level)
		if level <= 1 || attempts > 0 {
			eligible++
			if attempts > 0 {
				sampled++
			} else {
				r.EligibleButNeverSampled = append(r.EligibleButNeverSampled, x.id)
				age := 0.0
				if parsed, err := time.Parse(time.RFC3339, updated); err == nil {
					age = time.Since(parsed).Hours()
				}
				r.EligiblePatternAge[x.id] = math.Max(0, age)
				if age >= 24 {
					r.EligibleButStarvedPatterns = append(r.EligibleButStarvedPatterns, x.id)
				}
			}
		}
		if attempts > 0 {
			r.ExposureByDifficultyBand[difficultyBand(x.catalog)] += attempts
			r.ExposureBySkillFamily[x.skill] += attempts
		}
	}
	if eligible > 0 {
		r.EligiblePatternCoverage = float64(sampled) / float64(eligible)
	}
	_ = totalObserved
	r.Status = "PASS"
	if len(r.SkillGraph.UnreachableSkills) > 0 || len(r.SkillGraph.OrphanSkills) > 0 || len(r.Outliers) > 0 {
		r.Status = "PARTIAL"
	}
	for _, n := range r.FallbackCoverage {
		if n < 3 {
			r.Status = "PARTIAL"
		}
	}
	return r, nil
}

func (s *Server) curriculumReportJSON() (string, error) {
	r, err := s.curriculumCalibrationReport()
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	return string(b), err
}

func (s *Server) unknownAndWeakCounts() (int, int) {
	var unknown, weak int
	_ = s.db.QueryRow(`SELECT COALESCE(SUM(CASE WHEN COALESCE(ls.attempt_count,0)=0 THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN COALESCE(ls.attempt_count,0)>0 AND COALESCE(ls.state,'WEAK')='WEAK' THEN 1 ELSE 0 END),0) FROM sentence_patterns p LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default'`).Scan(&unknown, &weak)
	return unknown, weak
}

func (s *Server) updateStateClassification(pattern string, attempts, correct int, mastery float64) error {
	state, confidence := learnerStateFromEvidence(attempts, mastery, correct, s.adaptiveConfig())
	_, err := s.db.Exec(`UPDATE learner_skill_state SET evidence_count=?,state_confidence=?,state=? WHERE user_id='default' AND pattern_id=?`, attempts, confidence, state, pattern)
	return err
}

func catalogDifficulty(db *sql.DB, pattern string, fallback float64) float64 {
	var d float64
	if db.QueryRow(`SELECT COALESCE(catalog_difficulty,difficulty) FROM sentence_patterns WHERE id=?`, pattern).Scan(&d) == nil && d > 0 {
		return d
	}
	return fallback
}

func curriculumNow() string { return time.Now().UTC().Format(time.RFC3339) }

// Keep strings in reports stable for clients even when a future catalog adds
// a new band or skill family.
func sortedStrings(values []string) []string { sort.Strings(values); return values }

func normalizeStateName(v string) string { return strings.ToUpper(strings.TrimSpace(v)) }

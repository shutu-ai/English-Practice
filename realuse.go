package main

// Real-use observability is deliberately read-heavy and on-demand.  It records
// enough context to explain a selection or a session without putting report
// queries on the /api/practice/next hot path.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const calibrationVersion = "v2.1-real-use-1"

var allowedFeedbackTypes = map[string]bool{
	"too_easy": true, "too_hard": true, "unnatural": true,
	"evaluation_inaccurate": true, "repetitive": true,
}

type FeedbackRequest struct {
	ID           string         `json:"-"`
	ExerciseID   string         `json:"exercise_id"`
	AttemptID    string         `json:"attempt_id"`
	SessionID    string         `json:"session_id"`
	FeedbackType string         `json:"feedback_type"`
	Details      map[string]any `json:"details,omitempty"`
}

type calibrationWindow struct {
	Name      string
	Since     string
	SessionID string
}

type observedAttempt struct {
	ID, SessionID, ExerciseID, PatternID, SceneID, IntentID        string
	Submitted, Verdict, Reason, Hash, ReviewTiming                 string
	Difficulty, Meaning, Grammar, Naturalness, PatternScore        float64
	MasteryBefore, MasteryAfter, DifficultyBefore, DifficultyAfter float64
	Review, Probe, NewSkill                                        bool
}

func decodeJSONMap(raw string) map[string]any {
	out := map[string]any{}
	if json.Unmarshal([]byte(raw), &out) != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func (s *Server) calibrationWindow(name, sessionID string) calibrationWindow {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "7d" {
		name = "last_7_days"
	}
	if name == "30d" {
		name = "last_30_days"
	}
	if name == "current" {
		name = "current_session"
	}
	w := calibrationWindow{Name: name}
	switch name {
	case "last_7_days":
		w.Since = time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	case "last_30_days":
		w.Since = time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	case "current_session":
		w.SessionID = sessionID
	default:
		w.Name = "all"
	}
	return w
}

func attemptFilter(w calibrationWindow) (string, []any) {
	where := []string{"a.evaluation_status='validated'"}
	args := []any{}
	if w.Since != "" {
		where = append(where, "a.submitted_at>=?")
		args = append(args, w.Since)
	}
	if w.SessionID != "" {
		where = append(where, "a.session_id=?")
		args = append(args, w.SessionID)
	}
	return strings.Join(where, " AND "), args
}

func (s *Server) observedAttempts(w calibrationWindow) ([]observedAttempt, error) {
	where, args := attemptFilter(w)
	query := `SELECT a.id,a.session_id,a.exercise_id,e.pattern_id,e.scene_id,e.intent_id,a.submitted_at,
        COALESCE(v.verdict,''),COALESCE(a.exercise_difficulty,e.difficulty),
        COALESCE(v.meaning_score,0),COALESCE(v.grammar_score,0),COALESCE(v.naturalness_score,0),COALESCE(v.pattern_score,0),
        COALESCE(a.selection_reason,''),COALESCE(a.normalized_chinese_hash,''),COALESCE(a.review_timing,''),
        COALESCE(a.mastery_before,0),COALESCE(a.mastery_after,0),COALESCE(a.difficulty_before,0),COALESCE(a.difficulty_after,0),
        a.is_review,a.is_probe,a.is_new_skill
        FROM attempts a JOIN exercises e ON e.id=a.exercise_id LEFT JOIN evaluations v ON v.attempt_id=a.id
        WHERE ` + where + ` ORDER BY a.submitted_at,a.id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []observedAttempt
	for rows.Next() {
		var x observedAttempt
		var review, probe, newSkill int
		if err := rows.Scan(&x.ID, &x.SessionID, &x.ExerciseID, &x.PatternID, &x.SceneID, &x.IntentID, &x.Submitted, &x.Verdict, &x.Difficulty, &x.Meaning, &x.Grammar, &x.Naturalness, &x.PatternScore, &x.Reason, &x.Hash, &x.ReviewTiming, &x.MasteryBefore, &x.MasteryAfter, &x.DifficultyBefore, &x.DifficultyAfter, &review, &probe, &newSkill); err != nil {
			return nil, err
		}
		x.Review, x.Probe, x.NewSkill = review == 1, probe == 1, newSkill == 1
		out = append(out, x)
	}
	return out, rows.Err()
}

func accuracyMetric(xs []observedAttempt) map[string]any {
	if len(xs) == 0 {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	correct := 0
	for _, x := range xs {
		if x.Verdict == "correct" || x.Verdict == "mostly_correct" {
			correct++
		}
	}
	return map[string]any{"status": "ok", "sample_size": len(xs), "value": float64(correct) / float64(len(xs))}
}

func accuracySlice(xs []observedAttempt, n int) map[string]any {
	if len(xs) > n {
		xs = xs[len(xs)-n:]
	}
	return accuracyMetric(xs)
}

func difficultyMetrics(xs []observedAttempt) map[string]any {
	if len(xs) == 0 {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	values := make([]float64, len(xs))
	for i, x := range xs {
		values[i] = x.Difficulty
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	jitter := 0.0
	for i := 1; i < len(values); i++ {
		jitter += abs(values[i] - values[i-1])
	}
	if len(values) > 1 {
		jitter /= float64(len(values) - 1)
	}
	changes := func(n int) any {
		if len(values) < n {
			return map[string]any{"status": "insufficient_evidence", "sample_size": len(values)}
		}
		return map[string]any{"status": "ok", "sample_size": n, "value": values[len(values)-1] - values[len(values)-n]}
	}
	return map[string]any{
		"status": "ok", "sample_size": len(values), "current": values[len(values)-1],
		"min": sorted[0], "max": sorted[len(sorted)-1], "p50": sorted[(len(sorted)-1)/2],
		"changes_last_10": changes(10), "changes_last_50": changes(50), "jitter": jitter,
	}
}

func streakAndSpacing(xs []observedAttempt) map[string]any {
	if len(xs) == 0 {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	counts := map[string]int{}
	lastSeen := map[string]time.Time{}
	var gaps []float64
	maxPattern, maxSceneIntent := 0, 0
	patternStreak, sceneIntentStreak := 0, 0
	prevPattern, prevSceneIntent := "", ""
	for _, x := range xs {
		counts[x.Hash]++
		if x.Hash != "" {
			if t, err := time.Parse(time.RFC3339, x.Submitted); err == nil && !lastSeen[x.Hash].IsZero() {
				gaps = append(gaps, t.Sub(lastSeen[x.Hash]).Hours())
			}
			if t, err := time.Parse(time.RFC3339, x.Submitted); err == nil {
				lastSeen[x.Hash] = t
			}
		}
		if x.PatternID == prevPattern {
			patternStreak++
		} else {
			patternStreak = 1
		}
		if x.SceneID+"/"+x.IntentID == prevSceneIntent {
			sceneIntentStreak++
		} else {
			sceneIntentStreak = 1
		}
		maxPattern = maxInt(maxPattern, patternStreak)
		maxSceneIntent = maxInt(maxSceneIntent, sceneIntentStreak)
		prevPattern, prevSceneIntent = x.PatternID, x.SceneID+"/"+x.IntentID
	}
	repeated := 0
	for _, n := range counts {
		if n > 1 {
			repeated += n - 1
		}
	}
	avgGap, minGap := 0.0, 0.0
	if len(gaps) > 0 {
		minGap = gaps[0]
		for _, gap := range gaps {
			avgGap += gap
			minGap = mathMin(minGap, gap)
		}
		avgGap /= float64(len(gaps))
	}
	return map[string]any{"status": "ok", "sample_size": len(xs), "exact_repeat_rate": float64(repeated) / float64(len(xs)), "pattern_average_spacing_hours": avgGap, "pattern_min_spacing_hours": minGap, "max_same_pattern_streak": maxPattern, "max_same_scene_intent_streak": maxSceneIntent}
}

func mathMin(a, b float64) float64 {
	if a == 0 || b < a {
		return b
	}
	return a
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func (s *Server) patternStateCounts() map[string]int {
	out := map[string]int{stateUnknown: 0, stateWeak: 0, stateStable: 0, stateMastered: 0}
	rows, err := s.db.Query(`SELECT COALESCE(ls.state,'UNKNOWN'),COUNT(*) FROM sentence_patterns p LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' GROUP BY COALESCE(ls.state,'UNKNOWN')`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int
		if rows.Scan(&state, &count) == nil {
			out[normalizeStateName(state)] += count
		}
	}
	return out
}

func (s *Server) realUseCalibrationReport(windowName, sessionID string) (map[string]any, error) {
	base, err := s.curriculumCalibrationReport()
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(base)
	report := map[string]any{}
	_ = json.Unmarshal(b, &report)
	w := s.calibrationWindow(windowName, sessionID)
	xs, err := s.observedAttempts(w)
	if err != nil {
		return nil, err
	}
	states := s.patternStateCounts()
	accuracyByBand := map[string][]observedAttempt{}
	for _, x := range xs {
		accuracyByBand[difficultyBand(x.Difficulty)] = append(accuracyByBand[difficultyBand(x.Difficulty)], x)
	}
	bandMetrics := map[string]any{}
	for band, values := range accuracyByBand {
		bandMetrics[band] = accuracyMetric(values)
	}
	reviewServed, reviewSuccess := 0, 0
	probeSuccess, probeCount := 0, 0
	weakCount, weakExposures := 0, 0
	for _, x := range xs {
		if x.Review {
			reviewServed++
			if x.Verdict == "correct" || x.Verdict == "mostly_correct" {
				reviewSuccess++
			}
		}
		if x.Probe {
			probeCount++
			if x.Verdict == "correct" || x.Verdict == "mostly_correct" {
				probeSuccess++
			}
		}
	}
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM learner_skill_state WHERE user_id='default' AND state='WEAK'`).Scan(&weakCount)
	for _, x := range xs {
		var state string
		if s.db.QueryRow(`SELECT COALESCE(state,'') FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, x.PatternID).Scan(&state) == nil && state == stateWeak {
			weakExposures++
		}
	}
	retention := s.retentionMetrics(xs)
	readiness := s.readinessGate(xs, w)
	health := s.calibrationHealth(xs, reviewServed, probeCount, weakExposures)
	foundationRatio := s.foundationExposureRatio(xs)
	if len(base.EligibleButStarvedPatterns) > 0 {
		health = append(health, "ELIGIBLE_PATTERN_STARVATION")
	}
	if len(xs) >= 20 && foundationRatio > .6 {
		health = append(health, "FOUNDATION_OVEREXPOSURE")
	}
	report["acceptance_mode"] = os.Getenv("REAL_USE_ACCEPTANCE") == "1" || strings.EqualFold(os.Getenv("REAL_USE_ACCEPTANCE"), "true")
	report["window"] = map[string]any{"name": w.Name, "since": w.Since, "session_id": w.SessionID}
	report["total_attempts"] = len(xs)
	report["total_sessions"] = s.countSessions(w)
	report["pattern_state_counts"] = states
	report["observed_pattern_count"] = states[stateWeak] + states[stateStable] + states[stateMastered] + states[stateLearning]
	report["unknown_pattern_count"] = states[stateUnknown]
	report["difficulty"] = difficultyMetrics(xs)
	report["accuracy"] = map[string]any{"overall": accuracyMetric(xs), "recent_10": accuracySlice(xs, 10), "recent_30": accuracySlice(xs, 30), "recent_100": accuracySlice(xs, 100), "by_band": bandMetrics}
	report["repetition"] = streakAndSpacing(xs)
	dueReviews := s.countDueReviews()
	report["review"] = map[string]any{"due": dueReviews, "served": reviewServed, "due_not_served": maxInt(0, dueReviews-reviewServed), "hit_rate": metricRatio(reviewSuccess, reviewServed), "overdue": s.countOverdueReviews(), "served_before_due": s.countReviewTiming("before_due", w), "near_due": s.countReviewTiming("near_due", w), "overdue_served": s.countReviewTiming("overdue", w), "in_session_served": s.countReviewSessionType("in_session", w), "cross_session_served": s.countReviewSessionType("cross_session", w)}
	report["weak"] = map[string]any{"count": weakCount, "exposures": weakExposures, "exposure_ratio": metricRatio(weakExposures, len(xs)), "diagnostics": s.weakSpacing(w), "recovery_count": s.weakRecoveryCount(w)}
	report["probe"] = map[string]any{"count": probeCount, "ratio": metricRatio(probeCount, len(xs)), "success_rate": metricRatio(probeSuccess, probeCount), "difficulty_delta": s.probeDifficultyDelta(w)}
	report["curriculum"] = map[string]any{"unknown": states[stateUnknown], "unknown_to_observed": base.UnknownObservedConversion, "eligible": base.CatalogPatterns - base.UnknownPatternCount + len(base.EligibleButNeverSampled), "eligible_never_sampled": base.EligibleButNeverSampled, "unlock_count": s.countUnlocks(w), "band_coverage": base.ExposureByDifficultyBand, "newly_observed_last_7_days": s.newlyObserved(-7 * 24 * time.Hour), "newly_observed_last_30_days": s.newlyObserved(-30 * 24 * time.Hour)}
	report["foundation_exposure_ratio"] = metricFloatRatio(foundationRatio, len(xs))
	report["retention"] = retention
	report["health_flags"] = health
	report["readiness_gate"] = readiness
	report["feedback_count"] = s.countFeedback(w)
	return report, nil
}

func metricRatio(n, d int) any {
	if d <= 0 {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	return map[string]any{"status": "ok", "sample_size": d, "value": float64(n) / float64(d)}
}

func metricFloatRatio(value float64, sample int) any {
	if sample <= 0 {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	return map[string]any{"status": "ok", "sample_size": sample, "value": value}
}

func (s *Server) foundationExposureRatio(xs []observedAttempt) float64 {
	if len(xs) == 0 {
		return 0
	}
	foundation := 0
	for _, x := range xs {
		var exists int
		_ = s.db.QueryRow(`SELECT COUNT(*) FROM pattern_skills WHERE pattern_id=? AND skill_id='foundations'`, x.PatternID).Scan(&exists)
		if exists > 0 {
			foundation++
		}
	}
	return float64(foundation) / float64(len(xs))
}

func (s *Server) countSessions(w calibrationWindow) int {
	where := []string{"1=1"}
	args := []any{}
	if w.Since != "" {
		where = append(where, "started_at>=?")
		args = append(args, w.Since)
	}
	if w.SessionID != "" {
		where = append(where, "id=?")
		args = append(args, w.SessionID)
	}
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM sessions WHERE "+strings.Join(where, " AND "), args...).Scan(&n)
	return n
}

func (s *Server) countDueReviews() int {
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM review_schedule WHERE due_at<=?", time.Now().UTC().Format(time.RFC3339)).Scan(&n)
	return n
}

func (s *Server) countOverdueReviews() int {
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM review_schedule WHERE due_at<?", time.Now().UTC().Add(-24*time.Hour).Format(time.RFC3339)).Scan(&n)
	return n
}

func (s *Server) countReviewTiming(timing string, w calibrationWindow) int {
	where, args := attemptFilter(w)
	args = append(args, timing)
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM attempts a WHERE "+where+" AND a.review_timing=?", args...).Scan(&n)
	return n
}

func (s *Server) countReviewSessionType(timing string, w calibrationWindow) int {
	return s.countReviewTiming(timing, w)
}

func (s *Server) retentionMetrics(xs []observedAttempt) map[string]any {
	byPattern := map[string][]time.Time{}
	for _, x := range xs {
		if t, err := time.Parse(time.RFC3339, x.Submitted); err == nil {
			byPattern[x.PatternID] = append(byPattern[x.PatternID], t)
		}
	}
	counts := map[string]int{"short_gap": 0, "1d": 0, "3d": 0, "7d": 0}
	for _, times := range byPattern {
		for i := 1; i < len(times); i++ {
			days := times[i].Sub(times[i-1]).Hours() / 24
			switch {
			case days < 1:
				counts["short_gap"]++
			case days >= .75 && days <= 1.75:
				counts["1d"]++
			case days >= 2.25 && days <= 4.5:
				counts["3d"]++
			case days >= 5 && days <= 10:
				counts["7d"]++
			}
		}
	}
	out := map[string]any{}
	for k, v := range counts {
		if v == 0 {
			out[k] = map[string]any{"status": "insufficient_evidence", "sample_size": 0}
		} else {
			out[k] = map[string]any{"status": "ok", "sample_size": v}
		}
	}
	return out
}

func (s *Server) readinessGate(xs []observedAttempt, w calibrationWindow) map[string]any {
	sessions := s.countSessions(w)
	span := 0.0
	if len(xs) > 1 {
		first, _ := time.Parse(time.RFC3339, xs[0].Submitted)
		last, _ := time.Parse(time.RFC3339, xs[len(xs)-1].Submitted)
		span = last.Sub(first).Hours() / 24
	}
	patterns := map[string]bool{}
	for _, x := range xs {
		patterns[x.PatternID] = true
	}
	status := "READY_FOR_CALIBRATION"
	if len(xs) < 30 || sessions < 2 {
		status = "NOT_ENOUGH_DATA"
	} else if len(xs) < 100 || sessions < 3 || span < 7 || len(patterns) < 4 {
		status = "PARTIAL_DATA"
	}
	return map[string]any{"status": status, "attempts": len(xs), "sessions": sessions, "time_span_days": span, "pattern_coverage": len(patterns), "same_day_100_is_not_enough_for_7d": len(xs) >= 100 && span < 7}
}

func (s *Server) calibrationHealth(xs []observedAttempt, reviews, probes, weak int) []string {
	flags := []string{}
	repetition := streakAndSpacing(xs)
	if v, ok := repetition["exact_repeat_rate"].(float64); ok && len(xs) >= 20 && v >= .25 {
		flags = append(flags, "EXACT_REPEAT_HIGH")
	}
	if v, ok := repetition["max_same_pattern_streak"].(int); ok && v >= 4 {
		flags = append(flags, "PATTERN_STREAK_HIGH")
	}
	if d, ok := difficultyMetrics(xs)["jitter"].(float64); ok && len(xs) >= 10 && d >= .8 {
		flags = append(flags, "DIFFICULTY_JITTER_HIGH")
	}
	if len(xs) >= 10 && float64(weak)/float64(len(xs)) > .6 {
		flags = append(flags, "WEAK_SKILL_OVEREXPOSURE")
	}
	if len(xs) >= 30 && weak == 0 {
		flags = append(flags, "WEAK_SKILL_STARVATION")
	}
	if reviews == 0 && s.countDueReviews() > 0 {
		flags = append(flags, "REVIEW_STARVATION")
	}
	if len(xs) >= 20 && float64(probes)/float64(len(xs)) > .35 {
		flags = append(flags, "PROBE_TOO_HIGH")
	}
	if len(xs) >= 20 && float64(probes)/float64(len(xs)) < .02 {
		flags = append(flags, "PROBE_TOO_LOW")
	}
	return flags
}

func (s *Server) newlyObserved(offset time.Duration) int {
	cutoff := time.Now().UTC().Add(offset).Format(time.RFC3339)
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(DISTINCT e.pattern_id) FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' AND a.submitted_at>=? AND NOT EXISTS (SELECT 1 FROM attempts prior JOIN exercises pe ON pe.id=prior.exercise_id WHERE prior.evaluation_status='validated' AND pe.pattern_id=e.pattern_id AND prior.submitted_at<?)`, cutoff, cutoff).Scan(&n)
	return n
}

func (s *Server) countUnlocks(w calibrationWindow) int {
	where := "1=1"
	args := []any{}
	if w.Since != "" {
		where = "unlocked_at>=?"
		args = append(args, w.Since)
	}
	if w.SessionID != "" {
		where += " AND session_id=?"
		args = append(args, w.SessionID)
	}
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM skill_unlock_events WHERE "+where, args...).Scan(&n)
	return n
}

func (s *Server) countFeedback(w calibrationWindow) int {
	where := "1=1"
	args := []any{}
	if w.Since != "" {
		where = "created_at>=?"
		args = append(args, w.Since)
	}
	var n int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM feedback WHERE "+where, args...).Scan(&n)
	return n
}

func (s *Server) weakSpacing(w calibrationWindow) any {
	xs, err := s.observedAttempts(w)
	if err != nil {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	weak := map[string]bool{}
	rows, _ := s.db.Query(`SELECT pattern_id FROM learner_skill_state WHERE user_id='default' AND state='WEAK'`)
	if rows != nil {
		for rows.Next() {
			var id string
			if rows.Scan(&id) == nil {
				weak[id] = true
			}
		}
		rows.Close()
	}
	byPattern := map[string][]observedAttempt{}
	for _, x := range xs {
		if weak[x.PatternID] {
			byPattern[x.PatternID] = append(byPattern[x.PatternID], x)
		}
	}
	patterns := []map[string]any{}
	for pattern := range weak {
		values := byPattern[pattern]
		last3 := []string{}
		for i := maxInt(0, len(values)-3); i < len(values); i++ {
			last3 = append(last3, values[i].Verdict)
		}
		var gaps []float64
		for i := 1; i < len(values); i++ {
			a, ea := time.Parse(time.RFC3339, values[i-1].Submitted)
			b, eb := time.Parse(time.RFC3339, values[i].Submitted)
			if ea == nil && eb == nil {
				gaps = append(gaps, b.Sub(a).Hours())
			}
		}
		avg, min := 0.0, 0.0
		for _, gap := range gaps {
			avg += gap
			min = mathMin(min, gap)
		}
		if len(gaps) > 0 {
			avg /= float64(len(gaps))
		}
		var next string
		_ = s.db.QueryRow(`SELECT COALESCE(next_review_at,'') FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, pattern).Scan(&next)
		patterns = append(patterns, map[string]any{"pattern_id": pattern, "last20_count": countLast(values, 20), "last50_count": countLast(values, 50), "average_spacing_hours": avg, "min_spacing_hours": min, "last3_results": last3, "next_review": next})
	}
	sort.Slice(patterns, func(i, j int) bool {
		return fmt.Sprint(patterns[i]["pattern_id"]) < fmt.Sprint(patterns[j]["pattern_id"])
	})
	return map[string]any{"status": "ok", "sample_size": len(xs), "patterns": patterns}
}
func countLast(values []observedAttempt, n int) int {
	if len(values) < n {
		return len(values)
	}
	return n
}
func (s *Server) weakRecoveryCount(w calibrationWindow) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM learner_skill_state WHERE user_id='default' AND state IN ('STABLE','MASTERED') AND attempt_count>=3 AND success_count>=2`).Scan(&n)
	return n
}
func (s *Server) probeDifficultyDelta(w calibrationWindow) any {
	xs, err := s.observedAttempts(w)
	if err != nil {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	var total float64
	count := 0
	for _, x := range xs {
		if x.Probe && x.DifficultyAfter != 0 && x.DifficultyBefore != 0 {
			total += x.DifficultyAfter - x.DifficultyBefore
			count++
		}
	}
	if count == 0 {
		return map[string]any{"status": "insufficient_evidence", "sample_size": 0}
	}
	return map[string]any{"status": "ok", "sample_size": count, "value": total / float64(count)}
}

func (s *Server) calibrationPatternDiagnostics(windowName string) ([]map[string]any, error) {
	w := s.calibrationWindow(windowName, "")
	xs, err := s.observedAttempts(w)
	if err != nil {
		return nil, err
	}
	byPattern := map[string][]observedAttempt{}
	for _, x := range xs {
		byPattern[x.PatternID] = append(byPattern[x.PatternID], x)
	}
	rows, err := s.db.Query(`SELECT p.id,p.pattern,COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(ls.state,'UNKNOWN'),COALESCE(ls.evidence_count,0),COALESCE(ls.state_confidence,0),COALESCE(ls.mastery,0),COALESCE(ls.acquisition,0),COALESCE(ls.retention,0),COALESCE(ls.transfer,0),COALESCE(ls.next_review_at,'') FROM sentence_patterns p LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, name, state, next string
		var difficulty, confidence, mastery, acquisition, retention, transfer float64
		var evidence int
		if rows.Scan(&id, &name, &difficulty, &state, &evidence, &confidence, &mastery, &acquisition, &retention, &transfer, &next) != nil {
			continue
		}
		attempts := byPattern[id]
		var recent, success int
		var longTotal, longSuccess int
		var lastSeen, lastSuccess, lastFailure string
		var gaps []float64
		var previous time.Time
		reasons := map[string]int{}
		scenes := map[string]bool{}
		for _, x := range attempts {
			recent++
			longTotal++
			if x.Verdict == "correct" || x.Verdict == "mostly_correct" {
				success++
				longSuccess++
				lastSuccess = x.Submitted
			} else {
				lastFailure = x.Submitted
			}
			lastSeen = x.Submitted
			reasons[x.Reason]++
			scenes[x.SceneID] = true
			if t, e := time.Parse(time.RFC3339, x.Submitted); e == nil && !previous.IsZero() {
				gaps = append(gaps, t.Sub(previous).Hours())
				previous = t
			} else if e == nil {
				previous = t
			}
		}
		avgGap := 0.0
		for _, gap := range gaps {
			avgGap += gap
		}
		if len(gaps) > 0 {
			avgGap /= float64(len(gaps))
		}
		out = append(out, map[string]any{"pattern_id": id, "pattern": name, "catalog_difficulty": difficulty, "effective_difficulty": difficulty, "state": normalizeStateName(state), "evidence_count": evidence, "state_confidence": confidence, "attempts": len(attempts), "recent_accuracy": metricRatio(success, recent), "long_accuracy": metricRatio(longSuccess, longTotal), "mastery": mastery, "acquisition": acquisition, "retention": retention, "transfer": transfer, "last_seen": lastSeen, "last_success": lastSuccess, "last_failure": lastFailure, "next_review": next, "average_spacing_hours": avgGap, "scene_coverage": len(scenes), "selection_count_by_reason": reasons})
	}
	return out, rows.Err()
}

func (s *Server) calibrationSessionReport(sessionID string) (map[string]any, error) {
	var mode, started string
	var ended sql.NullString
	if err := s.db.QueryRow("SELECT mode,started_at,ended_at FROM sessions WHERE id=?", sessionID).Scan(&mode, &started, &ended); err != nil {
		return nil, err
	}
	xs, err := s.observedAttempts(calibrationWindow{Name: "current_session", SessionID: sessionID})
	if err != nil {
		return nil, err
	}
	questions := make([]map[string]any, 0, len(xs))
	for i, x := range xs {
		questions = append(questions, map[string]any{"position": i + 1, "attempt_id": x.ID, "exercise_id": x.ExerciseID, "pattern_id": x.PatternID, "difficulty": x.Difficulty, "selection_reason": x.Reason, "is_review": x.Review, "is_probe": x.Probe, "is_new_skill": x.NewSkill, "verdict": x.Verdict, "mastery_before": x.MasteryBefore, "mastery_after": x.MasteryAfter, "difficulty_before": x.DifficultyBefore, "difficulty_after": x.DifficultyAfter, "spacing_from_previous_same_pattern_hours": s.spacingFromPrevious(x, xs[:i])})
	}
	return map[string]any{"session_id": sessionID, "mode": mode, "started_at": started, "ended_at": ended.String, "attempt_count": len(xs), "questions": questions}, nil
}

func (s *Server) spacingFromPrevious(x observedAttempt, previous []observedAttempt) any {
	current, err := time.Parse(time.RFC3339, x.Submitted)
	if err != nil {
		return map[string]any{"status": "insufficient_evidence"}
	}
	for i := len(previous) - 1; i >= 0; i-- {
		if previous[i].PatternID == x.PatternID {
			then, e := time.Parse(time.RFC3339, previous[i].Submitted)
			if e == nil {
				return map[string]any{"status": "ok", "gap_hours": current.Sub(then).Hours()}
			}
		}
	}
	return map[string]any{"status": "insufficient_evidence"}
}

func (s *Server) recordFeedback(req FeedbackRequest) error {
	if !allowedFeedbackTypes[req.FeedbackType] {
		return fmt.Errorf("unsupported feedback_type")
	}
	if req.ExerciseID == "" && req.AttemptID == "" {
		return fmt.Errorf("exercise_id or attempt_id is required")
	}
	if req.ID == "" {
		req.ID = id("feedback")
	}
	if req.Details == nil {
		req.Details = map[string]any{}
	}
	// Link quality feedback to the exact provider/model and evaluated scores,
	// while never copying the answer or provider secret into telemetry.
	if req.AttemptID != "" {
		var provider, model, verdict, attemptExercise, attemptSession string
		var meaning, grammar, naturalness, pattern float64
		if s.db.QueryRow(`SELECT COALESCE(a.provider,''),COALESCE(a.model,''),COALESCE(v.verdict,''),COALESCE(v.meaning_score,0),COALESCE(v.grammar_score,0),COALESCE(v.naturalness_score,0),COALESCE(v.pattern_score,0),a.exercise_id,a.session_id FROM attempts a LEFT JOIN evaluations v ON v.attempt_id=a.id WHERE a.id=?`, req.AttemptID).Scan(&provider, &model, &verdict, &meaning, &grammar, &naturalness, &pattern, &attemptExercise, &attemptSession) == nil {
			if req.ExerciseID == "" {
				req.ExerciseID = attemptExercise
			}
			if req.SessionID == "" {
				req.SessionID = attemptSession
			}
			req.Details["provider"] = provider
			req.Details["model"] = model
			req.Details["verdict"] = verdict
			req.Details["scores"] = map[string]float64{"meaning": meaning, "grammar": grammar, "naturalness": naturalness, "pattern": pattern}
		}
	}
	if req.ExerciseID != "" {
		var pattern, scene string
		var difficulty float64
		if s.db.QueryRow(`SELECT pattern_id,scene_id,difficulty FROM exercises WHERE id=?`, req.ExerciseID).Scan(&pattern, &scene, &difficulty) == nil {
			req.Details["pattern_id"] = pattern
			req.Details["scene_id"] = scene
			req.Details["difficulty"] = difficulty
		}
	}
	b, _ := json.Marshal(req.Details)
	_, err := s.db.Exec("INSERT INTO feedback(id,exercise_id,attempt_id,session_id,feedback_type,details_json,created_at) VALUES(?,?,?,?,?,?,?)", req.ID, req.ExerciseID, req.AttemptID, req.SessionID, req.FeedbackType, string(b), time.Now().UTC().Format(time.RFC3339))
	return err
}

func updateSessionObservationTx(tx *sql.Tx, sessionID, pattern string, eval Eval, probe bool, masteryBefore, masteryAfter, difficultyBefore, difficultyAfter float64, position int) error {
	var patternsRaw, skillsRaw, exerciseID string
	var review, newSkill int
	if err := tx.QueryRow(`SELECT e.id,a.is_review,a.is_new_skill FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.session_id=? AND a.position=0 ORDER BY a.submitted_at DESC LIMIT 1`, sessionID).Scan(&exerciseID, &review, &newSkill); err != nil {
		// The position is assigned after this function, so read the current
		// attempt by its session's latest validated row below when needed.
		_ = tx.QueryRow(`SELECT e.id,a.is_review,a.is_new_skill FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.session_id=? AND a.evaluation_status='validated' ORDER BY a.submitted_at DESC,a.id DESC LIMIT 1`, sessionID).Scan(&exerciseID, &review, &newSkill)
	}
	_ = tx.QueryRow("SELECT patterns_seen,skills_seen FROM sessions WHERE id=?", sessionID).Scan(&patternsRaw, &skillsRaw)
	patterns := decodeJSONMap(patternsRaw)
	patterns[pattern] = intFromAny(patterns[pattern]) + 1
	skills := decodeJSONMap(skillsRaw)
	var skill string
	_ = tx.QueryRow(`SELECT COALESCE(skill_id,'') FROM pattern_skills WHERE pattern_id=? LIMIT 1`, pattern).Scan(&skill)
	if skill != "" {
		skills[skill] = intFromAny(skills[skill]) + 1
	}
	patternsJSON, _ := json.Marshal(patterns)
	skillsJSON, _ := json.Marshal(skills)
	success := eval.Verdict == "correct" || eval.Verdict == "mostly_correct"
	var mode string
	_ = tx.QueryRow("SELECT mode FROM sessions WHERE id=?", sessionID).Scan(&mode)
	if _, err := tx.Exec(`UPDATE sessions SET attempt_count=?,end_global_difficulty=?,patterns_seen=?,skills_seen=?,reviews_served=reviews_served+?,probes_served=probes_served+?,new_skills_served=new_skills_served+?,ended_at=? WHERE id=?`, position, difficultyAfter, string(patternsJSON), string(skillsJSON), review, boolInt(probe), newSkill, time.Now().UTC().Format(time.RFC3339), sessionID); err != nil {
		return err
	}
	if newSkill == 1 && skill != "" {
		var already int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM skill_unlock_events WHERE skill_id=?`, skill).Scan(&already)
		if already == 0 {
			evidence, _ := json.Marshal(map[string]any{"attempt_position": position, "mode": mode})
			probeEvidence, _ := json.Marshal(map[string]any{"is_probe": probe, "success": success})
			_, _ = tx.Exec(`INSERT INTO skill_unlock_events(id,skill_id,session_id,unlocked_at,prerequisite_evidence,prerequisite_mastery,retention,probe_evidence,unlock_reason) VALUES(?,?,?,?,?,?,?,?,?)`, id("unlock"), skill, sessionID, time.Now().UTC().Format(time.RFC3339), string(evidence), masteryBefore, masteryAfter, string(probeEvidence), "new_skill_selected_after graph readiness")
		}
	}
	_ = masteryBefore
	_ = masteryAfter
	_ = difficultyBefore
	return nil
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func (s *Server) calibrationSnapshot(window, sessionID string, debug bool) (map[string]any, error) {
	report, err := s.realUseCalibrationReport(window, sessionID)
	if err != nil {
		return nil, err
	}
	patterns, err := s.calibrationPatternDiagnostics(window)
	if err != nil {
		return nil, err
	}
	snapshot := map[string]any{"policy_version": "adaptive-v2.1", "calibration_version": calibrationVersion, "generated_at": time.Now().UTC().Format(time.RFC3339), "summary": report, "pattern_diagnostics": patterns, "health_flags": report["health_flags"]}
	snapshot["skill_unlock_events"] = s.unlockEventSnapshot()
	if sessionID != "" {
		if session, e := s.calibrationSessionReport(sessionID); e == nil {
			snapshot["session_summary"] = session
		}
	}
	if debug {
		snapshot["debug_answers_included"] = true
		where, args := attemptFilter(s.calibrationWindow(window, sessionID))
		rows, queryErr := s.db.Query("SELECT a.id,a.user_answer FROM attempts a WHERE "+where+" ORDER BY a.submitted_at,a.id", args...)
		if queryErr == nil {
			answers := []map[string]string{}
			for rows.Next() {
				var id, answer string
				if rows.Scan(&id, &answer) == nil {
					answers = append(answers, map[string]string{"attempt_id": id, "answer": answer})
				}
			}
			rows.Close()
			snapshot["debug_answers"] = answers
		}
	}
	return snapshot, nil
}

func (s *Server) unlockEventSnapshot() []map[string]any {
	rows, err := s.db.Query(`SELECT skill_id,session_id,unlocked_at,prerequisite_evidence,prerequisite_mastery,retention,probe_evidence,unlock_reason FROM skill_unlock_events ORDER BY unlocked_at`)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var skill, session, unlocked, evidence, probe, reason string
		var mastery, retention float64
		if rows.Scan(&skill, &session, &unlocked, &evidence, &mastery, &retention, &probe, &reason) == nil {
			out = append(out, map[string]any{"skill": skill, "session_id": session, "timestamp": unlocked, "prerequisite_evidence": decodeJSONMap(evidence), "prerequisite_mastery": mastery, "retention": retention, "probe_evidence": decodeJSONMap(probe), "unlock_reason": reason})
		}
	}
	return out
}

func (s *Server) calibrationReplay(window, candidate string) (map[string]any, error) {
	report, err := s.realUseCalibrationReport(window, "")
	if err != nil {
		return nil, err
	}
	candidateConfig := map[string]any{"source": "current_config", "changes": map[string]any{}}
	if strings.TrimSpace(candidate) != "" {
		if err := json.Unmarshal([]byte(candidate), &candidateConfig); err != nil {
			return nil, fmt.Errorf("candidate must be JSON: %w", err)
		}
	}
	return map[string]any{"mode": "historical_replay", "applied": false, "candidate": candidateConfig, "current_summary": map[string]any{"accuracy": report["accuracy"], "difficulty": report["difficulty"], "health_flags": report["health_flags"]}, "note": "Replay is diagnostic only; candidate policy is never auto-applied."}, nil
}

func runCalibrationReportCLI() error {
	dataDir := os.Getenv("ENGLISH_PRACTICE_DATA")
	if dataDir == "" {
		dataDir = "data"
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "english-practice.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	if err := migrate(db); err != nil {
		return err
	}
	if err := seed(db); err != nil {
		return err
	}
	s := &Server{db: db, llm: &LLMRegistry{configs: map[string]ProviderConfig{}}}
	report, err := s.realUseCalibrationReport(os.Getenv("CALIBRATION_WINDOW"), os.Getenv("CALIBRATION_SESSION_ID"))
	if err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

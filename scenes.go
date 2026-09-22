package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Scene data is deliberately small and curated. Patterns remain the learning
// units; scenes only constrain the candidate pool and provide context.
type sceneSeed struct {
	ID, Name, Description, Category string
	Sort                            int
	Children                        []sceneSeed
}

var adaptiveSceneSeeds = []sceneSeed{
	{ID: "daily", Name: "Daily Life", Description: "Everyday routines, errands, and casual plans", Category: "life", Sort: 10, Children: []sceneSeed{{"morning-routine", "Morning Routine", "Starting the day and getting ready", "life", 1, nil}, {"home", "Home", "Household routines and everyday needs", "life", 2, nil}, {"making-plans", "Making Plans", "Making and changing everyday plans", "life", 3, nil}, {"casual-conversation", "Casual Conversation", "Natural conversation with people you know", "life", 4, nil}, {"everyday-problems", "Everyday Problems", "Small practical problems and solutions", "life", 5, nil}}},
	{ID: "friends", Name: "Family & Friends", Description: "Invitations, feelings, requests, and polite choices", Category: "social", Sort: 20, Children: []sceneSeed{{"invitations", "Invitations", "Inviting and responding to invitations", "social", 1, nil}, {"making-suggestions", "Making Suggestions", "Suggesting activities and options", "social", 2, nil}, {"talking-feelings", "Talking About Feelings", "Sharing feelings and experiences", "social", 3, nil}, {"asking-help", "Asking for Help", "Asking for and offering help", "social", 4, nil}, {"polite-refusal", "Polite Refusal", "Declining while keeping the conversation friendly", "social", 5, nil}}},
	{ID: "shopping", Name: "Shopping", Description: "Products, prices, options, returns, and service", Category: "service", Sort: 30, Children: []sceneSeed{{"product-questions", "Asking About Products", "Questions about products and services", "service", 1, nil}, {"price-payment", "Price & Payment", "Prices, payment, and negotiation", "service", 2, nil}, {"returns", "Returns", "Returning or exchanging an item", "service", 3, nil}, {"sizes-options", "Sizes & Options", "Choosing sizes, options, and alternatives", "service", 4, nil}, {"shopping-complaints", "Complaints", "Resolving a service problem politely", "service", 5, nil}}},
	{ID: "restaurant", Name: "Restaurant", Description: "Ordering, questions, requests, complaints, and paying", Category: "service", Sort: 40, Children: []sceneSeed{{"ordering", "Ordering", "Ordering food and drinks", "service", 1, nil}, {"restaurant-questions", "Asking Questions", "Questions about dishes and ingredients", "service", 2, nil}, {"special-requests", "Special Requests", "Making polite requests about an order", "service", 3, nil}, {"restaurant-complaints", "Complaints", "Handling a problem with an order", "service", 4, nil}, {"paying", "Paying", "Asking for the bill and paying", "service", 5, nil}}},
	{ID: "health", Name: "Health", Description: "Appointments, symptoms, advice, and wellbeing", Category: "life", Sort: 50, Children: []sceneSeed{{"symptoms", "Describing Symptoms", "Explaining a symptom clearly", "life", 1, nil}, {"appointments", "Making an Appointment", "Booking or changing an appointment", "life", 2, nil}, {"health-advice", "Asking for Advice", "Asking for practical health advice", "life", 3, nil}, {"explaining-feelings", "Explaining How You Feel", "Describing how you feel in context", "life", 4, nil}}},
	{ID: "travel", Name: "Travel", Description: "Directions, transport, bookings, and travel problems", Category: "travel", Sort: 60, Children: []sceneSeed{{"directions", "Asking Directions", "Finding a place and checking a route", "travel", 1, nil}, {"transportation", "Transportation", "Trains, buses, taxis, and arrivals", "travel", 2, nil}, {"booking", "Booking", "Making and changing a booking", "travel", 3, nil}, {"travel-problems", "Problems", "Explaining a travel problem", "travel", 4, nil}, {"travel-changes", "Changes", "Changing a plan or reservation", "travel", 5, nil}}},
	{ID: "hotel", Name: "Hotel", Description: "Check-in, requests, problems, and check-out", Category: "travel", Sort: 70, Children: []sceneSeed{{"hotel-check-in", "Check-in", "Checking in and confirming details", "travel", 1, nil}, {"hotel-requests", "Requests", "Making a request at a hotel", "travel", 2, nil}, {"hotel-problems", "Problems", "Explaining a problem with a room or service", "travel", 3, nil}, {"hotel-check-out", "Check-out", "Checking out and settling payment", "travel", 4, nil}}},
	{ID: "airport", Name: "Airport", Description: "Check-in, security, boarding, changes, and baggage", Category: "travel", Sort: 80, Children: []sceneSeed{{"airport-check-in", "Check-in", "Checking in and confirming a flight", "travel", 1, nil}, {"security", "Security", "Answering questions at security", "travel", 2, nil}, {"boarding", "Boarding", "Gates, boarding, and announcements", "travel", 3, nil}, {"flight-changes", "Flight Changes", "Delays, cancellations, and changes", "travel", 4, nil}, {"lost-baggage", "Lost Baggage", "Reporting and resolving lost baggage", "travel", 5, nil}}},
	{ID: "phone", Name: "Phone Call", Description: "Starting, clarifying, leaving, and ending a call", Category: "communication", Sort: 90, Children: []sceneSeed{{"starting-call", "Starting a Call", "Opening a phone call naturally", "communication", 1, nil}, {"asking-someone", "Asking for Someone", "Asking to speak to a person", "communication", 2, nil}, {"phone-clarification", "Clarification", "Clarifying information on a call", "communication", 3, nil}, {"leaving-message", "Leaving a Message", "Leaving a clear message", "communication", 4, nil}, {"ending-call", "Ending a Call", "Closing a phone call politely", "communication", 5, nil}}},
	{ID: "work", Name: "Work", Description: "Updates, questions, requests, and coordination", Category: "professional", Sort: 100, Children: []sceneSeed{{"work-questions", "Asking Questions", "Asking useful work questions", "professional", 1, nil}, {"giving-updates", "Giving Updates", "Giving a concise progress update", "professional", 2, nil}, {"explaining-problems", "Explaining Problems", "Explaining a work problem and its impact", "professional", 3, nil}, {"work-requests", "Making Requests", "Making a clear professional request", "professional", 4, nil}, {"work-clarifying", "Clarifying", "Clarifying a work detail", "professional", 5, nil}, {"coordinating", "Coordinating", "Coordinating people, time, and next steps", "professional", 6, nil}}},
	{ID: "meeting", Name: "Meeting", Description: "Opinions, suggestions, disagreement, and progress", Category: "professional", Sort: 110, Children: []sceneSeed{{"giving-opinions", "Giving Opinions", "Sharing a reasoned opinion", "professional", 1, nil}, {"meeting-suggestions", "Making Suggestions", "Suggesting a practical next step", "professional", 2, nil}, {"agreeing", "Agreeing", "Agreeing and building on an idea", "professional", 3, nil}, {"polite-disagreement", "Polite Disagreement", "Disagreeing without damaging rapport", "professional", 4, nil}, {"meeting-clarifying", "Clarifying", "Clarifying a complex point", "professional", 5, nil}, {"reporting-progress", "Reporting Progress", "Reporting progress and delays", "professional", 6, nil}, {"summarizing", "Summarizing", "Summarizing a decision or discussion", "professional", 7, nil}}},
	{ID: "problem", Name: "Problem Solving", Description: "Causes, impact, options, solutions, and escalation", Category: "professional", Sort: 120, Children: []sceneSeed{{"explaining-cause", "Explaining Cause", "Explaining why something happened", "professional", 1, nil}, {"explaining-impact", "Explaining Impact", "Explaining the effect of a problem", "professional", 2, nil}, {"suggesting-solutions", "Suggesting Solutions", "Suggesting a practical solution", "professional", 3, nil}, {"comparing-options", "Comparing Options", "Comparing possible choices", "professional", 4, nil}, {"escalating-issues", "Escalating Issues", "Escalating an issue clearly and respectfully", "professional", 5, nil}}},
	{ID: "social", Name: "Social Conversation", Description: "Small talk, preferences, invitations, and disagreement", Category: "social", Sort: 130, Children: []sceneSeed{{"small-talk", "Small Talk", "Starting a relaxed conversation", "social", 1, nil}, {"social-opinions", "Opinions", "Sharing an opinion in conversation", "social", 2, nil}, {"social-invitations", "Invitations", "Inviting and responding naturally", "social", 3, nil}, {"social-preferences", "Preferences", "Talking about preferences", "social", 4, nil}, {"social-disagreement", "Polite Disagreement", "Disagreeing politely in social conversation", "social", 5, nil}}},
}

var scenePatternFamilies = map[string][]string{
	"daily":      {"be-basic", "have-basic", "like", "can", "want", "need", "there-is", "do-you", "present-continuous", "simple-past", "future-will", "because", "going-to", "should"},
	"friends":    {"like", "want", "can", "would-like", "can-i", "should", "lets", "would-you-mind", "polite-refusal", "present-perfect", "because"},
	"shopping":   {"would-like", "can-i", "would-you-mind", "comparative", "negotiate", "polite-refusal", "because", "clarification"},
	"restaurant": {"would-like", "can-i", "would-you-mind", "polite-refusal", "clarification", "because", "comparative"},
	"health":     {"should", "need", "can", "because", "present-perfect", "clarification", "would-you-mind"},
	"travel":     {"going-to", "modal-possibility", "present-perfect", "comparative", "can-i", "would-like", "because", "simple-past", "negotiate"},
	"hotel":      {"would-like", "can-i", "would-you-mind", "polite-request", "polite-refusal", "clarification", "because"},
	"airport":    {"can-i", "would-like", "polite-request", "because", "simple-past", "modal-possibility", "clarification"},
	"phone":      {"would-you-mind", "polite-request", "clarification", "reported-speech", "polite-refusal", "because"},
	"work":       {"professional-suggestion", "clarification", "disagreement", "negotiate", "reported-speech", "hedge", "conditional", "because", "would-like"},
	"meeting":    {"professional-suggestion", "clarification", "disagreement", "negotiate", "hedge", "although", "would-rather", "having-said-that", "formal-opinion", "reported-speech"},
	"problem":    {"because", "conditional", "clarification", "disagreement", "negotiate", "comparative", "although", "professional-suggestion", "hedge"},
	"social":     {"like", "want", "would-like", "lets", "would-rather", "comparative", "although", "disagreement", "polite-refusal"},
}

func seedSceneHierarchy(db *sql.DB) error {
	validPatterns := map[string]bool{}
	rows, err := db.Query(`SELECT id FROM sentence_patterns`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		validPatterns[id] = true
	}
	rows.Close()
	for _, root := range adaptiveSceneSeeds {
		if _, err := db.Exec(`INSERT OR IGNORE INTO scenes(id,name,description,parent_id,category,difficulty_min,difficulty_max,enabled,sort_order) VALUES(?,?,?,?,?,?,?,?,?)`, root.ID, root.Name, root.Description, "", root.Category, 1, 8, 1, root.Sort); err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE scenes SET name=?,description=?,parent_id='',category=?,enabled=1,sort_order=? WHERE id=?`, root.Name, root.Description, root.Category, root.Sort, root.ID); err != nil {
			return err
		}
		for _, child := range root.Children {
			if _, err := db.Exec(`INSERT OR IGNORE INTO scenes(id,name,description,parent_id,category,difficulty_min,difficulty_max,enabled,sort_order) VALUES(?,?,?,?,?,?,?,?,?)`, child.ID, child.Name, child.Description, root.ID, child.Category, 1, 8, 1, child.Sort); err != nil {
				return err
			}
		}
		patterns := filterExistingPatterns(scenePatternFamilies[root.ID], validPatterns)
		for _, pid := range patterns {
			if _, err := db.Exec(`INSERT OR IGNORE INTO scene_pattern_map(scene_id,pattern_id,weight) VALUES(?,?,1)`, root.ID, pid); err != nil {
				return err
			}
			var intent, skill string
			_ = db.QueryRow(`SELECT intent_id FROM sentence_patterns WHERE id=?`, pid).Scan(&intent)
			_ = db.QueryRow(`SELECT skill_id FROM pattern_skills WHERE pattern_id=? ORDER BY weight DESC LIMIT 1`, pid).Scan(&skill)
			if intent != "" {
				_, _ = db.Exec(`INSERT OR IGNORE INTO scene_intent_map(scene_id,intent_id,weight) VALUES(?,?,1)`, root.ID, intent)
			}
			if skill != "" {
				_, _ = db.Exec(`INSERT OR IGNORE INTO scene_skill_map(scene_id,skill_id,weight) VALUES(?,?,1)`, root.ID, skill)
			}
		}
		for _, child := range root.Children {
			childPatterns := subscenePatterns(child.ID, patterns)
			for _, pid := range childPatterns {
				if _, err := db.Exec(`INSERT OR IGNORE INTO scene_pattern_map(scene_id,pattern_id,weight) VALUES(?,?,?)`, child.ID, pid, .9); err != nil {
					return err
				}
				var intent, skill string
				_ = db.QueryRow(`SELECT intent_id FROM sentence_patterns WHERE id=?`, pid).Scan(&intent)
				_ = db.QueryRow(`SELECT skill_id FROM pattern_skills WHERE pattern_id=? ORDER BY weight DESC LIMIT 1`, pid).Scan(&skill)
				if intent != "" {
					_, _ = db.Exec(`INSERT OR IGNORE INTO scene_intent_map(scene_id,intent_id,weight) VALUES(?,?,1)`, child.ID, intent)
				}
				if skill != "" {
					_, _ = db.Exec(`INSERT OR IGNORE INTO scene_skill_map(scene_id,skill_id,weight) VALUES(?,?,1)`, child.ID, skill)
				}
			}
		}
	}
	// Older V2.2 catalog ids remain queryable in history, but no longer appear
	// as duplicate entry points in the Scenes page.
	legacy := []string{"school", "family", "interview", "presentation", "refusal"}
	for _, old := range legacy {
		_, _ = db.Exec(`UPDATE scenes SET enabled=0 WHERE id=? AND parent_id=''`, old)
	}
	for old, next := range map[string]string{"school": "daily", "family": "friends", "interview": "work", "presentation": "meeting"} {
		_, _ = db.Exec(`UPDATE exercises SET scene_id=? WHERE scene_id=?`, next, old)
	}
	_, _ = db.Exec(`UPDATE exercises SET scene_id='friends' WHERE scene_id='refusal'`)
	return nil
}

func filterExistingPatterns(patterns []string, valid map[string]bool) []string {
	out := []string{}
	for _, p := range patterns {
		if valid[p] {
			out = append(out, p)
		}
	}
	return out
}

func subscenePatterns(id string, root []string) []string {
	keys := strings.Fields(strings.ReplaceAll(id, "-", " "))
	var out []string
	for _, p := range root {
		if len(out) >= 5 {
			break
		}
		for _, key := range keys {
			if strings.Contains(p, key) || (key == "questions" && strings.Contains(p, "clarif")) || (key == "suggestions" && (strings.Contains(p, "suggest") || p == "lets")) || (key == "problems" && (p == "because" || p == "clarification" || p == "disagreement")) || (key == "opinions" && (p == "hedge" || p == "formal-opinion" || p == "disagreement")) {
				out = append(out, p)
				break
			}
		}
	}
	if len(out) == 0 {
		out = append(out, root...)
	}
	return out
}

type sceneConstraint struct {
	RootID, SubsceneID string
	PatternIDs         map[string]bool
	ScopeRelaxed       bool
	RelaxationReason   string
}

func (s *Server) sceneConstraint(selected string) (sceneConstraint, error) {
	c := sceneConstraint{PatternIDs: map[string]bool{}}
	if selected == "" {
		return c, nil
	}
	var parent, category string
	var enabled int
	err := s.db.QueryRow(`SELECT parent_id,category,enabled FROM scenes WHERE id=?`, selected).Scan(&parent, &category, &enabled)
	if err != nil {
		return c, err
	}
	if enabled == 0 {
		return c, fmt.Errorf("scene %q is not available", selected)
	}
	if parent == "" {
		c.RootID = selected
	} else {
		c.RootID, c.SubsceneID = parent, selected
	}
	ids := []string{selected}
	if parent == "" {
		rows, _ := s.db.Query(`SELECT id FROM scenes WHERE parent_id=? AND enabled=1`, selected)
		if rows != nil {
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				ids = append(ids, id)
			}
			rows.Close()
		}
	}
	if len(ids) > 0 {
		c.PatternIDs = s.patternIDsForScenes(ids)
	}
	if len(c.PatternIDs) == 0 && parent != "" {
		c.ScopeRelaxed, c.RelaxationReason = true, "subscene_candidate_shortage_parent_scene"
		c.PatternIDs = s.patternIDsForScenes([]string{parent})
	}
	if len(c.PatternIDs) == 0 {
		c.ScopeRelaxed, c.RelaxationReason = true, "scene_candidate_shortage_related_category"
		rows, _ := s.db.Query(`SELECT id FROM scenes WHERE parent_id='' AND category=? AND enabled=1`, category)
		var related []string
		if rows != nil {
			for rows.Next() {
				var id string
				_ = rows.Scan(&id)
				related = append(related, id)
			}
			rows.Close()
		}
		c.PatternIDs = s.patternIDsForScenes(related)
	}
	if len(c.PatternIDs) == 0 {
		c.ScopeRelaxed, c.RelaxationReason = true, "scene_candidate_shortage_global_fallback"
	}
	return c, nil
}

func (s *Server) patternIDsForScenes(sceneIDs []string) map[string]bool {
	out := map[string]bool{}
	if len(sceneIDs) == 0 {
		return out
	}
	marks := strings.TrimRight(strings.Repeat("?,", len(sceneIDs)), ",")
	args := make([]any, len(sceneIDs))
	for i, id := range sceneIDs {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT DISTINCT pattern_id FROM scene_pattern_map WHERE scene_id IN (`+marks+`)`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out[id] = true
	}
	return out
}

func (s *Server) sceneDetail(id string) (map[string]any, error) {
	var name, description, parent, category string
	var minD, maxD float64
	var enabled int
	if err := s.db.QueryRow(`SELECT name,description,parent_id,category,difficulty_min,difficulty_max,enabled FROM scenes WHERE id=?`, id).Scan(&name, &description, &parent, &category, &minD, &maxD, &enabled); err != nil {
		return nil, err
	}
	mastery := s.sceneMastery(id)
	type childRow struct{ id, name, description string }
	childRows := []childRow{}
	rows, _ := s.db.Query(`SELECT id,name,description FROM scenes WHERE parent_id=? AND enabled=1 ORDER BY sort_order,id`, id)
	if rows != nil {
		for rows.Next() {
			var cid, cn, cd string
			if rows.Scan(&cid, &cn, &cd) == nil {
				childRows = append(childRows, childRow{cid, cn, cd})
			}
		}
		rows.Close()
	}
	children := []map[string]any{}
	for _, child := range childRows {
		children = append(children, map[string]any{"id": child.id, "name": child.name, "description": child.description, "mastery": s.sceneMastery(child.id)})
	}
	return map[string]any{"id": id, "name": name, "description": description, "parent_id": parent, "category": category, "difficulty_min": minD, "difficulty_max": maxD, "enabled": enabled == 1, "mastery": mastery, "subscenes": children}, nil
}

func (s *Server) sceneMastery(id string) map[string]any {
	var attempts, correct, evidence int
	var mastery, recent, diff, confidence, pc, ic, transfer float64
	var last, state string
	err := s.db.QueryRow(`SELECT attempts,correct,mastery,recent_accuracy,effective_difficulty,evidence_count,confidence,pattern_coverage,intent_coverage,transfer,last_practiced,state FROM scene_mastery WHERE scene_id=?`, id).Scan(&attempts, &correct, &mastery, &recent, &diff, &evidence, &confidence, &pc, &ic, &transfer, &last, &state)
	if err == sql.ErrNoRows {
		state = "UNKNOWN"
	}
	if state == "" {
		state = "UNKNOWN"
	}
	return map[string]any{"mastery": mastery, "recent_accuracy": recent, "effective_difficulty": diff, "evidence_count": evidence, "confidence": confidence, "pattern_coverage": pc, "intent_coverage": ic, "transfer": transfer, "attempt_count": attempts, "correct_count": correct, "last_practiced": last, "state": state, "weak_subscene_count": s.weakSubsceneCount(id)}
}

func (s *Server) weakSubsceneCount(id string) int {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_mastery m JOIN scenes sc ON sc.id=m.scene_id WHERE sc.parent_id=? AND m.state='WEAK'`, id).Scan(&n)
	return n
}

func (s *Server) sceneDiagnostics() ([]map[string]any, error) {
	rows, err := s.db.Query(`SELECT id,name FROM scenes WHERE parent_id='' AND enabled=1 ORDER BY sort_order,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type diagnosticRow struct{ id, name string }
	var diagnosticRows []diagnosticRow
	for rows.Next() {
		var id, name string
		if rows.Scan(&id, &name) == nil {
			diagnosticRows = append(diagnosticRows, diagnosticRow{id, name})
		}
	}
	rows.Close()
	out := []map[string]any{}
	for _, row := range diagnosticRows {
		m := s.sceneMastery(row.id)
		m["scene_id"] = row.id
		m["name"] = row.name
		out = append(out, m)
	}
	return out, nil
}

func (s *Server) sceneCalibrationReport() map[string]any {
	var count, subcount, mapped, intents, skills, unknown, weak int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scenes WHERE parent_id='' AND enabled=1`).Scan(&count)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scenes WHERE parent_id<>'' AND enabled=1`).Scan(&subcount)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_pattern_map`).Scan(&mapped)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_intent_map`).Scan(&intents)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_skill_map`).Scan(&skills)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_mastery WHERE state='UNKNOWN'`).Scan(&unknown)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_mastery WHERE state='WEAK'`).Scan(&weak)
	var relaxed int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM exercises WHERE json_extract(decision_trace_json,'$.scope_relaxed')=1`).Scan(&relaxed)
	var transfer int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM scene_transfer_events`).Scan(&transfer)
	return map[string]any{"scene_count": count, "subscene_count": subcount, "scene_pattern_mappings": mapped, "scene_intent_mappings": intents, "scene_skill_mappings": skills, "scene_unknown_count": unknown, "scene_weak_count": weak, "scope_relaxation_count": relaxed, "transfer_events": transfer}
}

func updateSceneMasteryTx(tx *sql.Tx, pattern, scene string, difficulty float64, e Eval, attempt string) error {
	if scene == "" {
		return nil
	}
	var parent string
	_ = tx.QueryRow(`SELECT parent_id FROM scenes WHERE id=?`, scene).Scan(&parent)
	root := scene
	sub := ""
	if parent != "" {
		root, sub = parent, scene
	}
	targets := []string{root}
	if sub != "" {
		targets = append(targets, sub)
	}
	score := clamp(.45*e.PatternScore+.35*e.MeaningScore+.20*e.GrammarScore, 0, 1)
	ok := e.Verdict == "correct" || e.Verdict == "mostly_correct"
	now := time.Now().UTC().Format(time.RFC3339)
	for _, id := range targets {
		var a, cnt, evidence int
		var mastery, recent, eff, conf, pc, ic, tr float64
		var last, state string
		_ = tx.QueryRow(`SELECT attempts,correct,evidence_count,mastery,recent_accuracy,effective_difficulty,confidence,pattern_coverage,intent_coverage,transfer,last_practiced,state FROM scene_mastery WHERE scene_id=?`, id).Scan(&a, &cnt, &evidence, &mastery, &recent, &eff, &conf, &pc, &ic, &tr, &last, &state)
		a++
		evidence++
		if ok {
			cnt++
		}
		recent = score
		mastery = clamp(.7*mastery+.3*score, 0, 1)
		if evidence == 1 {
			mastery = score
		}
		if eff <= 0 {
			eff = difficulty
		} else {
			eff = .8*eff + .2*difficulty
		}
		var eligible, observed, eligibleIntent, observedIntent int
		_ = tx.QueryRow(`SELECT COUNT(*) FROM scene_pattern_map WHERE scene_id=?`, id).Scan(&eligible)
		_ = tx.QueryRow(`SELECT COUNT(DISTINCT e.pattern_id) FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' AND (e.scene_id=? OR e.subscene_id=?)`, id, id).Scan(&observed)
		_ = tx.QueryRow(`SELECT COUNT(*) FROM scene_intent_map WHERE scene_id=?`, id).Scan(&eligibleIntent)
		_ = tx.QueryRow(`SELECT COUNT(DISTINCT e.intent_id) FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' AND (e.scene_id=? OR e.subscene_id=?)`, id, id).Scan(&observedIntent)
		if eligible > 0 {
			pc = float64(observed) / float64(eligible)
		}
		if eligibleIntent > 0 {
			ic = float64(observedIntent) / float64(eligibleIntent)
		}
		conf = minConfidence(evidence)
		state = "LEARNING"
		if evidence == 0 {
			state = "UNKNOWN"
		} else if mastery < .55 {
			state = "WEAK"
		} else if mastery >= .8 && evidence >= 3 {
			state = "MASTERED"
		} else if mastery >= .65 {
			state = "STABLE"
		}
		_, err := tx.Exec(`INSERT INTO scene_mastery(scene_id,attempts,correct,mastery,last_practiced,recent_accuracy,effective_difficulty,evidence_count,confidence,pattern_coverage,intent_coverage,transfer,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(scene_id) DO UPDATE SET attempts=excluded.attempts,correct=excluded.correct,mastery=excluded.mastery,last_practiced=excluded.last_practiced,recent_accuracy=excluded.recent_accuracy,effective_difficulty=excluded.effective_difficulty,evidence_count=excluded.evidence_count,confidence=excluded.confidence,pattern_coverage=excluded.pattern_coverage,intent_coverage=excluded.intent_coverage,transfer=excluded.transfer,state=excluded.state`, id, a, cnt, mastery, now, recent, eff, evidence, conf, pc, ic, tr, state)
		if err != nil {
			return err
		}
	}
	return updateTransferTx(tx, attempt, pattern, scene, now)
}

func updateTransferTx(tx *sql.Tx, attempt, pattern, scene, now string) error {
	if scene == "" {
		return nil
	}
	var before float64
	_ = tx.QueryRow(`SELECT COALESCE(transfer,0) FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, pattern).Scan(&before)
	var scenes, intents, successes int
	_ = tx.QueryRow(`SELECT COUNT(DISTINCT e.scene_id) FROM attempts a JOIN exercises e ON e.id=a.exercise_id JOIN evaluations v ON v.attempt_id=a.id WHERE e.pattern_id=? AND v.verdict IN ('correct','mostly_correct')`, pattern).Scan(&scenes)
	_ = tx.QueryRow(`SELECT COUNT(DISTINCT e.intent_id) FROM attempts a JOIN exercises e ON e.id=a.exercise_id JOIN evaluations v ON v.attempt_id=a.id WHERE e.pattern_id=? AND v.verdict IN ('correct','mostly_correct')`, pattern).Scan(&intents)
	_ = tx.QueryRow(`SELECT COUNT(*) FROM evaluations v JOIN attempts a ON a.id=v.attempt_id JOIN exercises e ON e.id=a.exercise_id WHERE e.pattern_id=? AND v.verdict IN ('correct','mostly_correct')`, pattern).Scan(&successes)
	if successes == 0 {
		return nil
	}
	after := clamp(.45*minFloat(1, float64(scenes)/3)+.25*minFloat(1, float64(intents)/3)+.30*minFloat(1, float64(successes)/5), 0, 1)
	if _, err := tx.Exec(`UPDATE learner_skill_state SET transfer=?,updated_at=? WHERE user_id='default' AND pattern_id=?`, after, now, pattern); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE scene_mastery SET transfer=? WHERE scene_id=? OR scene_id=(SELECT parent_id FROM scenes WHERE id=?)`, after, scene, scene); err != nil {
		return err
	}
	if scenes > 0 && (before != after || scenes == 1) {
		_, err := tx.Exec(`INSERT INTO scene_transfer_events(id,pattern_id,previous_scene_count,new_scene_id,transfer_before,transfer_after,intent_count,created_at) VALUES(?,?,?,?,?,?,?,?)`, id("transfer"), pattern, maxInt(0, scenes-1), scene, before, after, intents, now)
		return err
	}
	return nil
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func sceneContextLabel(db *sql.DB, root, sub string) string {
	var name, child string
	_ = db.QueryRow(`SELECT name FROM scenes WHERE id=?`, root).Scan(&name)
	if sub != "" {
		_ = db.QueryRow(`SELECT name FROM scenes WHERE id=?`, sub).Scan(&child)
	}
	if child != "" {
		return name + " / " + child
	}
	if name != "" {
		return name
	}
	return root
}

func decodeSceneTrace(raw string) map[string]any {
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

var _ = sort.Strings

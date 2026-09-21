package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAdaptiveSelectionInterleavesPatterns(t *testing.T) {
	s := testServer(t)
	seen := map[string]bool{}
	var previous string
	for i := 0; i < 12; i++ {
		ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
		if err != nil {
			t.Fatal(err)
		}
		pattern := ex["pattern_id"].(string)
		if pattern == previous {
			t.Fatalf("pattern repeated consecutively at step %d: %s", i, pattern)
		}
		previous = pattern
		seen[pattern] = true
		result, err := s.submitAttempt(context.Background(), "adaptive-test", ex["exercise_id"].(string), "I might be a little late today.")
		if err != nil || result["evaluation_status"] != "validated" {
			t.Fatalf("step %d result=%#v err=%v", i, result, err)
		}
	}
	if len(seen) < 4 {
		t.Fatalf("adaptive selector did not interleave enough patterns: %v", seen)
	}
}

func TestAssessmentAnchorsAndStateRebuild(t *testing.T) {
	s := testServer(t)
	expected := []string{"going-to", "modal-possibility", "because"}
	for _, want := range expected {
		ex, err := s.generateExercise(context.Background(), 3, "assessment", "")
		if err != nil {
			t.Fatal(err)
		}
		if ex["pattern_id"] != want {
			t.Fatalf("anchor=%v want=%s", ex["pattern_id"], want)
		}
		result, err := s.submitAttempt(context.Background(), "assessment", ex["exercise_id"].(string), "I might be a little late today.")
		if err != nil || result["evaluation_status"] != "validated" {
			t.Fatalf("assessment result=%#v err=%v", result, err)
		}
	}
	var before int
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(attempt_count),0) FROM learner_skill_state`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := s.rebuildLearnerState(); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(attempt_count),0) FROM learner_skill_state`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("rebuild changed attempt count: before=%d after=%d", before, after)
	}
}

func TestDueReviewGetsPriorityAfterInterleaving(t *testing.T) {
	s := testServer(t)
	first, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.submitAttempt(context.Background(), "review-test", first["exercise_id"].(string), "I might be a little late today."); err != nil {
		t.Fatal(err)
	}
	second, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.submitAttempt(context.Background(), "review-test", second["exercise_id"].(string), "I might be a little late today."); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE review_schedule SET due_at=? WHERE pattern_id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), first["pattern_id"]); err != nil {
		t.Fatal(err)
	}
	next, err := s.generateExercise(context.Background(), 3, "review", "")
	if err != nil {
		t.Fatal(err)
	}
	if next["pattern_id"] != first["pattern_id"] || next["is_review"] != true {
		t.Fatalf("due review was not selected: %#v", next)
	}
}

func TestLongRunningAdaptiveSimulation(t *testing.T) {
	s := testServer(t)
	weak := map[string]bool{"conditional": true, "polite-refusal": true}
	counts := map[string]int{}
	var previousPattern, previousPrompt string
	for i := 0; i < 100; i++ {
		ex, err := s.generateExercise(context.Background(), 3, "adaptive", "")
		if err != nil {
			t.Fatal(err)
		}
		pattern := ex["pattern_id"].(string)
		prompt := ex["chinese_prompt"].(string)
		if strings.Contains(prompt, "\u573a\u666f\u53d8\u4f53") {
			t.Fatalf("internal scene variation marker leaked into prompt at attempt %d: %s", i, prompt)
		}
		if pattern == previousPattern {
			t.Fatalf("consecutive pattern at attempt %d: %s", i, pattern)
		}
		if prompt == previousPrompt {
			t.Fatalf("consecutive exact prompt at attempt %d", i)
		}
		previousPattern, previousPrompt = pattern, prompt
		counts[pattern]++
		answer := "I might be a little late today."
		if weak[pattern] && i%3 != 0 {
			answer = "x"
		}
		result, err := s.submitAttempt(context.Background(), "simulation", ex["exercise_id"].(string), answer)
		if err != nil || result["evaluation_status"] != "validated" {
			t.Fatalf("attempt %d result=%#v err=%v", i, result, err)
		}
	}
	if counts["conditional"] == 0 || counts["polite-refusal"] == 0 {
		t.Fatalf("weak skills never appeared: %v", counts)
	}
	var reviews, probes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM review_schedule`).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE is_probe=1`).Scan(&probes); err != nil {
		t.Fatal(err)
	}
	if reviews == 0 || probes == 0 {
		t.Fatalf("memory/probe state missing: reviews=%d probes=%d", reviews, probes)
	}
}

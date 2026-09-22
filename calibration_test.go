package main

import (
	"context"
	"testing"
)

func TestCurriculumCalibrationSeparatesUnknownFromWeak(t *testing.T) {
	s := testServer(t)
	unknown, weak := s.unknownAndWeakCounts()
	if unknown != len(patternCatalog()) || weak != 0 {
		t.Fatalf("initial curriculum state unknown=%d weak=%d catalog=%d", unknown, weak, len(patternCatalog()))
	}
	if _, err := s.generateExercise(context.Background(), 3, "weak", ""); err == nil {
		t.Fatal("weak mode selected an unobserved curriculum pattern")
	}
	if _, err := s.generateExercise(context.Background(), 3, "review", ""); err == nil {
		t.Fatal("review mode selected an unobserved curriculum pattern")
	}
}

func TestCurriculumCalibrationCatalogReport(t *testing.T) {
	s := testServer(t)
	report, err := s.curriculumCalibrationReport()
	if err != nil {
		t.Fatal(err)
	}
	if report.CatalogPatterns != 44 || report.DifficultyRange[0] < 1 || report.DifficultyRange[1] < 6 {
		t.Fatalf("catalog audit=%+v", report)
	}
	if len(report.AssessmentAnchors) < 8 {
		t.Fatalf("assessment anchors too narrow: %d", len(report.AssessmentAnchors))
	}
	anchorBands := map[string]bool{}
	for _, anchor := range report.AssessmentAnchors {
		anchorBands[difficultyBand(anchor.Difficulty)] = true
	}
	if len(anchorBands) < 4 {
		t.Fatalf("anchors do not span enough bands: %v", anchorBands)
	}
	if !report.SkillGraph.Reachable || len(report.SkillGraph.OrphanSkills) != 0 || len(report.SkillGraph.UnreachableSkills) != 0 {
		t.Fatalf("skill graph audit=%+v", report.SkillGraph)
	}
	for pattern, seeds := range report.FallbackCoverage {
		if seeds < 3 || !report.GeneratorCoverage[pattern] {
			t.Fatalf("fallback coverage %s=%d generator=%v", pattern, seeds, report.GeneratorCoverage[pattern])
		}
	}
}

func TestExistingHistoryMigrationDoesNotInventNewEvidence(t *testing.T) {
	s := testServer(t)
	if _, err := s.db.Exec(`UPDATE pattern_mastery SET attempts=12,correct=10,mastery=.82 WHERE pattern_id='going-to'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM learner_skill_state`); err != nil {
		t.Fatal(err)
	}
	if err := seedAdaptiveData(s.db); err != nil {
		t.Fatal(err)
	}
	var attempts, evidence int
	var state string
	if err := s.db.QueryRow(`SELECT attempt_count,evidence_count,state FROM learner_skill_state WHERE pattern_id='going-to'`).Scan(&attempts, &evidence, &state); err != nil {
		t.Fatal(err)
	}
	if attempts != 12 || evidence != 12 || state == stateUnknown {
		t.Fatalf("existing history was lost: attempts=%d evidence=%d state=%s", attempts, evidence, state)
	}
	var newAttempts, newEvidence int
	if err := s.db.QueryRow(`SELECT attempt_count,evidence_count FROM learner_skill_state WHERE pattern_id='formal-opinion'`).Scan(&newAttempts, &newEvidence); err != nil {
		t.Fatal(err)
	}
	if newAttempts != 0 || newEvidence != 0 {
		t.Fatalf("new pattern received historical evidence: attempts=%d evidence=%d", newAttempts, newEvidence)
	}
	var reviews int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM review_schedule WHERE pattern_id='formal-opinion'`).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Fatalf("new pattern received an immediate review: %d", reviews)
	}
}

func TestStrongAdultAssessmentDoesNotRestartAtFoundations(t *testing.T) {
	s := testServer(t)
	if _, err := s.db.Exec(`UPDATE user_profile SET global_difficulty=5.2 WHERE id='default'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE learner_skill_state SET attempt_count=5,success_count=5,evidence_count=5,mastery=.9,retention=.9,state='MASTERED' WHERE pattern_id IN (SELECT id FROM sentence_patterns WHERE difficulty>=4.0)`); err != nil {
		t.Fatal(err)
	}
	ex, err := s.generateExercise(context.Background(), 5.2, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	if ex["difficulty"].(float64) < 4.0 {
		t.Fatalf("strong learner restarted at a foundation: %#v", ex)
	}
}

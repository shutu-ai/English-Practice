package main

import (
	"context"
	"testing"
)

func TestCurriculumMetadataCoversAllBuiltInPatterns(t *testing.T) {
	if err := validateCurriculumCatalog(); err != nil {
		t.Fatal(err)
	}
	if got, want := len(curriculumPatternMap()), len(patternCatalog()); got != want {
		t.Fatalf("curriculum pattern coverage=%d want=%d", got, want)
	}
	for _, p := range patternCatalog() {
		metadata, ok := curriculumPattern(p.id)
		if !ok || metadata.AppLevelMin < 1 || metadata.AppLevelMin > 8 || metadata.CEFRAnchor == "" || metadata.GrammarFamily == "" || metadata.Confidence == "" {
			t.Fatalf("incomplete metadata for %s: %#v", p.id, metadata)
		}
	}
}

func TestCurriculumWouldYouMindBoundary(t *testing.T) {
	if ok, _ := curriculumEligible("would-you-mind", 1); ok {
		t.Fatal("would-you-mind must not be eligible at D1")
	}
	if ok, _ := curriculumEligible("would-you-mind", 4); !ok {
		t.Fatal("would-you-mind should be eligible at D4")
	}
	validator := CurriculumComplianceValidator{}
	if result := validator.Validate(1, "would-you-mind", "请表达一个简单请求"); result.Passed {
		t.Fatalf("D1 validator accepted advanced target: %#v", result)
	}
	if result := validator.Validate(4, "would-you-mind", "请请求对方把音量调小"); !result.Passed {
		t.Fatalf("D4 validator rejected mapped target: %#v", result)
	}
}

func TestCurriculumGraphAndEnvelope(t *testing.T) {
	diagnostics := curriculumGraphDiagnostics()
	if diagnostics["validation"] != "VALIDATED" || diagnostics["cycles"] != 0 || diagnostics["invalid_edges"] != 0 {
		t.Fatalf("invalid curriculum graph: %#v", diagnostics)
	}
	envelope := curriculumEnvelope(1)
	if envelope.Version != curriculumVersion || envelope.Level != 1 {
		t.Fatalf("unexpected D1 envelope: %#v", envelope)
	}
	for _, grammar := range []string{"be", "have", "like", "can"} {
		found := false
		for _, allowed := range envelope.AllowedGrammar {
			if allowed == grammar {
				found = true
			}
		}
		if !found {
			t.Fatalf("D1 envelope missing %s: %#v", grammar, envelope.AllowedGrammar)
		}
	}
	for _, grammar := range envelope.AllowedGrammar {
		if grammar == "would" || grammar == "mixed_conditionals" {
			t.Fatalf("D1 envelope leaked advanced grammar %s", grammar)
		}
	}
}

func TestCurriculumSeedAndSelectionGate(t *testing.T) {
	s := testServer(t)
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM curriculum_patterns`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(patternCatalog()) {
		t.Fatalf("seeded curriculum patterns=%d want=%d", count, len(patternCatalog()))
	}
	exercise, err := s.generateExercise(context.Background(), 1, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	patternID, _ := exercise["pattern_id"].(string)
	if ok, _ := curriculumEligible(patternID, 1); !ok {
		t.Fatalf("D1 selector returned ineligible pattern %q", patternID)
	}
	if exercise["curriculum_version"] != curriculumVersion {
		t.Fatalf("exercise missing curriculum version: %#v", exercise)
	}
}

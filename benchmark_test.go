package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestCommonLifeTaxonomyIsVersionedAndUnique(t *testing.T) {
	taxonomy := commonLifeTaxonomy()
	if taxonomy.Version != benchmarkTaxonomyVersion || len(taxonomy.Capabilities) < 13 {
		t.Fatalf("taxonomy coverage is incomplete: %#v", taxonomy)
	}
	seen := map[string]bool{}
	weight := 0
	for _, capability := range taxonomy.Capabilities {
		if capability.ID == "" || seen[capability.ID] || capability.Weight < 1 {
			t.Fatalf("invalid or duplicate capability: %#v", capability)
		}
		seen[capability.ID] = true
		weight += capability.Weight
	}
	if weight <= len(taxonomy.Capabilities) {
		t.Fatalf("frequency weights were not applied: %d", weight)
	}
	intentSeen := map[string]bool{}
	for _, intent := range taxonomy.Intents {
		if intent.ID == "" || intentSeen[intent.ID] || intent.Weight < 1 {
			t.Fatalf("invalid or duplicate intent: %#v", intent)
		}
		intentSeen[intent.ID] = true
	}
}

func TestCurriculumCoverageUsesIndependentJudgeOutput(t *testing.T) {
	taxonomy := commonLifeTaxonomy()
	judge := IndependentCurriculumJudge{Taxonomy: taxonomy}
	result := judge.Judge(CurriculumJudgeInput{ChinesePrompt: "In the Shopping context, ask about the price and compare options.", ReferenceAnswers: []string{"Could I compare the price of these options?"}})
	if result.CommonLifeCapability != "shopping" || result.CommunicationIntent == "" || result.Confidence <= 0 {
		t.Fatalf("independent curriculum judge did not classify the public input: %#v", result)
	}
	corpus := benchmarkCorpus(BenchmarkConfig{Samples: 500})
	report := runCurriculumBenchmark(corpus)
	if report.RawCapabilityCoverage != 1 || report.WeightedCoverage != 1 || report.HighFrequencyCoverage != 1 {
		t.Fatalf("balanced benchmark corpus lost coverage: %#v", report)
	}
}

func TestDifficultyJudgeDoesNotReceiveProductionDifficultyFields(t *testing.T) {
	input := DifficultyJudgeInput{ChinesePrompt: "In a work meeting, explain a change politely.", ExpectedTask: "Disagree politely; language structure: I see your point, but ...", ReferenceAnswers: []string{"I see your point, but I am concerned that the change could affect quality."}}
	result := (IndependentDifficultyJudge{}).Judge(input)
	if result.Overall < 1 || result.Overall > 8 || result.Confidence <= 0 {
		t.Fatalf("invalid independent difficulty result: %#v", result)
	}
	encoded, _ := json.Marshal(input)
	for _, forbidden := range []string{"target_difficulty", "realized_difficulty", "catalog_difficulty", "learner_ability"} {
		if string(encoded) != "" && containsString(string(encoded), forbidden) {
			t.Fatalf("production difficulty field leaked into judge input: %s", forbidden)
		}
	}
	if got := languageStructureEstimate("condition; language structure: If ..., I'll ..."); math.Abs(got-4) > .001 {
		t.Fatalf("language structure prior is not independent/calibrated: %v", got)
	}
}

func TestAdaptiveBenchmarkHidesTrueAbilityAndIsDeterministic(t *testing.T) {
	cfg := BenchmarkConfig{AdaptiveAttempts: 1000, Seed: 42}
	a := runAdaptiveBenchmark(cfg)
	b := runAdaptiveBenchmark(cfg)
	if len(a.Personas) != 8 || a.GlobalMAE > .4 || a.PatternMAE > .6 || a.SceneMAE > .6 || a.ProductiveZone < .7 || a.FalseWeakRate > .1 {
		t.Fatalf("adaptive benchmark missed acceptance targets: %#v", a)
	}
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	if string(left) != string(right) {
		t.Fatal("adaptive benchmark is not deterministic")
	}
	for _, persona := range a.Personas {
		if persona.ProductiveZoneRate < .7 || persona.ProbeRatio <= 0 || persona.TimeToConvergence <= 0 {
			t.Fatalf("adaptive persona metrics incomplete: %#v", persona)
		}
	}
}

func containsString(value, needle string) bool {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

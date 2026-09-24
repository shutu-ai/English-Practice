package main

import "testing"

func TestCurriculumLiveJudgeAcceptsCommonProviderEnvelope(t *testing.T) {
	judge, err := parseCurriculumLiveJudge(`{"result":{"estimated_cefr_band":"A2","productive_complexity":"42","grammar":"simple past","communication":"narrative","confidence":"80%"}}`)
	if err != nil {
		t.Fatal(err)
	}
	if judge.EstimatedCEFR != "A2" || judge.ProductiveComplexity != .42 || judge.Confidence != .8 || len(judge.GrammarFeatures) != 1 {
		t.Fatalf("unexpected normalized judge: %#v", judge)
	}
}

func TestGeneratorSpecKeepsApplicationCurriculumLevel(t *testing.T) {
	exercise := SimulationExercise{ID: "v251", PatternID: "would-you-mind", Pattern: "Would you mind ...?", Difficulty: 4, CurriculumLevel: 4}
	spec := generationSpecFromExercise(exercise)
	if spec.CurriculumLevel != 4 {
		t.Fatalf("curriculum level was not application-owned: %#v", spec)
	}
	if check := (CurriculumComplianceValidator{}).Validate(1, "would-you-mind", "请表达一个简单请求"); check.Passed {
		t.Fatal("D1 would-you-mind target was not blocked before provider")
	}
}

func TestCurriculumSeriousMismatchUsesAdjacentBandTolerance(t *testing.T) {
	if curriculumSeriousMismatch("approaching B2 productive expression", "B2") {
		t.Fatal("same B2 band was classified as a serious mismatch")
	}
	if !curriculumSeriousMismatch("Pre-A1 / early A1", "A2") {
		t.Fatal("two-band A1 to A2 mismatch was not classified")
	}
}

func TestReferenceAnswerSupportsFirstConditionalWithCan(t *testing.T) {
	if !referenceAnswerSupportsPattern("if-first", "If you finish early, you can call me.") {
		t.Fatal("valid first conditional using can was rejected")
	}
	if !referenceAnswerSupportsPattern("if-first", "If you finish early, I'll call you.") {
		t.Fatal("valid first conditional using I'll was rejected")
	}
	if referenceAnswerSupportsPattern("if-second", "If you finish early, you can call me.") {
		t.Fatal("first conditional was accepted as second conditional")
	}
	if !referenceAnswerSupportsPattern("unless", "Unless you hurry, we miss the bus.") {
		t.Fatal("valid unless clause with a present-tense result was rejected")
	}
}

func TestLiveEvidenceCurriculumMappingOverridesAreNarrow(t *testing.T) {
	need, ok := curriculumPattern("need")
	if !ok || need.AppLevelMin != 2 {
		t.Fatalf("need mapping was not reclassified narrowly to D2: %#v", need)
	}
	wouldYouMind, ok := curriculumPattern("would-you-mind")
	if !ok || wouldYouMind.AppLevelMin != 4 {
		t.Fatalf("would-you-mind mapping changed unexpectedly: %#v", wouldYouMind)
	}
}

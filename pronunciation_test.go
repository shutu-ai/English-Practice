package main

import "testing"

func TestEvaluationOptionalNaturalnessFieldsAreBackwardCompatible(t *testing.T) {
	eval, err := normalizeEvalContent(`{"verdict":"correct","meaning_score":0.98,"grammar_score":0.98,"naturalness_score":0.96,"pattern_score":0.95,"errors":[],"suggested_answer":"I didn't go because I didn't feel well.","more_natural_needed":false,"more_natural":"","alternative":"I stayed home because I wasn't feeling well.","explanation_zh":"Natural."}`)
	if err != nil {
		t.Fatal(err)
	}
	if eval.MoreNaturalNeeded || eval.MoreNatural != "" || eval.Alternative == "" {
		t.Fatalf("unexpected optional evaluation fields: %#v", eval)
	}
	legacy, err := normalizeEvalContent(validEvaluationJSON())
	if err != nil {
		t.Fatal(err)
	}
	if legacy.MoreNaturalNeeded || legacy.MoreNatural != "" || legacy.Alternative != "" {
		t.Fatalf("legacy evaluation was not preserved: %#v", legacy)
	}
}

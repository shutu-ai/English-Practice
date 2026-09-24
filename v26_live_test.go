package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type countingLLMClient struct{ calls int }

func (c *countingLLMClient) Chat(context.Context, ChatRequest) (*ChatResponse, error) {
	c.calls++
	return &ChatResponse{}, nil
}

func TestV26ProviderCallLimiterStopsBeforeExceedingBound(t *testing.T) {
	inner := &countingLLMClient{}
	limiter := &providerCallLimiter{maximum: 2}
	client := limitedLLMClient{inner: inner, limiter: limiter}

	for i := 0; i < 3; i++ {
		_, err := client.Chat(context.Background(), ChatRequest{})
		if i < 2 && err != nil {
			t.Fatalf("call %d unexpectedly failed: %v", i+1, err)
		}
		if i == 2 && (err == nil || err.Error() != "V2.6 live acceptance provider call limit reached") {
			t.Fatalf("call past the cap should be denied, got %v", err)
		}
	}
	allowed, denied := limiter.counts()
	if inner.calls != 2 || allowed != 2 || denied != 1 {
		t.Fatalf("calls=%d allowed=%d denied=%d, want 2/2/1", inner.calls, allowed, denied)
	}
}

func TestV26AlternativeAcceptanceRequiresTenAcceptedWithNoRejections(t *testing.T) {
	if !v26AlternativeAcceptancePassed(10, 10, 10, 0) {
		t.Fatal("10 of 10 accepted alternatives should pass")
	}
	if v26AlternativeAcceptancePassed(10, 10, 9, 1) {
		t.Fatal("one rejected alternative must fail the false-rejection gate")
	}
	if v26AlternativeAcceptancePassed(8, 10, 8, 0) {
		t.Fatal("fewer than 10 available pairs must fail the alternative gate")
	}
}

func TestV26RegistryAppliesSharedProviderCallLimiter(t *testing.T) {
	limiter := &providerCallLimiter{maximum: 1}
	registry := &LLMRegistry{callLimiter: limiter}
	client := registry.Client(ProviderConfig{Type: "openai-compatible"})
	limited, ok := client.(limitedLLMClient)
	if !ok || limited.limiter != limiter {
		t.Fatal("registry client does not share the V2.6 limiter")
	}
}

func TestV26LiveReportIncludesCallsFailuresAndDBSnapshots(t *testing.T) {
	dir := t.TempDir()
	before := &V241DatabaseSnapshot{Tables: map[string]int{"attempts": 7}}
	after := &V241DatabaseSnapshot{Tables: map[string]int{"attempts": 7}}
	report := v26LiveReport{
		Provider: "provider", Model: "model", TotalProviderCalls: 3, DeniedCalls: 1,
		ProductionBefore: before, ProductionAfter: after,
		Scenarios: []v26LiveScenario{{
			DifficultyMode: DifficultyModeFixed, TrainingFocus: TrainingFocusFree,
			Requested: 1, Generated: 1, GeneratorInitialSuccesses: 1,
			EvaluatorCalls: 2, EvaluatorFailures: 1,
			EvaluatorFailureKinds: map[string]int{"invalid_structured_output": 1},
			EvaluatorSchemaErrors: map[string]int{"missing required field verdict": 1},
			Samples:               []v26LiveSample{{Scenario: "fixed-free", AlternativeTested: true, AlternativeAccepted: false, AlternativeVerdict: "incorrect"}},
		}},
	}
	jsonPath := filepath.Join(dir, "report.json")
	mdPath := filepath.Join(dir, "report.md")
	if err := writeV26LiveReport(jsonPath, mdPath, report); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(mdPath)
	if err != nil {
		t.Fatal(err)
	}
	markdown := string(data)
	for _, expected := range []string{"Provider calls: 3; denied at cap: 1", "invalid_structured_output", "missing required field verdict", "| attempts | 7 | 7 |", "## Alternative Answer Evaluations", "fixed-free | false | 0 | incorrect"} {
		if !strings.Contains(markdown, expected) {
			t.Errorf("report missing %q", expected)
		}
	}
}

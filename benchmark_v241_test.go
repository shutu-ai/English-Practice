package main

import (
	"context"
	"strings"
	"testing"
)

type v241TestClient struct {
	Response string
	Requests []ChatRequest
}

func (c *v241TestClient) Chat(_ context.Context, request ChatRequest) (*ChatResponse, error) {
	c.Requests = append(c.Requests, request)
	return &ChatResponse{Content: c.Response}, nil
}

func TestV241WeakAuditIsDeterministicAndGrounded(t *testing.T) {
	a := runV241WeakAudit(2000, 42)
	b := runV241WeakAudit(2000, 42)
	if len(a.Personas) != 3 || a.PatternMetrics.TP == 0 || a.PatternMetrics.TN == 0 {
		t.Fatalf("weak audit did not produce a usable confusion matrix: %#v", a.PatternMetrics)
	}
	if a.PatternMetrics != b.PatternMetrics || a.SceneMetrics != b.SceneMetrics {
		t.Fatalf("weak audit is not deterministic: %#v %#v", a.PatternMetrics, b.PatternMetrics)
	}
	for _, persona := range a.Personas {
		if len(persona.TrueWeakPatterns) < 3 || persona.Attempts != 2000 {
			t.Fatalf("dedicated weak persona is incomplete: %#v", persona)
		}
	}
}

func TestV241LiveJudgesReceiveBlindInputs(t *testing.T) {
	curriculum := &v241TestClient{Response: `{"scene":"work","communication_intent":"clarification","language_function":"professional communication","common_life_capability_id":"work","confidence":0.9}`}
	if _, err := v241LiveCurriculumJudge(context.Background(), curriculum, "请在工作场景中澄清一个安排。", []string{"Could you clarify the schedule?"}, "", 128); err != nil {
		t.Fatal(err)
	}
	difficulty := &v241TestClient{Response: `{"overall":4,"confidence":0.8}`}
	if _, err := v241LiveDifficultyJudge(context.Background(), difficulty, "请在工作场景中澄清一个安排。", []string{"Could you clarify the schedule?"}, 128); err != nil {
		t.Fatal(err)
	}
	for _, client := range []*v241TestClient{curriculum, difficulty} {
		if len(client.Requests) != 1 {
			t.Fatalf("expected exactly one independent judge call: %d", len(client.Requests))
		}
		prompt := client.Requests[0].Messages[1].Content
		for _, leaked := range []string{"4.203", "6.677", "pattern_id: conditional", "learner ability: 5"} {
			if strings.Contains(prompt, leaked) {
				t.Fatalf("live judge input leaked production value %q: %s", leaked, prompt)
			}
		}
	}
}

func TestV241CoverageSeparatesLiveAndBalancedMetrics(t *testing.T) {
	if v241Version == benchmarkVersion {
		t.Fatalf("V2.4.1 must remain a separate audit version")
	}
	if got := v241Duplicates([]string{"same", "same", "different"}).ExactRate; got <= 0 {
		t.Fatalf("duplicate audit did not detect exact duplicates: %v", got)
	}
}

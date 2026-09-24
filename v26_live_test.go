package main

import (
	"context"
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

func TestV26RegistryAppliesSharedProviderCallLimiter(t *testing.T) {
	limiter := &providerCallLimiter{maximum: 1}
	registry := &LLMRegistry{callLimiter: limiter}
	client := registry.Client(ProviderConfig{Type: "openai-compatible"})
	limited, ok := client.(limitedLLMClient)
	if !ok || limited.limiter != limiter {
		t.Fatal("registry client does not share the V2.6 limiter")
	}
}

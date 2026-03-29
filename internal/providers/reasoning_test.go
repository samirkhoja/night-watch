package providers

import "testing"

func TestApplyOpenAIOptionsIncludesReasoningEffort(t *testing.T) {
	payload := openAIResponsesRequest{}
	applyOpenAIOptions(&payload, "gpt-5.4", "high", map[string]any{
		"temperature":       0.7,
		"max_output_tokens": 256,
	})

	if payload.Reasoning == nil || payload.Reasoning.Effort != "high" {
		t.Fatalf("expected openai reasoning effort to be preserved, got %#v", payload.Reasoning)
	}
	if payload.Temperature != nil {
		t.Fatalf("expected GPT-5 payload to omit temperature, got %v", *payload.Temperature)
	}
	if payload.MaxOutputTokens == nil || *payload.MaxOutputTokens != 256 {
		t.Fatalf("expected max output tokens to be preserved")
	}
}

func TestApplyAnthropicOptionsAddsThinkingBudget(t *testing.T) {
	payload := anthropicMessageRequest{}
	applyAnthropicOptions(&payload, "high", map[string]any{
		"max_output_tokens": 300,
	})

	if payload.Thinking == nil || payload.Thinking.BudgetTokens != 1536 {
		t.Fatalf("expected anthropic thinking budget for high effort, got %#v", payload.Thinking)
	}
	if payload.MaxTokens != 1792 {
		t.Fatalf("expected max tokens to be raised above thinking budget, got %d", payload.MaxTokens)
	}
}

func TestGeminiConvertGenerationConfigAddsThinkingBudget(t *testing.T) {
	cfg := geminiConvertGenerationConfig("low", map[string]any{
		"max_output_tokens": 123,
	})

	if cfg == nil || cfg.ThinkingConfig == nil {
		t.Fatalf("expected gemini generation config with thinking budget")
	}
	if cfg.ThinkingConfig.ThinkingBudget != 256 {
		t.Fatalf("expected low reasoning thinking budget, got %d", cfg.ThinkingConfig.ThinkingBudget)
	}
	if cfg.MaxOutputTokens == nil || *cfg.MaxOutputTokens != 123 {
		t.Fatalf("expected max output tokens to round-trip")
	}
}

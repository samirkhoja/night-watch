package llm

import (
	"testing"

	"github.com/samirkhoja/night-watch/internal/config"
)

func TestCompactionModelForProvider(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{
			name: "openai gpt-5 uses mini",
			cfg: config.Config{
				LLMProvider: "openai",
				LLMModel:    "gpt-5.2",
			},
			want: "gpt-5-mini",
		},
		{
			name: "openai always uses gpt-5-mini",
			cfg: config.Config{
				LLMProvider: "openai",
				LLMModel:    "gpt-4o",
			},
			want: "gpt-5-mini",
		},
		{
			name: "anthropic always uses haiku",
			cfg: config.Config{
				LLMProvider: "anthropic",
				LLMModel:    "claude-opus-4-5",
			},
			want: "claude-haiku-4-5",
		},
		{
			name: "google always uses flash",
			cfg: config.Config{
				LLMProvider: "google",
				LLMModel:    "gemini-3.0-pro",
			},
			want: "gemini-3.0-flash",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := compactionModelForProvider(tc.cfg)
			if got != tc.want {
				t.Fatalf("compactionModelForProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}

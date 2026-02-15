package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/samirkhoja/night-watch/internal/config"
)

type Message struct {
	Role    string
	Content string
}

type GenerateRequest struct {
	System       string
	Messages     []Message
	Temperature  float64
	MaxTokens    int
	DisableTools bool
}

type ToolCall struct {
	Name      string
	Arguments map[string]interface{}
}

type GenerateResponse struct {
	Reply     string
	Reasoning []string
	ToolCalls []ToolCall
}

type Client interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	Name() string
}

func NewClient(cfg config.Config, cfgManager *config.Manager) (Client, error) {
	switch strings.ToLower(cfg.LLMProvider) {
	case "openai":
		return newOpenAIClient(cfg, cfgManager)
	case "anthropic":
		return newAnthropicClient(cfg, cfgManager)
	case "google":
		return newGoogleClient(cfg, cfgManager)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLMProvider)
	}
}

func NewCompactionClient(cfg config.Config, cfgManager *config.Manager) (Client, string, error) {
	compactCfg := cfg
	compactCfg.LLMModel = compactionModelForProvider(cfg)
	client, err := NewClient(compactCfg, cfgManager)
	if err != nil {
		return nil, "", err
	}
	return client, compactCfg.LLMModel, nil
}

func compactionModelForProvider(cfg config.Config) string {
	provider := strings.ToLower(strings.TrimSpace(cfg.LLMProvider))

	switch provider {
	case "openai":
		return "gpt-5-mini"
	case "anthropic":
		return "claude-haiku-4-5"
	case "google":
		return "gemini-3.0-flash"
	default:
		return strings.TrimSpace(cfg.LLMModel)
	}
}

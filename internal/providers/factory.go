package providers

import (
	"fmt"
	"strings"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
)

func New(cfg config.Config, cfgManager *config.Manager) (agentsdk.Provider, error) {
	return newProvider(cfg, cfgManager, strings.TrimSpace(cfg.LLMModel))
}

func NewCompaction(cfg config.Config, cfgManager *config.Manager) (agentsdk.Provider, string, error) {
	model := compactionModelForProvider(cfg)
	provider, err := newProvider(cfg, cfgManager, model)
	if err != nil {
		return nil, "", err
	}
	return provider, model, nil
}

func newProvider(cfg config.Config, cfgManager *config.Manager, model string) (agentsdk.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.LLMProvider)) {
	case "openai":
		return newOpenAIProvider(cfg, cfgManager, model)
	case "anthropic":
		return newAnthropicProvider(cfg, cfgManager, model)
	case "google":
		return newGeminiProvider(cfg, cfgManager, model)
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLMProvider)
	}
}

func compactionModelForProvider(cfg config.Config) string {
	switch strings.ToLower(strings.TrimSpace(cfg.LLMProvider)) {
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

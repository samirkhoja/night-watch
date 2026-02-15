package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/samirkhoja/night-watch/internal/config"
)

type anthropicClient struct {
	apiKey          string
	model           string
	reasoningEffort string
}

func newAnthropicClient(cfg config.Config, cfgManager *config.Manager) (Client, error) {
	key, err := cfgManager.GetEnvValue("ANTHROPIC_API_KEY")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is not configured")
	}

	model := strings.TrimSpace(cfg.LLMModel)
	if model == "" {
		model = "claude-opus-4-5"
	}

	return &anthropicClient{
		apiKey:          key,
		model:           model,
		reasoningEffort: config.NormalizeReasoningEffort(cfg.ReasoningEffort),
	}, nil
}

func (c *anthropicClient) Name() string {
	return "anthropic"
}

func (c *anthropicClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	type requestContent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type requestMessage struct {
		Role    string           `json:"role"`
		Content []requestContent `json:"content"`
	}
	msgs := make([]requestMessage, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := msg.Role
		if role == "system" {
			role = "user"
		}
		if role != "assistant" && role != "user" {
			role = "user"
		}
		msgs = append(msgs, requestMessage{
			Role: role,
			Content: []requestContent{
				{
					Type: "text",
					Text: msg.Content,
				},
			},
		})
	}

	maxTokens := maxInt(req.MaxTokens, 2048)
	thinkingBudget := anthropicThinkingBudget(c.reasoningEffort)
	if maxTokens < thinkingBudget+256 {
		maxTokens = thinkingBudget + 256
	}

	body := struct {
		Model       string  `json:"model"`
		MaxTokens   int     `json:"max_tokens"`
		Temperature float64 `json:"temperature,omitempty"`
		System      string  `json:"system,omitempty"`
		Thinking    *struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		} `json:"thinking,omitempty"`
		Messages   []requestMessage         `json:"messages"`
		Tools      []map[string]interface{} `json:"tools,omitempty"`
		ToolChoice map[string]string        `json:"tool_choice,omitempty"`
	}{
		Model:       c.model,
		MaxTokens:   maxTokens,
		Temperature: boundedTemperature(req.Temperature),
		System:      strings.TrimSpace(req.System),
		Thinking: &struct {
			Type         string `json:"type"`
			BudgetTokens int    `json:"budget_tokens"`
		}{
			Type:         "enabled",
			BudgetTokens: thinkingBudget,
		},
		Messages: msgs,
	}
	if !req.DisableTools {
		body.Tools = anthropicToolsPayload()
		body.ToolChoice = map[string]string{
			"type": "auto",
		}
	}

	headers := map[string]string{
		"x-api-key":         c.apiKey,
		"anthropic-version": "2023-06-01",
	}

	data, statusCode, err := doJSONRequest(
		ctx,
		newHTTPClient(),
		"POST",
		"https://api.anthropic.com/v1/messages",
		headers,
		body,
	)
	if err != nil {
		return GenerateResponse{}, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return GenerateResponse{}, summarizeHTTPError(statusCode, data)
	}

	var resp struct {
		Content []struct {
			Type     string                 `json:"type"`
			Text     string                 `json:"text,omitempty"`
			Thinking string                 `json:"thinking,omitempty"`
			Name     string                 `json:"name,omitempty"`
			Input    map[string]interface{} `json:"input,omitempty"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return GenerateResponse{}, fmt.Errorf("parse anthropic response: %w", err)
	}
	if len(resp.Content) == 0 {
		return GenerateResponse{}, errors.New("anthropic returned no content")
	}

	out := GenerateResponse{}
	var replyChunks []string
	for _, part := range resp.Content {
		partType := strings.ToLower(strings.TrimSpace(part.Type))
		switch partType {
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				Name:      strings.TrimSpace(part.Name),
				Arguments: part.Input,
			})
		case "thinking":
			if text := strings.TrimSpace(part.Thinking); text != "" {
				out.Reasoning = append(out.Reasoning, text)
			}
			if text := strings.TrimSpace(part.Text); text != "" {
				out.Reasoning = append(out.Reasoning, text)
			}
		case "text":
			if text := strings.TrimSpace(part.Text); text != "" {
				replyChunks = append(replyChunks, text)
			}
		}
	}
	if len(replyChunks) > 0 {
		out.Reply = strings.Join(replyChunks, "\n")
	}
	out = normalizeGenerateResponse(out)
	if strings.TrimSpace(out.Reply) == "" && len(out.ToolCalls) == 0 && len(out.Reasoning) == 0 {
		return GenerateResponse{}, errors.New("anthropic returned no content or tool calls")
	}
	return out, nil
}

func boundedTemperature(temp float64) float64 {
	if temp < 0 {
		return 0
	}
	if temp > 1 {
		return 1
	}
	return temp
}

func maxInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

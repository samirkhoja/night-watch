package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
)

const (
	anthropicDefaultBaseURL    = "https://api.anthropic.com/v1"
	anthropicDefaultModel      = "claude-opus-4-6"
	anthropicDefaultAPIVersion = "2023-06-01"
)

type anthropicProvider struct {
	apiKey          string
	baseURL         string
	model           string
	apiVersion      string
	reasoningEffort string
	httpClient      *http.Client
}

func newAnthropicProvider(cfg config.Config, cfgManager *config.Manager, model string) (agentsdk.Provider, error) {
	key, err := cfgManager.GetEnvValue("ANTHROPIC_API_KEY")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("ANTHROPIC_API_KEY is not configured")
	}

	model = strings.TrimSpace(model)
	if model == "" {
		model = anthropicDefaultModel
	}

	return &anthropicProvider{
		apiKey:          key,
		baseURL:         anthropicDefaultBaseURL,
		model:           model,
		apiVersion:      anthropicDefaultAPIVersion,
		reasoningEffort: config.NormalizeReasoningEffort(cfg.ReasoningEffort),
		httpClient:      newHTTPClient(),
	}, nil
}

func (p *anthropicProvider) DefaultModel() string {
	return p.model
}

func (p *anthropicProvider) Chat(
	ctx context.Context,
	messages []agentsdk.Message,
	tools []agentsdk.ToolDefinition,
	model string,
	options map[string]any,
) (*agentsdk.LLMResponse, error) {
	if strings.TrimSpace(model) == "" {
		model = p.model
	}

	systemText, convertedMessages := anthropicConvertMessages(messages)
	payload := anthropicMessageRequest{
		Model:    model,
		System:   systemText,
		Messages: convertedMessages,
		Tools:    anthropicConvertToolDefinitions(tools),
	}
	applyAnthropicOptions(&payload, firstNonEmptyReasoning(options, p.reasoningEffort), options)

	headers := map[string]string{
		"x-api-key":         p.apiKey,
		"anthropic-version": p.apiVersion,
	}

	data, statusCode, err := doJSONRequest(
		ctx,
		p.httpClient,
		http.MethodPost,
		strings.TrimRight(p.baseURL, "/")+"/messages",
		headers,
		payload,
	)
	if err != nil {
		return nil, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, summarizeHTTPError(statusCode, data)
	}

	var resp anthropicMessageResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse anthropic response: %w", err)
	}

	textParts := make([]string, 0)
	toolCalls := make([]agentsdk.ToolCall, 0)
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			argsRaw, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, agentsdk.ToolCall{
				ID:        block.ID,
				Type:      "function",
				Name:      block.Name,
				Arguments: cloneMap(block.Input),
				Function: &agentsdk.FunctionCall{
					Name:      block.Name,
					Arguments: string(argsRaw),
				},
			})
		}
	}

	return &agentsdk.LLMResponse{
		Content:      strings.Join(textParts, "\n"),
		ToolCalls:    toolCalls,
		FinishReason: resp.StopReason,
		Usage: &agentsdk.UsageInfo{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}, nil
}

type anthropicMessageRequest struct {
	Model       string              `json:"model"`
	MaxTokens   int                 `json:"max_tokens"`
	Temperature *float64            `json:"temperature,omitempty"`
	TopP        *float64            `json:"top_p,omitempty"`
	System      string              `json:"system,omitempty"`
	Thinking    *anthropicThinking  `json:"thinking,omitempty"`
	Tools       []anthropicTool     `json:"tools,omitempty"`
	Messages    []anthropicMessage  `json:"messages"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   any            `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type anthropicMessageResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func anthropicConvertMessages(messages []agentsdk.Message) (string, []anthropicMessage) {
	var systemParts []string
	out := make([]anthropicMessage, 0, len(messages))
	toolNameByID := map[string]string{}
	nextCallID := 1

	for _, msg := range messages {
		switch msg.Role {
		case agentsdk.RoleSystem:
			if msg.Content != "" {
				systemParts = append(systemParts, msg.Content)
			}
		case agentsdk.RoleUser:
			out = append(out, anthropicMessage{
				Role: "user",
				Content: []anthropicBlock{
					{Type: "text", Text: msg.Content},
				},
			})
		case agentsdk.RoleAssistant:
			blocks := make([]anthropicBlock, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				normalized := normalizeToolCall(tc, fmt.Sprintf("tool_%d", nextCallID))
				nextCallID++
				toolNameByID[normalized.ID] = normalized.Name
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    normalized.ID,
					Name:  normalized.Name,
					Input: cloneMap(normalized.Arguments),
				})
			}
			if len(blocks) == 0 {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: msg.Content})
			}
			out = append(out, anthropicMessage{Role: "assistant", Content: blocks})
		case agentsdk.RoleTool:
			result := anthropicParseToolResultContent(msg.Content)
			toolName := toolNameByID[msg.ToolCallID]
			if toolName != "" {
				if obj, ok := result.(map[string]any); ok {
					obj["tool_name"] = toolName
					result = obj
				}
			}
			out = append(out, anthropicMessage{
				Role: "user",
				Content: []anthropicBlock{
					{
						Type:      "tool_result",
						ToolUseID: msg.ToolCallID,
						Content:   result,
					},
				},
			})
		}
	}

	return strings.Join(systemParts, "\n\n"), out
}

func anthropicConvertToolDefinitions(tools []agentsdk.ToolDefinition) []anthropicTool {
	if len(tools) == 0 {
		return nil
	}

	out := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}
	return out
}

func applyAnthropicOptions(payload *anthropicMessageRequest, reasoningEffort string, options map[string]any) {
	if maxTokens, ok := asInt(options["max_tokens"]); ok && maxTokens > 0 {
		payload.MaxTokens = maxTokens
	}
	if maxTokens, ok := asInt(options["max_output_tokens"]); ok && maxTokens > 0 {
		payload.MaxTokens = maxTokens
	}
	if payload.MaxTokens <= 0 {
		payload.MaxTokens = 2048
	}

	if strings.TrimSpace(reasoningEffort) == "" {
		reasoningEffort = "medium"
	}
	thinkingBudget := anthropicThinkingBudget(reasoningEffort)
	if payload.MaxTokens < thinkingBudget+256 {
		payload.MaxTokens = thinkingBudget + 256
	}
	payload.Thinking = &anthropicThinking{
		Type:         "enabled",
		BudgetTokens: thinkingBudget,
	}

	if temp, ok := asFloat(options["temperature"]); ok {
		payload.Temperature = &temp
	}
	if payload.Temperature == nil {
		if topP, ok := asFloat(options["top_p"]); ok {
			payload.TopP = &topP
		}
	}
}

func anthropicParseToolResultContent(text string) any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil && parsed != nil {
		return parsed
	}
	return text
}

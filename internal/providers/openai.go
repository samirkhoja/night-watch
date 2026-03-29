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
	openAIDefaultBaseURL = "https://api.openai.com/v1"
	openAIDefaultModel   = "gpt-5.4"
)

type openAIProvider struct {
	apiKey          string
	baseURL         string
	model           string
	reasoningEffort string
	httpClient      *http.Client
}

func newOpenAIProvider(cfg config.Config, cfgManager *config.Manager, model string) (agentsdk.Provider, error) {
	key, err := cfgManager.GetEnvValue("OPENAI_API_KEY")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured")
	}

	model = strings.TrimSpace(model)
	if model == "" {
		model = openAIDefaultModel
	}

	return &openAIProvider{
		apiKey:          key,
		baseURL:         openAIDefaultBaseURL,
		model:           model,
		reasoningEffort: config.NormalizeReasoningEffort(cfg.ReasoningEffort),
		httpClient:      newHTTPClient(),
	}, nil
}

func (p *openAIProvider) DefaultModel() string {
	return p.model
}

func (p *openAIProvider) Chat(
	ctx context.Context,
	messages []agentsdk.Message,
	tools []agentsdk.ToolDefinition,
	model string,
	options map[string]any,
) (*agentsdk.LLMResponse, error) {
	if strings.TrimSpace(model) == "" {
		model = p.model
	}

	instructions, input := openAIConvertInput(messages)
	payload := openAIResponsesRequest{
		Model:        model,
		Instructions: instructions,
		Input:        input,
		Tools:        openAIConvertToolDefinitions(tools),
	}
	applyOpenAIOptions(&payload, model, firstNonEmptyReasoning(options, p.reasoningEffort), options)

	headers := map[string]string{
		"Authorization": "Bearer " + p.apiKey,
	}
	data, statusCode, err := doJSONRequest(
		ctx,
		p.httpClient,
		http.MethodPost,
		strings.TrimRight(p.baseURL, "/")+"/responses",
		headers,
		payload,
	)
	if err != nil {
		return nil, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, summarizeHTTPError(statusCode, data)
	}

	var resp openAIResponsesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse openai response: %w", err)
	}
	if len(resp.Output) == 0 {
		return nil, errors.New("openai returned no output items")
	}

	return &agentsdk.LLMResponse{
		Content:      openAIExtractOutputText(resp.Output),
		ToolCalls:    openAIExtractToolCalls(resp.Output),
		FinishReason: resp.Status,
		Usage: &agentsdk.UsageInfo{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}, nil
}

type openAIResponsesRequest struct {
	Model           string                   `json:"model"`
	Instructions    string                   `json:"instructions,omitempty"`
	Input           []openAIResponseInputItem `json:"input,omitempty"`
	Tools           []openAIResponsesTool    `json:"tools,omitempty"`
	Temperature     *float64                 `json:"temperature,omitempty"`
	TopP            *float64                 `json:"top_p,omitempty"`
	MaxOutputTokens *int                     `json:"max_output_tokens,omitempty"`
	Reasoning       *struct {
		Effort string `json:"effort"`
	} `json:"reasoning,omitempty"`
}

type openAIResponseInputItem struct {
	Type      string               `json:"type,omitempty"`
	Role      string               `json:"role,omitempty"`
	Content   []openAIResponseContent `json:"content,omitempty"`
	ID        string               `json:"id,omitempty"`
	CallID    string               `json:"call_id,omitempty"`
	Name      string               `json:"name,omitempty"`
	Arguments string               `json:"arguments,omitempty"`
	Output    string               `json:"output,omitempty"`
}

type openAIResponseContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type openAIResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIResponsesResponse struct {
	ID     string                     `json:"id"`
	Status string                     `json:"status"`
	Output []openAIResponseOutputItem `json:"output"`
	Usage  struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		TotalTokens  int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIResponseOutputItem struct {
	ID        string                  `json:"id"`
	Type      string                  `json:"type"`
	Status    string                  `json:"status,omitempty"`
	Role      string                  `json:"role,omitempty"`
	Name      string                  `json:"name,omitempty"`
	CallID    string                  `json:"call_id,omitempty"`
	Arguments string                  `json:"arguments,omitempty"`
	Content   []openAIResponseContent `json:"content,omitempty"`
}

func openAIConvertInput(messages []agentsdk.Message) (string, []openAIResponseInputItem) {
	instructions := make([]string, 0)
	items := make([]openAIResponseInputItem, 0, len(messages))

	for _, msg := range messages {
		switch msg.Role {
		case agentsdk.RoleSystem:
			if msg.Content != "" {
				instructions = append(instructions, msg.Content)
			}
		case agentsdk.RoleUser:
			items = append(items, openAIResponseInputItem{
				Type: "message",
				Role: "user",
				Content: []openAIResponseContent{
					{Type: "input_text", Text: msg.Content},
				},
			})
		case agentsdk.RoleAssistant:
			if msg.Content != "" {
				items = append(items, openAIResponseInputItem{
					Type: "message",
					Role: "assistant",
					Content: []openAIResponseContent{
						{Type: "output_text", Text: msg.Content},
					},
				})
			}
			for _, tc := range msg.ToolCalls {
				items = append(items, openAIToFunctionCallItem(tc))
			}
		case agentsdk.RoleTool:
			if msg.ToolCallID == "" {
				continue
			}
			items = append(items, openAIResponseInputItem{
				Type:   "function_call_output",
				CallID: msg.ToolCallID,
				Output: msg.Content,
			})
		default:
			items = append(items, openAIResponseInputItem{
				Type: "message",
				Role: "user",
				Content: []openAIResponseContent{
					{Type: "input_text", Text: msg.Content},
				},
			})
		}
	}

	return strings.Join(instructions, "\n\n"), items
}

func openAIConvertToolDefinitions(tools []agentsdk.ToolDefinition) []openAIResponsesTool {
	if len(tools) == 0 {
		return nil
	}

	out := make([]openAIResponsesTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, openAIResponsesTool{
			Type:        "function",
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}
	return out
}

func openAIToFunctionCallItem(tc agentsdk.ToolCall) openAIResponseInputItem {
	name := tc.Name
	if name == "" && tc.Function != nil {
		name = tc.Function.Name
	}

	args := tc.Arguments
	if args == nil {
		args = map[string]any{}
	}
	argsRaw, _ := json.Marshal(args)
	if tc.Function != nil && tc.Function.Arguments != "" {
		argsRaw = []byte(tc.Function.Arguments)
	}

	callID := tc.ID
	if callID == "" {
		callID = "call_auto"
	}

	return openAIResponseInputItem{
		Type:      "function_call",
		CallID:    callID,
		Name:      name,
		Arguments: string(argsRaw),
	}
}

func openAIExtractOutputText(items []openAIResponseOutputItem) string {
	parts := make([]string, 0)
	for _, item := range items {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, content := range item.Content {
			switch content.Type {
			case "output_text", "text":
				if content.Text != "" {
					parts = append(parts, content.Text)
				}
			}
		}
	}
	return strings.Join(parts, "\n")
}

func openAIExtractToolCalls(items []openAIResponseOutputItem) []agentsdk.ToolCall {
	toolCalls := make([]agentsdk.ToolCall, 0)
	for _, item := range items {
		if item.Type != "function_call" {
			continue
		}

		args := map[string]any{}
		if item.Arguments != "" {
			_ = json.Unmarshal([]byte(item.Arguments), &args)
		}

		id := item.CallID
		if id == "" {
			id = item.ID
		}

		toolCalls = append(toolCalls, agentsdk.ToolCall{
			ID:        id,
			Type:      "function",
			Name:      item.Name,
			Arguments: args,
			Function: &agentsdk.FunctionCall{
				Name:      item.Name,
				Arguments: item.Arguments,
			},
		})
	}
	return toolCalls
}

func applyOpenAIOptions(payload *openAIResponsesRequest, model string, reasoningEffort string, options map[string]any) {
	if temp, ok := asFloat(options["temperature"]); ok && !isGPT5Model(model) {
		payload.Temperature = &temp
	}
	if topP, ok := asFloat(options["top_p"]); ok {
		payload.TopP = &topP
	}
	if maxTokens, ok := asInt(options["max_output_tokens"]); ok {
		payload.MaxOutputTokens = &maxTokens
	} else if maxTokens, ok := asInt(options["max_tokens"]); ok {
		payload.MaxOutputTokens = &maxTokens
	}
	if strings.TrimSpace(reasoningEffort) != "" {
		payload.Reasoning = &struct {
			Effort string `json:"effort"`
		}{
			Effort: strings.TrimSpace(reasoningEffort),
		}
	}
}

func firstNonEmptyReasoning(options map[string]any, fallback string) string {
	if options != nil {
		if value, ok := options["reasoning_effort"].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(fallback)
}

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/samirkhoja/night-watch/internal/config"
)

type openAIClient struct {
	apiKey          string
	model           string
	reasoningEffort string
}

func newOpenAIClient(cfg config.Config, cfgManager *config.Manager) (Client, error) {
	key, err := cfgManager.GetEnvValue("OPENAI_API_KEY")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured")
	}

	model := strings.TrimSpace(cfg.LLMModel)
	if model == "" {
		model = "gpt-5.2"
	}

	return &openAIClient{
		apiKey:          key,
		model:           model,
		reasoningEffort: config.NormalizeReasoningEffort(cfg.ReasoningEffort),
	}, nil
}

func (c *openAIClient) Name() string {
	return "openai"
}

func (c *openAIClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	type inputPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type inputMessage struct {
		Role    string      `json:"role"`
		Content []inputPart `json:"content"`
	}
	body := struct {
		Model             string                   `json:"model"`
		Temperature       float64                  `json:"temperature,omitempty"`
		MaxOutputTokens   int                      `json:"max_output_tokens,omitempty"`
		Input             []inputMessage           `json:"input"`
		Tools             []map[string]interface{} `json:"tools,omitempty"`
		ParallelToolCalls bool                     `json:"parallel_tool_calls,omitempty"`
		Reasoning         *struct {
			Effort string `json:"effort"`
		} `json:"reasoning,omitempty"`
	}{
		Model: c.model,
		Input: make([]inputMessage, 0, len(req.Messages)+1),
	}
	if !req.DisableTools {
		body.Tools = openAIToolsPayload()
		body.ParallelToolCalls = true
	}
	body.Reasoning = &struct {
		Effort string `json:"effort"`
	}{
		Effort: c.reasoningEffort,
	}
	if !isGPT5Model(c.model) {
		body.Temperature = maxFloat(req.Temperature, 1.0)
		body.MaxOutputTokens = maxInt(req.MaxTokens, 2500)
	}

	if strings.TrimSpace(req.System) != "" {
		body.Input = append(body.Input, inputMessage{
			Role:    "system",
			Content: []inputPart{{Type: "input_text", Text: req.System}},
		})
	}
	for _, msg := range req.Messages {
		role := msg.Role
		if role != "assistant" && role != "user" && role != "system" {
			role = "user"
		}
		body.Input = append(body.Input, inputMessage{
			Role:    role,
			Content: []inputPart{{Type: openAIInputPartType(role), Text: msg.Content}},
		})
	}

	headers := map[string]string{
		"Authorization": "Bearer " + c.apiKey,
	}
	data, statusCode, err := doJSONRequest(
		ctx,
		newHTTPClient(),
		"POST",
		"https://api.openai.com/v1/responses",
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
		OutputText string `json:"output_text"`
		Output     []struct {
			Type      string `json:"type"`
			Name      string `json:"name,omitempty"`
			Arguments string `json:"arguments,omitempty"`
			Summary   []struct {
				Text string `json:"text,omitempty"`
			} `json:"summary,omitempty"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text,omitempty"`
			} `json:"content,omitempty"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return GenerateResponse{}, fmt.Errorf("parse openai response: %w", err)
	}

	out := GenerateResponse{}
	var replyChunks []string
	if strings.TrimSpace(resp.OutputText) != "" {
		replyChunks = append(replyChunks, strings.TrimSpace(resp.OutputText))
	}
	for _, item := range resp.Output {
		itemType := strings.ToLower(strings.TrimSpace(item.Type))
		switch itemType {
		case "message":
			for _, part := range item.Content {
				if strings.TrimSpace(part.Text) != "" {
					replyChunks = append(replyChunks, strings.TrimSpace(part.Text))
				}
			}
		case "reasoning":
			for _, summary := range item.Summary {
				if strings.TrimSpace(summary.Text) != "" {
					out.Reasoning = append(out.Reasoning, strings.TrimSpace(summary.Text))
				}
			}
		case "function_call":
			args, err := parseToolArguments(item.Arguments)
			if err != nil {
				args = map[string]interface{}{
					"raw_arguments": item.Arguments,
					"parse_error":   err.Error(),
				}
			}
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				Name:      strings.TrimSpace(item.Name),
				Arguments: args,
			})
		}
	}
	if len(replyChunks) > 0 {
		out.Reply = strings.Join(replyChunks, "\n")
	}
	out = normalizeGenerateResponse(out)
	if strings.TrimSpace(out.Reply) == "" && len(out.ToolCalls) == 0 && len(out.Reasoning) == 0 {
		return GenerateResponse{}, errors.New("openai returned no content or tool calls")
	}
	return out, nil
}

func maxFloat(value float64, fallback float64) float64 {
	if value <= 0 {
		return fallback
	}
	return value
}

func isGPT5Model(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-5")
}

func openAIInputPartType(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "assistant" {
		return "output_text"
	}
	return "input_text"
}

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
)

const (
	geminiDefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	geminiDefaultModel   = "gemini-3.0-pro"
)

type geminiProvider struct {
	apiKey          string
	baseURL         string
	model           string
	reasoningEffort string
	httpClient      *http.Client
}

func newGeminiProvider(cfg config.Config, cfgManager *config.Manager, model string) (agentsdk.Provider, error) {
	key, err := cfgManager.GetEnvValue("GOOGLE_API_KEY")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("GOOGLE_API_KEY is not configured")
	}

	model = strings.TrimSpace(model)
	if model == "" {
		model = geminiDefaultModel
	}

	return &geminiProvider{
		apiKey:          key,
		baseURL:         geminiDefaultBaseURL,
		model:           model,
		reasoningEffort: config.NormalizeReasoningEffort(cfg.ReasoningEffort),
		httpClient:      newHTTPClient(),
	}, nil
}

func (p *geminiProvider) DefaultModel() string {
	return p.model
}

func (p *geminiProvider) Chat(
	ctx context.Context,
	messages []agentsdk.Message,
	tools []agentsdk.ToolDefinition,
	model string,
	options map[string]any,
) (*agentsdk.LLMResponse, error) {
	if strings.TrimSpace(model) == "" {
		model = p.model
	}

	payload := geminiGenerateContentRequest{}
	systemInstruction, contents := geminiConvertMessages(messages)
	payload.SystemInstruction = systemInstruction
	payload.Contents = contents
	payload.Tools = geminiConvertTools(tools)
	payload.GenerationConfig = geminiConvertGenerationConfig(firstNonEmptyReasoning(options, p.reasoningEffort), options)

	endpoint := p.buildEndpoint(model)
	data, statusCode, err := doJSONRequest(ctx, p.httpClient, http.MethodPost, endpoint, nil, payload)
	if err != nil {
		return nil, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, summarizeHTTPError(statusCode, data)
	}

	var resp geminiGenerateContentResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("parse gemini response: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return nil, errors.New("gemini returned no candidates")
	}

	candidate := resp.Candidates[0]
	textParts := make([]string, 0)
	toolCalls := make([]agentsdk.ToolCall, 0)
	for i, part := range candidate.Content.Parts {
		if part.Text != "" {
			textParts = append(textParts, part.Text)
		}
		if part.FunctionCall != nil {
			argsRaw, _ := json.Marshal(part.FunctionCall.Args)
			toolCalls = append(toolCalls, agentsdk.ToolCall{
				ID:        fmt.Sprintf("call_%d", i+1),
				Type:      "function",
				Name:      part.FunctionCall.Name,
				Arguments: cloneMap(part.FunctionCall.Args),
				Function: &agentsdk.FunctionCall{
					Name:      part.FunctionCall.Name,
					Arguments: string(argsRaw),
				},
			})
		}
	}

	return &agentsdk.LLMResponse{
		Content:      strings.Join(textParts, "\n"),
		ToolCalls:    toolCalls,
		FinishReason: candidate.FinishReason,
		Usage: &agentsdk.UsageInfo{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		},
	}, nil
}

type geminiGenerateContentRequest struct {
	Contents          []geminiContent          `json:"contents,omitempty"`
	SystemInstruction *geminiContent           `json:"systemInstruction,omitempty"`
	Tools             []geminiTool             `json:"tools,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

type geminiFunctionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type geminiFunctionDeclaration struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiGenerationConfig struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	TopP           *float64 `json:"topP,omitempty"`
	MaxOutputTokens *int    `json:"maxOutputTokens,omitempty"`
	ThinkingConfig *struct {
		ThinkingBudget int `json:"thinkingBudget,omitempty"`
	} `json:"thinkingConfig,omitempty"`
}

type geminiGenerateContentResponse struct {
	Candidates []struct {
		Content struct {
			Role  string       `json:"role"`
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
		TotalTokenCount      int `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

func (p *geminiProvider) buildEndpoint(model string) string {
	base := strings.TrimRight(strings.TrimSpace(p.baseURL), "/")
	modelPath := model
	if !strings.HasPrefix(modelPath, "models/") {
		modelPath = "models/" + modelPath
	}
	return fmt.Sprintf("%s/%s:generateContent?key=%s", base, modelPath, url.QueryEscape(p.apiKey))
}

func geminiConvertMessages(messages []agentsdk.Message) (*geminiContent, []geminiContent) {
	systemText := make([]string, 0)
	contents := make([]geminiContent, 0, len(messages))
	toolNameByID := map[string]string{}
	nextCallID := 1

	for _, msg := range messages {
		switch msg.Role {
		case agentsdk.RoleSystem:
			if msg.Content != "" {
				systemText = append(systemText, msg.Content)
			}
		case agentsdk.RoleUser:
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{
					{Text: msg.Content},
				},
			})
		case agentsdk.RoleAssistant:
			parts := make([]geminiPart, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				parts = append(parts, geminiPart{Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				normalized := normalizeToolCall(tc, fmt.Sprintf("tool_%d", nextCallID))
				nextCallID++
				toolNameByID[normalized.ID] = normalized.Name
				parts = append(parts, geminiPart{
					FunctionCall: &geminiFunctionCall{
						Name: normalized.Name,
						Args: cloneMap(normalized.Arguments),
					},
				})
			}
			if len(parts) > 0 {
				contents = append(contents, geminiContent{
					Role:  "model",
					Parts: parts,
				})
			}
		case agentsdk.RoleTool:
			toolName := toolNameByID[msg.ToolCallID]
			if toolName == "" {
				toolName = "tool_" + msg.ToolCallID
			}
			contents = append(contents, geminiContent{
				Role: "user",
				Parts: []geminiPart{
					{
						FunctionResponse: &geminiFunctionResponse{
							Name:     toolName,
							Response: geminiToFunctionResponse(msg.Content),
						},
					},
				},
			})
		}
	}

	if len(systemText) == 0 {
		return nil, contents
	}
	return &geminiContent{
		Parts: []geminiPart{
			{Text: strings.Join(systemText, "\n\n")},
		},
	}, contents
}

func geminiConvertTools(toolsIn []agentsdk.ToolDefinition) []geminiTool {
	if len(toolsIn) == 0 {
		return nil
	}

	declarations := make([]geminiFunctionDeclaration, 0, len(toolsIn))
	for _, td := range toolsIn {
		declarations = append(declarations, geminiFunctionDeclaration{
			Name:        td.Function.Name,
			Description: td.Function.Description,
			Parameters:  td.Function.Parameters,
		})
	}

	return []geminiTool{
		{
			FunctionDeclarations: declarations,
		},
	}
}

func geminiConvertGenerationConfig(reasoningEffort string, options map[string]any) *geminiGenerationConfig {
	cfg := &geminiGenerationConfig{}
	if temp, ok := asFloat(options["temperature"]); ok {
		cfg.Temperature = &temp
	}
	if topP, ok := asFloat(options["top_p"]); ok {
		cfg.TopP = &topP
	}
	if maxTokens, ok := asInt(options["max_output_tokens"]); ok {
		cfg.MaxOutputTokens = &maxTokens
	} else if maxTokens, ok := asInt(options["max_tokens"]); ok {
		cfg.MaxOutputTokens = &maxTokens
	}
	if strings.TrimSpace(reasoningEffort) == "" {
		reasoningEffort = "medium"
	}
	cfg.ThinkingConfig = &struct {
		ThinkingBudget int `json:"thinkingBudget,omitempty"`
	}{
		ThinkingBudget: googleThinkingBudget(reasoningEffort),
	}
	return cfg
}

func geminiToFunctionResponse(text string) map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil && parsed != nil {
		return parsed
	}
	return map[string]any{
		"content": text,
	}
}

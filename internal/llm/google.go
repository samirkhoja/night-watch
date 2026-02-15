package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/samirkhoja/night-watch/internal/config"
)

type googleClient struct {
	apiKey          string
	model           string
	reasoningEffort string
}

func newGoogleClient(cfg config.Config, cfgManager *config.Manager) (Client, error) {
	key, err := cfgManager.GetEnvValue("GOOGLE_API_KEY")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("GOOGLE_API_KEY is not configured")
	}

	model := strings.TrimSpace(cfg.LLMModel)
	if model == "" {
		model = "gemini-3.0-pro"
	}

	return &googleClient{
		apiKey:          key,
		model:           model,
		reasoningEffort: config.NormalizeReasoningEffort(cfg.ReasoningEffort),
	}, nil
}

func (c *googleClient) Name() string {
	return "google"
}

func (c *googleClient) Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error) {
	type part struct {
		Text string `json:"text"`
	}
	type content struct {
		Role  string `json:"role,omitempty"`
		Parts []part `json:"parts"`
	}

	contents := make([]content, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := msg.Role
		if role == "assistant" {
			role = "model"
		}
		if role != "model" && role != "user" {
			role = "user"
		}
		contents = append(contents, content{
			Role: role,
			Parts: []part{
				{
					Text: msg.Content,
				},
			},
		})
	}

	body := struct {
		SystemInstruction *content                 `json:"systemInstruction,omitempty"`
		Contents          []content                `json:"contents"`
		Tools             []map[string]interface{} `json:"tools,omitempty"`
		ToolConfig        *struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		} `json:"toolConfig,omitempty"`
		GenerationConfig struct {
			Temperature    float64 `json:"temperature,omitempty"`
			MaxTokens      int     `json:"maxOutputTokens,omitempty"`
			ThinkingConfig struct {
				ThinkingBudget int `json:"thinkingBudget,omitempty"`
			} `json:"thinkingConfig,omitempty"`
		} `json:"generationConfig"`
	}{
		Contents: contents,
	}
	if strings.TrimSpace(req.System) != "" {
		body.SystemInstruction = &content{
			Parts: []part{
				{
					Text: req.System,
				},
			},
		}
	}
	body.GenerationConfig.Temperature = boundedTemperature(req.Temperature)
	body.GenerationConfig.MaxTokens = maxInt(req.MaxTokens, 2048)
	body.GenerationConfig.ThinkingConfig.ThinkingBudget = googleThinkingBudget(c.reasoningEffort)
	if !req.DisableTools {
		body.Tools = googleToolsPayload()
		body.ToolConfig = &struct {
			FunctionCallingConfig struct {
				Mode string `json:"mode"`
			} `json:"functionCallingConfig"`
		}{}
		body.ToolConfig.FunctionCallingConfig.Mode = "AUTO"
	}

	endpoint := fmt.Sprintf(
		"https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s",
		url.PathEscape(c.model),
		url.QueryEscape(c.apiKey),
	)
	data, statusCode, err := doJSONRequest(ctx, newHTTPClient(), "POST", endpoint, nil, body)
	if err != nil {
		return GenerateResponse{}, err
	}
	if statusCode < 200 || statusCode >= 300 {
		return GenerateResponse{}, summarizeHTTPError(statusCode, data)
	}

	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string `json:"text,omitempty"`
					FunctionCall struct {
						Name string                 `json:"name"`
						Args map[string]interface{} `json:"args"`
					} `json:"functionCall,omitempty"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return GenerateResponse{}, fmt.Errorf("parse google response: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return GenerateResponse{}, errors.New("google returned no candidates")
	}

	out := GenerateResponse{}
	var chunks []string
	for _, p := range resp.Candidates[0].Content.Parts {
		if strings.TrimSpace(p.Text) != "" {
			chunks = append(chunks, strings.TrimSpace(p.Text))
		}
		if strings.TrimSpace(p.FunctionCall.Name) != "" {
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				Name:      strings.TrimSpace(p.FunctionCall.Name),
				Arguments: p.FunctionCall.Args,
			})
		}
	}
	if len(chunks) > 0 {
		out.Reply = strings.Join(chunks, "\n")
	}
	out = normalizeGenerateResponse(out)
	if strings.TrimSpace(out.Reply) == "" && len(out.ToolCalls) == 0 && len(out.Reasoning) == 0 {
		return GenerateResponse{}, errors.New("google returned no content or tool calls")
	}
	return out, nil
}

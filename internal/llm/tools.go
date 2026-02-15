package llm

import (
	"encoding/json"
	"strings"
)

type actionTool struct {
	Name        string
	Description string
	Parameters  map[string]interface{}
}

func actionTools() []actionTool {
	return []actionTool{
		{
			Name:        "status",
			Description: "Emit a short status update for the user before or during work.",
			Parameters: map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"message": map[string]interface{}{
						"type": "string",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			Name:        "run_command",
			Description: "Run a shell command for diagnostics or data collection. Include a short user-visible reason. Use cwd and paths inside workspace_root or runbook_root.",
			Parameters: map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"reason": map[string]interface{}{
						"type": "string",
					},
					"command": map[string]interface{}{
						"type": "string",
					},
					"cwd": map[string]interface{}{
						"type": "string",
					},
					"timeout_sec": map[string]interface{}{
						"type":    "integer",
						"minimum": 1,
					},
				},
				"required": []string{"reason", "command"},
			},
		},
		{
			Name:        "spawn_sub_agents",
			Description: "Run independent sub-agent tasks in parallel and aggregate findings.",
			Parameters: map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"max_parallel": map[string]interface{}{
						"type":    "integer",
						"minimum": 1,
					},
					"tasks": map[string]interface{}{
						"type":     "array",
						"minItems": 1,
						"items": map[string]interface{}{
							"type":                 "object",
							"additionalProperties": false,
							"properties": map[string]interface{}{
								"name": map[string]interface{}{
									"type": "string",
								},
								"goal": map[string]interface{}{
									"type": "string",
								},
								"context": map[string]interface{}{
									"type": "string",
								},
								"max_steps": map[string]interface{}{
									"type":    "integer",
									"minimum": 1,
								},
							},
							"required": []string{"goal"},
						},
					},
					"goal": map[string]interface{}{
						"type": "string",
					},
					"task": map[string]interface{}{
						"type": "string",
					},
					"context": map[string]interface{}{
						"type": "string",
					},
					"max_steps": map[string]interface{}{
						"type":    "integer",
						"minimum": 1,
					},
				},
			},
		},
		{
			Name:        "spawn_sub_agent",
			Description: "Run a single delegated sub-agent task.",
			Parameters: map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]interface{}{
					"goal": map[string]interface{}{
						"type": "string",
					},
					"task": map[string]interface{}{
						"type": "string",
					},
					"context": map[string]interface{}{
						"type": "string",
					},
					"max_steps": map[string]interface{}{
						"type":    "integer",
						"minimum": 1,
					},
				},
			},
		},
	}
}

func openAIToolsPayload() []map[string]interface{} {
	tools := actionTools()
	out := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]interface{}{
			"type":        "function",
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	return out
}

func anthropicToolsPayload() []map[string]interface{} {
	tools := actionTools()
	out := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]interface{}{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.Parameters,
		})
	}
	return out
}

func googleToolsPayload() []map[string]interface{} {
	tools := actionTools()
	functions := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		functions = append(functions, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  toGoogleSchemaObject(tool.Parameters),
		})
	}
	return []map[string]interface{}{
		{
			"functionDeclarations": functions,
		},
	}
}

func toGoogleSchemaObject(schema map[string]interface{}) map[string]interface{} {
	converted, _ := toGoogleSchemaValue(schema).(map[string]interface{})
	return converted
}

func toGoogleSchemaValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		out := map[string]interface{}{}
		for key, rawValue := range typed {
			switch key {
			case "type":
				if typeName, ok := rawValue.(string); ok {
					out[key] = strings.ToUpper(strings.TrimSpace(typeName))
				}
			case "properties":
				props := map[string]interface{}{}
				if rawProps, ok := rawValue.(map[string]interface{}); ok {
					for propName, propSchema := range rawProps {
						props[propName] = toGoogleSchemaValue(propSchema)
					}
				}
				out[key] = props
			case "items":
				out[key] = toGoogleSchemaValue(rawValue)
			case "required", "description", "enum", "minimum", "maximum", "minItems", "maxItems":
				out[key] = rawValue
			}
		}
		return out
	case []interface{}:
		out := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			out = append(out, toGoogleSchemaValue(item))
		}
		return out
	default:
		return value
	}
}

func parseToolArguments(raw string) (map[string]interface{}, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}, nil
	}

	var parsed interface{}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, err
	}
	if parsed == nil {
		return map[string]interface{}{}, nil
	}
	object, ok := parsed.(map[string]interface{})
	if !ok {
		return map[string]interface{}{
			"value": parsed,
		}, nil
	}
	return object, nil
}

func normalizeGenerateResponse(resp GenerateResponse) GenerateResponse {
	resp.Reply = strings.TrimSpace(resp.Reply)

	reasoning := make([]string, 0, len(resp.Reasoning))
	for _, item := range resp.Reasoning {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		reasoning = append(reasoning, trimmed)
	}
	resp.Reasoning = reasoning
	if resp.Reasoning == nil {
		resp.Reasoning = []string{}
	}

	toolCalls := make([]ToolCall, 0, len(resp.ToolCalls))
	for _, call := range resp.ToolCalls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			continue
		}
		args := call.Arguments
		if args == nil {
			args = map[string]interface{}{}
		}
		toolCalls = append(toolCalls, ToolCall{
			Name:      name,
			Arguments: args,
		})
	}
	resp.ToolCalls = toolCalls
	if resp.ToolCalls == nil {
		resp.ToolCalls = []ToolCall{}
	}

	return resp
}

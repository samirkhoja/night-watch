package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	agentsdk "github.com/samirkhoja/agent-sdk"
)

func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 45 * time.Second,
	}
}

func doJSONRequest(
	ctx context.Context,
	client *http.Client,
	method string,
	url string,
	headers map[string]string,
	body any,
) ([]byte, int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, 0, fmt.Errorf("marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("perform request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response body: %w", err)
	}
	return data, resp.StatusCode, nil
}

func summarizeHTTPError(statusCode int, payload []byte) error {
	msg := strings.TrimSpace(string(payload))
	if len(msg) > 600 {
		msg = msg[:600] + "...[truncated]"
	}
	if msg == "" {
		msg = "(empty response body)"
	}
	return fmt.Errorf("provider API error (%d): %s", statusCode, msg)
}

func asFloat(v any) (float64, bool) {
	switch value := v.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int32:
		return float64(value), true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

func asInt(v any) (int, bool) {
	switch value := v.(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case float32:
		return int(value), true
	default:
		return 0, false
	}
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func normalizeToolCall(tc agentsdk.ToolCall, fallbackID string) agentsdk.ToolCall {
	call := tc
	if call.ID == "" {
		call.ID = fallbackID
	}
	if call.Name == "" && call.Function != nil {
		call.Name = call.Function.Name
	}
	if call.Arguments == nil {
		call.Arguments = map[string]any{}
	}
	if len(call.Arguments) == 0 && call.Function != nil && call.Function.Arguments != "" {
		_ = json.Unmarshal([]byte(call.Function.Arguments), &call.Arguments)
	}
	return call
}

func isGPT5Model(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "gpt-5")
}

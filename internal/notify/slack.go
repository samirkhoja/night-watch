package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type SlackWebhookClient struct {
	httpClient *http.Client
}

type SlackWebhookPayload struct {
	Text string `json:"text"`
}

func NewSlackWebhookClient() *SlackWebhookClient {
	return &SlackWebhookClient{
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *SlackWebhookClient) Send(ctx context.Context, webhookURL string, payload SlackWebhookPayload) error {
	if c == nil {
		c = NewSlackWebhookClient()
	}
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return fmt.Errorf("slack webhook URL is required")
	}
	payload.Text = strings.TrimSpace(payload.Text)
	if payload.Text == "" {
		return fmt.Errorf("slack payload text is required")
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		webhookURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("create slack webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send slack webhook request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
	return fmt.Errorf(
		"slack webhook returned status %d: %s",
		resp.StatusCode,
		strings.TrimSpace(string(respBody)),
	)
}

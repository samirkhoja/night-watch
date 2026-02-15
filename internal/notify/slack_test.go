package notify

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSlackWebhookClientSendSuccess(t *testing.T) {
	var seen bool
	client := &SlackWebhookClient{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				seen = true
				if r.Method != http.MethodPost {
					t.Fatalf("unexpected method: %s", r.Method)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Fatalf("unexpected content type: %q", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader("ok")),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	err := client.Send(context.Background(), "https://hooks.slack.test/services/T/B/X", SlackWebhookPayload{
		Text: "night watch run completed",
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !seen {
		t.Fatal("expected HTTP transport to be called")
	}
}

func TestSlackWebhookClientSendNonSuccessStatus(t *testing.T) {
	client := &SlackWebhookClient{
		httpClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Body:       io.NopCloser(strings.NewReader("invalid_payload")),
					Header:     make(http.Header),
				}, nil
			}),
		},
	}

	err := client.Send(context.Background(), "https://hooks.slack.test/services/T/B/X", SlackWebhookPayload{
		Text: "night watch run completed",
	})
	if err == nil {
		t.Fatal("expected non-success status error")
	}
	if !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("expected status detail in error, got: %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

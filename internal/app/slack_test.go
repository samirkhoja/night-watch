package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/notify"
)

func TestNotifySlackRunCompletionSendsWebhookWhenEnabled(t *testing.T) {
	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))

	originalFactory := newSlackWebhookSender
	t.Cleanup(func() {
		newSlackWebhookSender = originalFactory
	})
	recorder := &recordingSlackSender{}
	newSlackWebhookSender = func() slackWebhookSender { return recorder }

	out := bytes.NewBuffer(nil)
	application, err := New(bytes.NewBuffer(nil), out, Options{
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := application.cfgManager.SetEnvValue(slackWebhookEnvName, "https://hooks.slack.test/services/T/B/X"); err != nil {
		t.Fatalf("SetEnvValue returned error: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.SlackEnabled = true
	cfg.CloudProvider = "aws"
	cfg.LLMProvider = "openai"
	cfg.LLMModel = "gpt-5.2"

	application.notifySlackRunCompletion(context.Background(), cfg, "check my logs", "investigation complete")

	if got := recorder.calls; got != 1 {
		t.Fatalf("expected exactly one webhook call, got %d", got)
	}
	if recorder.lastURL == "" {
		t.Fatal("expected webhook URL to be passed")
	}
	if !strings.Contains(recorder.lastPayload.Text, "Night Watch run completed") {
		t.Fatalf("unexpected payload text: %s", recorder.lastPayload.Text)
	}
}

func TestNotifySlackRunCompletionSkipsWhenDisabled(t *testing.T) {
	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))

	originalFactory := newSlackWebhookSender
	t.Cleanup(func() {
		newSlackWebhookSender = originalFactory
	})
	recorder := &recordingSlackSender{}
	newSlackWebhookSender = func() slackWebhookSender { return recorder }

	out := bytes.NewBuffer(nil)
	application, err := New(bytes.NewBuffer(nil), out, Options{
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if err := application.cfgManager.SetEnvValue(slackWebhookEnvName, "https://hooks.slack.test/services/T/B/X"); err != nil {
		t.Fatalf("SetEnvValue returned error: %v", err)
	}

	cfg := config.DefaultConfig()
	cfg.SlackEnabled = false
	application.notifySlackRunCompletion(context.Background(), cfg, "check my logs", "investigation complete")

	if got := recorder.calls; got != 0 {
		t.Fatalf("expected no webhook calls when disabled, got %d", got)
	}
}

func TestSlackRunCompletionMessage(t *testing.T) {
	cfg := config.Config{
		LLMProvider:   "openai",
		LLMModel:      "gpt-5.2",
		CloudProvider: "aws",
	}
	msg := slackRunCompletionMessage(cfg, "what errors are in my logs?", "No errors found.")
	if !strings.Contains(msg, "Night Watch run completed") {
		t.Fatalf("unexpected message: %s", msg)
	}
	if !strings.Contains(msg, "Prompt: what errors are in my logs?") {
		t.Fatalf("prompt missing from message: %s", msg)
	}
	if !strings.Contains(msg, "Summary: No errors found.") {
		t.Fatalf("summary missing from message: %s", msg)
	}
}

type recordingSlackSender struct {
	calls       int
	lastURL     string
	lastPayload notify.SlackWebhookPayload
}

func (r *recordingSlackSender) Send(
	_ context.Context,
	webhookURL string,
	payload notify.SlackWebhookPayload,
) error {
	r.calls++
	r.lastURL = webhookURL
	r.lastPayload = payload
	return nil
}

package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/notify"
	"github.com/samirkhoja/night-watch/internal/ui"
)

const slackWebhookEnvName = "SLACK_WEBHOOK_URL"

type slackWebhookSender interface {
	Send(ctx context.Context, webhookURL string, payload notify.SlackWebhookPayload) error
}

var newSlackWebhookSender = func() slackWebhookSender {
	return notify.NewSlackWebhookClient()
}

func (a *App) notifySlackRunCompletion(ctx context.Context, cfg config.Config, prompt, reply string) {
	if a == nil {
		return
	}
	if !cfg.SlackEnabled {
		return
	}

	webhookURL, err := a.cfgManager.GetEnvValue(slackWebhookEnvName)
	if err != nil {
		ui.Warn(a.out, "Slack notification failed: "+err.Error())
		return
	}
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		ui.Warn(a.out, "Slack is enabled but SLACK_WEBHOOK_URL is not configured.")
		return
	}

	client := newSlackWebhookSender()
	message := slackRunCompletionMessage(cfg, prompt, reply)
	if err := client.Send(ctx, webhookURL, notify.SlackWebhookPayload{Text: message}); err != nil {
		ui.Warn(a.out, "Slack notification failed: "+err.Error())
	}
}

func slackRunCompletionMessage(cfg config.Config, prompt, reply string) string {
	var builder strings.Builder
	builder.WriteString(":white_check_mark: Night Watch run completed\n")
	builder.WriteString(fmt.Sprintf("Cloud: %s\n", firstNonBlank(cfg.CloudProvider, "unknown")))
	builder.WriteString(fmt.Sprintf("Model: %s/%s\n", firstNonBlank(cfg.LLMProvider, "unknown"), firstNonBlank(cfg.LLMModel, "unknown")))
	builder.WriteString("Prompt: ")
	builder.WriteString(truncateForSlack(prompt, 350))
	builder.WriteByte('\n')
	builder.WriteString("Summary: ")
	builder.WriteString(truncateForSlack(reply, 1400))
	return builder.String()
}

func truncateForSlack(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(empty)"
	}
	if max <= 0 || len(value) <= max {
		return value
	}
	return strings.TrimSpace(value[:max]) + " ..."
}

func firstNonBlank(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

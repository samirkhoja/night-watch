package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/prompts"
)

const compactionSystemPrompt = `You condense prior conversation history for an operations agent.

Return only this structure:
Compacted context summary:
User goals and constraints:
- ...
Assistant findings/actions:
- ...
Open questions:
- ...

Rules:
- Keep only actionable facts, constraints, decisions, and unresolved questions.
- Do not invent details.
- Keep it concise and easy to continue from.`

func (s *Session) compactMessagesForBudget(messages []agentsdk.Message) ([]agentsdk.Message, bool) {
	return s.compactMessagesForBudgetWithContext(context.Background(), messages)
}

func (s *Session) compactMessagesForBudgetWithContext(ctx context.Context, messages []agentsdk.Message) ([]agentsdk.Message, bool) {
	if ctx == nil {
		ctx = context.Background()
	}

	normalized := normalizeHistory(messages, 0)
	if len(normalized) == 0 {
		return normalized, false
	}

	budget := s.inputTokenBudget()
	if estimateConversationTokens(prompts.AgentSystem, normalized) <= budget {
		return normalized, false
	}

	working := normalized
	summaryTarget := 1200
	if summaryTarget > budget/3 {
		summaryTarget = maxIntValue(400, budget/3)
	}

	for attempt := 0; attempt < 8 && len(working) > 2; attempt++ {
		if estimateConversationTokens(prompts.AgentSystem, working) <= budget {
			return working, true
		}

		keepRecent := 8 - attempt
		if keepRecent < 2 {
			keepRecent = 2
		}
		if keepRecent >= len(working) {
			keepRecent = len(working) - 1
		}
		cut := len(working) - keepRecent
		if cut < 1 {
			cut = 1
		}

		older := working[:cut]
		recent := working[cut:]
		summary := s.summarizeForCompactionWithModel(ctx, older, summaryTarget)
		if strings.TrimSpace(summary) == "" {
			return truncateMessagesToBudget(working, budget), true
		}

		working = append([]agentsdk.Message{{Role: agentsdk.RoleAssistant, Content: summary}}, recent...)
		summaryTarget = maxIntValue(256, int(float64(summaryTarget)*0.8))
	}

	return truncateMessagesToBudget(working, budget), true
}

func (s *Session) summarizeForCompactionWithModel(
	ctx context.Context,
	messages []agentsdk.Message,
	targetTokens int,
) string {
	provider := s.runtimeCompactionProvider()
	if s == nil || provider == nil || len(messages) == 0 {
		return ""
	}

	if targetTokens < 256 {
		targetTokens = 256
	}
	if targetTokens > 2200 {
		targetTokens = 2200
	}

	prompt := buildCompactionPrompt(messages)
	resp, err := provider.Chat(
		ctx,
		[]agentsdk.Message{
			{Role: agentsdk.RoleSystem, Content: compactionSystemPrompt},
			{Role: agentsdk.RoleUser, Content: prompt},
		},
		nil,
		s.runtimeCompactionModel(),
		map[string]any{
			"reasoning_effort":  "low",
			"temperature":       0.2,
			"max_output_tokens": targetTokens,
		},
	)
	if err != nil || resp == nil {
		return ""
	}

	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return ""
	}
	if !strings.Contains(strings.ToLower(summary), "compacted context summary") {
		summary = "Compacted context summary:\n" + summary
	}

	targetChars := targetTokens * 4
	if targetChars < 400 {
		targetChars = 400
	}
	return summarizeSnippet(summary, targetChars)
}

func buildCompactionPrompt(messages []agentsdk.Message) string {
	var builder strings.Builder
	builder.WriteString("Summarize these prior turns for continued incident investigation context.\n")
	builder.WriteString("Preserve decisions, constraints, findings, and unanswered questions.\n\n")
	for idx, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "assistant", "system":
		default:
			role = "user"
		}
		content := summarizeSnippet(msg.Content, 520)
		if strings.TrimSpace(content) == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("%d. %s: %s\n", idx+1, role, content))
	}
	return strings.TrimSpace(builder.String())
}

func summarizeHistoryForSubAgent(messages []agentsdk.Message, maxRunes int) string {
	if len(messages) == 0 {
		return ""
	}
	var builder strings.Builder
	for idx, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		switch role {
		case "assistant", "user", "system":
		default:
			role = "user"
		}
		content := summarizeSnippet(msg.Content, 240)
		if strings.TrimSpace(content) == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("%d. %s: %s\n", idx+1, role, content))
	}
	return summarizeSnippet(builder.String(), maxRunes)
}

func truncateMessagesToBudget(messages []agentsdk.Message, budget int) []agentsdk.Message {
	working := normalizeHistory(messages, 0)
	if len(working) == 0 {
		return working
	}

	for len(working) > 1 && estimateConversationTokens(prompts.AgentSystem, working) > budget {
		working = working[1:]
	}
	if estimateConversationTokens(prompts.AgentSystem, working) <= budget {
		return working
	}

	maxMessageTokens := budget - (estimateTextTokens(prompts.AgentSystem) + 24 + 8)
	if maxMessageTokens < 80 {
		maxMessageTokens = 80
	}
	working[0].Content = summarizeSnippet(working[0].Content, maxMessageTokens*4)
	return working
}

func (s *Session) inputTokenBudget() int {
	maxTokens := s.modelMaxTokens
	if maxTokens <= 0 {
		maxTokens = 32000
	}

	reserved := s.replyMaxTokens + 900
	maxReserve := int(float64(maxTokens) * 0.45)
	if reserved > maxReserve {
		reserved = maxReserve
	}
	if reserved < 1200 {
		reserved = 1200
	}

	budget := maxTokens - reserved
	if budget < 2000 {
		budget = maxIntValue(1200, int(float64(maxTokens)*0.6))
	}
	return budget
}

func estimateConversationTokens(system string, messages []agentsdk.Message) int {
	total := estimateTextTokens(system) + 24
	for _, msg := range messages {
		total += estimateTextTokens(msg.Content) + 8
	}
	return total
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	runes := utf8.RuneCountInString(text)
	approx := runes / 4
	if approx < 1 {
		approx = 1
	}
	return approx
}

func cleanInline(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.Join(strings.Fields(value), " ")
}

func summarizeSnippet(value string, maxRunes int) string {
	value = cleanInline(value)
	if maxRunes <= 0 || utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	if maxRunes <= 3 {
		return firstRunes(value, maxRunes)
	}
	return firstRunes(value, maxRunes-3) + "..."
}

func firstRunes(value string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= n {
		return value
	}
	return string(runes[:n])
}

func inferModelMaxTokens(cfg *config.Config) int {
	if cfg == nil {
		return 32000
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.LLMProvider))
	model := strings.ToLower(strings.TrimSpace(cfg.LLMModel))

	switch provider {
	case "anthropic":
		return 200000
	case "google":
		return 200000
	case "openai":
		if strings.HasPrefix(model, "gpt-5") {
			return 128000
		}
		if strings.HasPrefix(model, "gpt-4.1") {
			return 128000
		}
		if strings.HasPrefix(model, "gpt-4o") {
			return 128000
		}
	}
	return 32000
}

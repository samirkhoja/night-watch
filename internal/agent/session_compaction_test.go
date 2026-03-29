package agent

import (
	"context"
	"strings"
	"testing"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/prompts"
)

type mockCompactionProvider struct {
	reply string
	err   error
	calls int
}

func (m *mockCompactionProvider) DefaultModel() string {
	return "mock-compaction"
}

func (m *mockCompactionProvider) Chat(
	ctx context.Context,
	messages []agentsdk.Message,
	tools []agentsdk.ToolDefinition,
	model string,
	options map[string]any,
) (*agentsdk.LLMResponse, error) {
	_ = ctx
	_ = messages
	_ = tools
	_ = model
	_ = options
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return &agentsdk.LLMResponse{Content: m.reply}, nil
}

func TestCompactMessagesForBudget(t *testing.T) {
	session := NewSession(nil, &config.Config{
		LLMProvider: "openai",
		LLMModel:    "gpt-4o-mini",
	}, nil, nil)
	session.modelMaxTokens = 2400
	session.replyMaxTokens = 300

	var messages []agentsdk.Message
	for i := 0; i < 20; i++ {
		role := agentsdk.RoleUser
		if i%2 == 1 {
			role = agentsdk.RoleAssistant
		}
		messages = append(messages, agentsdk.Message{
			Role:    role,
			Content: strings.Repeat("important context item ", 80),
		})
	}

	compacted, didCompact := session.compactMessagesForBudgetWithContext(context.Background(), messages)
	if !didCompact {
		t.Fatalf("expected compaction to occur")
	}
	if len(compacted) == 0 {
		t.Fatalf("expected compacted messages to be non-empty")
	}
	if estimateConversationTokens(prompts.AgentSystem, compacted) > session.inputTokenBudget() {
		t.Fatalf("expected compacted conversation to fit token budget")
	}
}

func TestCompactMessagesForBudgetNoop(t *testing.T) {
	session := NewSession(nil, &config.Config{
		LLMProvider: "openai",
		LLMModel:    "gpt-4o-mini",
	}, nil, nil)
	session.modelMaxTokens = 32000
	session.replyMaxTokens = 1200

	messages := []agentsdk.Message{
		{Role: agentsdk.RoleUser, Content: "check logs in us-east-1"},
		{Role: agentsdk.RoleAssistant, Content: "Which profile should I use?"},
	}
	compacted, didCompact := session.compactMessagesForBudgetWithContext(context.Background(), messages)
	if didCompact {
		t.Fatalf("expected no compaction for small conversation")
	}
	if len(compacted) != len(messages) {
		t.Fatalf("expected message count unchanged")
	}
}

func TestCompactMessagesForBudgetUsesCompactionProvider(t *testing.T) {
	session := NewSession(nil, &config.Config{
		LLMProvider: "openai",
		LLMModel:    "gpt-5.4",
	}, nil, nil)
	session.modelMaxTokens = 2400
	session.replyMaxTokens = 300

	mockProvider := &mockCompactionProvider{
		reply: "Compacted context summary:\nUser goals and constraints:\n- find root cause\nAssistant findings/actions:\n- checked cloud logs\nOpen questions:\n- none",
	}
	session.SetCompactionProvider(mockProvider)

	var messages []agentsdk.Message
	for i := 0; i < 20; i++ {
		role := agentsdk.RoleUser
		if i%2 == 1 {
			role = agentsdk.RoleAssistant
		}
		messages = append(messages, agentsdk.Message{
			Role:    role,
			Content: strings.Repeat("important context item ", 80),
		})
	}

	compacted, didCompact := session.compactMessagesForBudgetWithContext(context.Background(), messages)
	if !didCompact {
		t.Fatalf("expected compaction to occur")
	}
	if len(compacted) == 0 {
		t.Fatalf("expected compacted messages")
	}
	if mockProvider.calls == 0 {
		t.Fatalf("expected compaction provider to be called")
	}
}

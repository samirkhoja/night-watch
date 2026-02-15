package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/samirkhoja/night-watch/internal/config"
	"github.com/samirkhoja/night-watch/internal/llm"
	"github.com/samirkhoja/night-watch/internal/prompts"
)

type mockCompactionClient struct {
	reply   string
	err     error
	calls   int
	lastReq llm.GenerateRequest
}

func (m *mockCompactionClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	m.calls++
	m.lastReq = req
	if m.err != nil {
		return llm.GenerateResponse{}, m.err
	}
	return llm.GenerateResponse{
		Reply: m.reply,
	}, nil
}

func (m *mockCompactionClient) Name() string {
	return "mock-compaction"
}

func TestCompactMessagesForBudget(t *testing.T) {
	session := NewSession(nil, &config.Config{
		LLMProvider: "openai",
		LLMModel:    "gpt-4o-mini",
	}, nil, nil)
	session.modelMaxTokens = 2400
	session.replyMaxTokens = 300

	var messages []llm.Message
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, llm.Message{
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

	messages := []llm.Message{
		{Role: "user", Content: "check logs in us-east-1"},
		{Role: "assistant", Content: "Which profile should I use?"},
	}
	compacted, didCompact := session.compactMessagesForBudgetWithContext(context.Background(), messages)
	if didCompact {
		t.Fatalf("expected no compaction for small conversation")
	}
	if len(compacted) != len(messages) {
		t.Fatalf("expected message count unchanged")
	}
}

func TestCompactMessagesForBudgetUsesCompactionClient(t *testing.T) {
	session := NewSession(nil, &config.Config{
		LLMProvider: "openai",
		LLMModel:    "gpt-5.2",
	}, nil, nil)
	session.modelMaxTokens = 2400
	session.replyMaxTokens = 300

	mockClient := &mockCompactionClient{
		reply: "Compacted context summary:\nUser goals and constraints:\n- find root cause\nAssistant findings/actions:\n- checked cloud logs\nOpen questions:\n- none",
	}
	session.SetCompactionClient(mockClient)

	var messages []llm.Message
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		messages = append(messages, llm.Message{
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
	if mockClient.calls == 0 {
		t.Fatalf("expected compaction client to be called")
	}
	if !mockClient.lastReq.DisableTools {
		t.Fatalf("expected compaction request to disable tools")
	}
}

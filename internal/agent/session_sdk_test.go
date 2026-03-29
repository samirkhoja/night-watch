package agent

import (
	"bytes"
	"context"
	"testing"
	"time"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
)

type stubSDKProvider struct {
	reply string
	err   error
	calls int
}

func (p *stubSDKProvider) DefaultModel() string {
	return "stub-model"
}

func (p *stubSDKProvider) Chat(
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
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &agentsdk.LLMResponse{Content: p.reply}, nil
}

func TestSummarizeSDKRunCountsOperationalActions(t *testing.T) {
	stats := summarizeSDKRun([]agentsdk.Message{
		{
			Role: agentsdk.RoleAssistant,
			ToolCalls: []agentsdk.ToolCall{
				{ID: "call_1", Name: "run_command"},
				{ID: "call_2", Name: "spawn_sub_agent"},
			},
		},
		{Role: agentsdk.RoleTool, ToolCallID: "call_1", Content: "command complete"},
		{Role: agentsdk.RoleTool, ToolCallID: "call_2", Content: "sub-agent complete"},
	})

	if stats.ActionRuns != 2 {
		t.Fatalf("expected 2 action runs, got %d", stats.ActionRuns)
	}
	if stats.OperationalActions != 2 {
		t.Fatalf("expected 2 operational actions, got %d", stats.OperationalActions)
	}
	if stats.CommandActions != 1 {
		t.Fatalf("expected 1 command action, got %d", stats.CommandActions)
	}
	if stats.SubAgentActions != 1 {
		t.Fatalf("expected 1 sub-agent action, got %d", stats.SubAgentActions)
	}
}

func TestSDKGuardrailFollowUpRequestsToolExecution(t *testing.T) {
	followUp, status := sdkGuardrailFollowUp(1, "I found likely issues.", sdkRunStats{}, true, false)
	if followUp == "" {
		t.Fatalf("expected tool-execution follow-up")
	}
	if status == "" {
		t.Fatalf("expected user-visible status for follow-up")
	}
}

func TestSDKGuardrailFollowUpSkipsClarifyingQuestions(t *testing.T) {
	followUp, status := sdkGuardrailFollowUp(1, "Which AWS region should I use?", sdkRunStats{}, true, true)
	if followUp != "" || status != "" {
		t.Fatalf("expected clarifying question to bypass follow-up, got followUp=%q status=%q", followUp, status)
	}
}

func TestSDKGuardrailFollowUpRequiresDelegationAfterCommands(t *testing.T) {
	followUp, status := sdkGuardrailFollowUp(3, "I gathered evidence.", sdkRunStats{
		ActionRuns:         1,
		OperationalActions: 1,
		CommandActions:     1,
	}, false, true)
	if followUp == "" {
		t.Fatalf("expected delegation follow-up after command evidence collection")
	}
	if status == "" {
		t.Fatalf("expected status for delegation follow-up")
	}
}

func TestLoadHistoryFromStoreUsesSessionStoreState(t *testing.T) {
	store := agentsdk.NewMemorySessionStore()
	session := NewSession(nil, &config.Config{}, nil, nil)
	session.SetSessionStore(store)
	session.SetSessionID("resume")

	err := store.Save(context.Background(), "resume", &agentsdk.SessionState{
		Messages: []agentsdk.Message{
			{Role: agentsdk.RoleUser, Content: "inspect logs"},
			{Role: agentsdk.RoleAssistant, Content: "checking"},
			{Role: agentsdk.RoleTool, Content: "ignored tool result"},
		},
	})
	if err != nil {
		t.Fatalf("save store state: %v", err)
	}

	restored, err := session.LoadHistoryFromStore(context.Background())
	if err != nil {
		t.Fatalf("LoadHistoryFromStore returned error: %v", err)
	}
	if !restored {
		t.Fatalf("expected history to be restored from store")
	}

	history := session.History()
	if len(history) != 2 {
		t.Fatalf("expected normalized history with 2 messages, got %d", len(history))
	}
}

func TestResumeActiveRunCompletesPersistedRun(t *testing.T) {
	store := agentsdk.NewMemorySessionStore()
	provider := &stubSDKProvider{reply: "resumed reply"}
	session := NewSession(
		provider,
		&config.Config{LLMProvider: "openai", LLMModel: "gpt-5.4"},
		nil,
		bytes.NewBuffer(nil),
	)
	session.SetSessionStore(store)
	session.SetSessionID("resume-active")

	now := time.Now().UTC()
	err := store.Save(context.Background(), "resume-active", &agentsdk.SessionState{
		Messages: []agentsdk.Message{
			{Role: agentsdk.RoleUser, Content: "inspect logs"},
		},
		ActiveRun: &agentsdk.RunRecord{
			ID:        "run_123",
			Input:     "inspect logs",
			Status:    agentsdk.RunStatusInterrupted,
			StartedAt: now,
			UpdatedAt: now,
		},
	})
	if err != nil {
		t.Fatalf("save store state: %v", err)
	}

	reply, resumed, err := session.ResumeActiveRun(context.Background(), true)
	if err != nil {
		t.Fatalf("ResumeActiveRun returned error: %v", err)
	}
	if !resumed {
		t.Fatalf("expected active run to be resumed")
	}
	if reply != "resumed reply" {
		t.Fatalf("unexpected resumed reply: %q", reply)
	}
	if provider.calls != 1 {
		t.Fatalf("expected provider to be called once, got %d", provider.calls)
	}

	state, err := store.Load(context.Background(), "resume-active")
	if err != nil {
		t.Fatalf("load store state: %v", err)
	}
	if state.ActiveRun != nil {
		t.Fatalf("expected active run to be cleared after resume, got %+v", state.ActiveRun)
	}
	if len(state.Runs) != 1 {
		t.Fatalf("expected completed run to be recorded, got %d", len(state.Runs))
	}
	if len(session.History()) != 2 {
		t.Fatalf("expected resumed session history to include assistant reply")
	}
}

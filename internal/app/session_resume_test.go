package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/agent"
	"github.com/samirkhoja/night-watch/internal/config"
)

type stubAskSession struct {
	sessionID string
	history   []agentsdk.Message
	reply     string
	err       error
	calls     int
}

func (s *stubAskSession) Ask(ctx context.Context, prompt string) (string, error) {
	_ = ctx
	_ = prompt
	s.calls++
	return s.reply, s.err
}

func (s *stubAskSession) SessionID() string {
	return s.sessionID
}

func (s *stubAskSession) History() []agentsdk.Message {
	out := make([]agentsdk.Message, len(s.history))
	copy(out, s.history)
	return out
}

type stubProvider struct {
	reply string
	err   error
	calls int
}

func (p *stubProvider) DefaultModel() string {
	return "stub-model"
}

func (p *stubProvider) Chat(
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

func TestRunAskWithSessionPersistsHistoryOnError(t *testing.T) {
	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))

	application, err := New(bytes.NewBuffer(nil), bytes.NewBuffer(nil), Options{
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	session := &stubAskSession{
		sessionID: "session_123",
		history: []agentsdk.Message{
			{Role: agentsdk.RoleUser, Content: "check recent errors"},
		},
		err: errors.New("provider failed"),
	}

	err = application.runAskWithSession(context.Background(), config.DefaultConfig(), session, "check recent errors")
	if err == nil {
		t.Fatalf("expected runAskWithSession to return the ask error")
	}

	metas, err := application.sessionLogs.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(metas) != 1 {
		t.Fatalf("expected 1 saved session, got %d", len(metas))
	}
	snapshot, err := application.sessionLogs.LoadSnapshot(metas[0].Path)
	if err != nil {
		t.Fatalf("LoadSnapshot returned error: %v", err)
	}
	if snapshot.SessionID != "session_123" {
		t.Fatalf("expected session id to be saved on error, got %q", snapshot.SessionID)
	}
}

func TestResumeSessionHistoryResumesActiveRun(t *testing.T) {
	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))

	input := bytes.NewBufferString("1\n")
	output := bytes.NewBuffer(nil)
	application, err := New(input, output, Options{
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	provider := &stubProvider{reply: "resumed reply"}
	session := agent.NewSession(
		provider,
		&config.Config{LLMProvider: "openai", LLMModel: "gpt-5.4"},
		nil,
		output,
	)
	store := agentsdk.NewMemorySessionStore()
	session.SetSessionStore(store)
	session.SetSessionID("session_456")

	now := time.Now().UTC()
	state := &agentsdk.SessionState{
		Messages: []agentsdk.Message{
			{Role: agentsdk.RoleUser, Content: "inspect logs"},
		},
		ActiveRun: &agentsdk.RunRecord{
			ID:        "run_1",
			Input:     "inspect logs",
			Status:    agentsdk.RunStatusInterrupted,
			StartedAt: now,
			UpdatedAt: now,
		},
	}

	_, err = application.sessionLogs.Save(config.DefaultConfig(), "session_456", []agentsdk.Message{
		{Role: agentsdk.RoleUser, Content: "inspect logs"},
	}, state)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	err = application.resumeSessionHistory(context.Background(), session, true)
	if err != nil {
		t.Fatalf("resumeSessionHistory returned error: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("expected interrupted run to be resumed once, got %d provider calls", provider.calls)
	}
	if len(session.History()) != 2 {
		t.Fatalf("expected resumed session history to include assistant reply")
	}
}

func TestResumeSessionHistoryCanHideResumedReply(t *testing.T) {
	t.Setenv("NIGHTWATCH_CONFIG_DIR", filepath.Join(t.TempDir(), "cfg"))

	input := bytes.NewBufferString("1\n")
	output := bytes.NewBuffer(nil)
	application, err := New(input, output, Options{
		WorkingDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}

	provider := &stubProvider{reply: "resumed reply"}
	session := agent.NewSession(
		provider,
		&config.Config{LLMProvider: "openai", LLMModel: "gpt-5.4"},
		nil,
		output,
	)
	session.SetSessionStore(agentsdk.NewMemorySessionStore())
	session.SetSessionID("session_789")

	now := time.Now().UTC()
	state := &agentsdk.SessionState{
		Messages: []agentsdk.Message{
			{Role: agentsdk.RoleUser, Content: "inspect logs"},
		},
		ActiveRun: &agentsdk.RunRecord{
			ID:        "run_2",
			Input:     "inspect logs",
			Status:    agentsdk.RunStatusInterrupted,
			StartedAt: now,
			UpdatedAt: now,
		},
	}

	_, err = application.sessionLogs.Save(config.DefaultConfig(), "session_789", []agentsdk.Message{
		{Role: agentsdk.RoleUser, Content: "inspect logs"},
	}, state)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	err = application.resumeSessionHistory(context.Background(), session, false)
	if err != nil {
		t.Fatalf("resumeSessionHistory returned error: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("expected interrupted run to be resumed once, got %d provider calls", provider.calls)
	}
	if strings.Contains(output.String(), "resumed reply") {
		t.Fatalf("expected resumed reply to stay hidden in ask-style resume flow, got output %q", output.String())
	}
}

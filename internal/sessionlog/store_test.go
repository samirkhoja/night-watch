package sessionlog

import (
	"testing"
	"time"

	agentsdk "github.com/samirkhoja/agent-sdk"
	"github.com/samirkhoja/night-watch/internal/config"
)

func TestSaveAndLoadSnapshotPreservesSessionID(t *testing.T) {
	manager := &Manager{dir: t.TempDir()}
	meta, err := manager.Save(config.Config{
		LLMProvider:   "openai",
		LLMModel:      "gpt-5.4",
		CloudProvider: "aws",
	}, "session_123", []agentsdk.Message{
		{Role: agentsdk.RoleUser, Content: "check recent errors"},
		{Role: agentsdk.RoleAssistant, Content: "looking now"},
	}, nil)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	snapshot, err := manager.LoadSnapshot(meta.Path)
	if err != nil {
		t.Fatalf("LoadSnapshot returned error: %v", err)
	}
	if snapshot.SessionID != "session_123" {
		t.Fatalf("expected session id to round-trip, got %q", snapshot.SessionID)
	}
	if len(snapshot.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(snapshot.Messages))
	}
	if snapshot.State == nil || len(snapshot.State.Messages) != 2 {
		t.Fatalf("expected fallback session state to be preserved")
	}
}

func TestSaveAndLoadSnapshotPreservesFullSessionState(t *testing.T) {
	manager := &Manager{dir: t.TempDir()}
	now := time.Now().UTC()
	state := &agentsdk.SessionState{
		Messages: []agentsdk.Message{
			{Role: agentsdk.RoleUser, Content: "inspect logs"},
			{Role: agentsdk.RoleAssistant, Content: "checking"},
			{Role: agentsdk.RoleTool, ToolCallID: "call_1", Content: "tool output"},
		},
		ActiveRun: &agentsdk.RunRecord{
			ID:        "run_1",
			Input:     "inspect logs",
			Status:    agentsdk.RunStatusInterrupted,
			StartedAt: now,
			UpdatedAt: now,
		},
	}
	meta, err := manager.Save(config.Config{
		LLMProvider:   "openai",
		LLMModel:      "gpt-5.4",
		CloudProvider: "aws",
	}, "session_456", normalizeMessages(state.Messages), state)
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	snapshot, err := manager.LoadSnapshot(meta.Path)
	if err != nil {
		t.Fatalf("LoadSnapshot returned error: %v", err)
	}
	if snapshot.State == nil || snapshot.State.ActiveRun == nil {
		t.Fatalf("expected active run to round-trip in snapshot state")
	}
	if snapshot.State.ActiveRun.ID != "run_1" {
		t.Fatalf("expected active run id to round-trip, got %+v", snapshot.State.ActiveRun)
	}
}

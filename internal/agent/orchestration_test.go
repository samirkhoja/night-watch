package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/samirkhoja/night-watch/internal/llm"
)

type sequenceClient struct {
	responses []llm.GenerateResponse
	err       error
	calls     int
}

func (c *sequenceClient) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if c.err != nil {
		return llm.GenerateResponse{}, c.err
	}
	if c.calls >= len(c.responses) {
		return llm.GenerateResponse{}, errors.New("no more responses")
	}
	out := c.responses[c.calls]
	c.calls++
	return out, nil
}

func (c *sequenceClient) Name() string {
	return "sequence"
}

func TestRunActionLoopReturnsFinalReply(t *testing.T) {
	client := &sequenceClient{
		responses: []llm.GenerateResponse{
			{Reply: "Done"},
		},
	}
	session := &Session{client: client, replyMaxTokens: 512}

	result, err := session.runActionLoop(context.Background(), actionLoopConfig{
		SystemPrompt: "system",
		MaxTokens:    256,
		EmptyReply:   "fallback",
		ExecuteAction: func(ctx context.Context, action agentAction) (string, error) {
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("runActionLoop returned error: %v", err)
	}
	if result.Reply != "Done" {
		t.Fatalf("unexpected reply: got=%q", result.Reply)
	}
	if result.Steps != 1 {
		t.Fatalf("expected 1 step, got %d", result.Steps)
	}
	if result.ActionRuns != 0 {
		t.Fatalf("expected 0 action runs, got %d", result.ActionRuns)
	}
	if result.ReachedMaxSteps {
		t.Fatalf("did not expect max steps")
	}
}

func TestRunActionLoopTracksActionsAndMaxSteps(t *testing.T) {
	client := &sequenceClient{
		responses: []llm.GenerateResponse{
			{
				ToolCalls: []llm.ToolCall{
					{
						Name: "run_command",
						Arguments: map[string]interface{}{
							"reason":  "check",
							"command": "pwd",
						},
					},
				},
			},
		},
	}
	session := &Session{client: client, replyMaxTokens: 512}

	actionCalls := 0
	result, err := session.runActionLoop(context.Background(), actionLoopConfig{
		SystemPrompt: "system",
		MaxTokens:    256,
		MaxSteps:     1,
		EmptyReply:   "fallback",
		ExecuteAction: func(ctx context.Context, action agentAction) (string, error) {
			actionCalls++
			return "ok", nil
		},
	})
	if err != nil {
		t.Fatalf("runActionLoop returned error: %v", err)
	}
	if !result.ReachedMaxSteps {
		t.Fatalf("expected max steps to be reached")
	}
	if result.Steps != 1 {
		t.Fatalf("expected 1 step, got %d", result.Steps)
	}
	if result.ActionRuns != 1 {
		t.Fatalf("expected 1 action run, got %d", result.ActionRuns)
	}
	if actionCalls != 1 {
		t.Fatalf("expected executeAction to run once, got %d", actionCalls)
	}
}

func TestRunActionLoopReturnsPartialStateOnGenerateError(t *testing.T) {
	session := &Session{
		client:         &sequenceClient{},
		replyMaxTokens: 512,
	}

	result, err := session.runActionLoop(context.Background(), actionLoopConfig{
		SystemPrompt: "system",
		MaxTokens:    256,
		ExecuteAction: func(ctx context.Context, action agentAction) (string, error) {
			return "ok", nil
		},
	})
	if err == nil {
		t.Fatalf("expected generate error")
	}
	if result.Steps != 1 {
		t.Fatalf("expected step count to be preserved on error, got %d", result.Steps)
	}
	if result.ActionRuns != 0 {
		t.Fatalf("expected action runs to stay zero, got %d", result.ActionRuns)
	}
}

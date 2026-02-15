package agent

import (
	"testing"

	"github.com/samirkhoja/night-watch/internal/llm"
)

func TestPlanFromGenerateResponseMapsToolCalls(t *testing.T) {
	resp := llm.GenerateResponse{
		Reply: "working on it",
		ToolCalls: []llm.ToolCall{
			{
				Name: "status",
				Arguments: map[string]interface{}{
					"message": "checking auth",
				},
			},
			{
				Name: "run_command",
				Arguments: map[string]interface{}{
					"reason":      "verify AWS identity context",
					"command":     "aws sts get-caller-identity",
					"cwd":         ".",
					"timeout_sec": float64(25),
				},
			},
			{
				Name: "spawn_sub_agent",
				Arguments: map[string]interface{}{
					"goal":      "Correlate gathered errors with recent commits",
					"max_steps": float64(4),
				},
			},
		},
	}

	plan := planFromGenerateResponse(resp)
	if plan.Reply != "working on it" {
		t.Fatalf("unexpected reply: %q", plan.Reply)
	}
	if len(plan.Actions) != 3 {
		t.Fatalf("expected 3 actions, got %d", len(plan.Actions))
	}

	if plan.Actions[0].Type != "status" || plan.Actions[0].Message != "checking auth" {
		t.Fatalf("unexpected status mapping: %+v", plan.Actions[0])
	}
	if plan.Actions[1].Type != "run_command" {
		t.Fatalf("unexpected run_command type: %+v", plan.Actions[1])
	}
	if plan.Actions[1].Reason != "verify AWS identity context" {
		t.Fatalf("unexpected run_command reason: %+v", plan.Actions[1])
	}
	if plan.Actions[1].Command != "aws sts get-caller-identity" || plan.Actions[1].TimeoutSec != 25 {
		t.Fatalf("unexpected run_command mapping: %+v", plan.Actions[1])
	}
	if plan.Actions[2].Type != "spawn_sub_agent" {
		t.Fatalf("unexpected spawn_sub_agent type: %+v", plan.Actions[2])
	}
	if got := stringParam(plan.Actions[2].Params, "goal"); got != "Correlate gathered errors with recent commits" {
		t.Fatalf("unexpected spawn_sub_agent goal: %q", got)
	}
}

package agent

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestIsAutoApprovedLowRiskCommand(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{command: "ls", want: true},
		{command: "ls -la", want: true},
		{command: "pwd", want: true},
		{command: "whoami", want: true},
		{command: "date", want: true},
		{command: "which aws", want: true},
		{command: "AWS_PROFILE=prod ls", want: true},
		{command: "grep -R error .", want: false},
		{command: "ls; rm -rf /", want: false},
		{command: "pwd && whoami", want: false},
		{command: "pwd\nls", want: false},
		{command: "python3 - <<'PY'\nprint('x')\nPY", want: false},
	}

	for _, tc := range tests {
		got := isAutoApprovedLowRiskCommand(tc.command)
		if got != tc.want {
			t.Fatalf("isAutoApprovedLowRiskCommand(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}

func TestExecuteCommandActionBlockedEvenWithAutoApproval(t *testing.T) {
	reader := strings.NewReader("")
	approval := NewApprovalManager(
		reader,
		bufio.NewReader(reader),
		io.Discard,
		ApprovalOptions{AutoApprove: true},
	)
	session := &Session{
		approval: approval,
		out:      io.Discard,
	}
	got, err := session.executeCommandAction(context.Background(), agentAction{
		Type:    "run_command",
		Command: "rm -rf /",
		Cwd:     ".",
	})
	if err != nil {
		t.Fatalf("executeCommandAction returned error: %v", err)
	}
	if got != "blocked by safety policy" {
		t.Fatalf("unexpected blocked result: %q", got)
	}
}

func TestExecuteCommandActionWarnsInDangerousFlowWithAutoApproval(t *testing.T) {
	reader := strings.NewReader("")
	var out bytes.Buffer
	approval := NewApprovalManager(
		reader,
		bufio.NewReader(reader),
		&out,
		ApprovalOptions{AutoApprove: true},
	)
	session := &Session{
		approval: approval,
		out:      &out,
	}
	_, err := session.executeCommandAction(context.Background(), agentAction{
		Type:    "run_command",
		Command: "echo hello",
		Cwd:     ".",
	})
	if err != nil {
		t.Fatalf("executeCommandAction returned error: %v", err)
	}
	if !strings.Contains(out.String(), "dangerous flow: auto-approval enabled; running command without approval prompt") {
		t.Fatalf("expected dangerous-flow warning in output, got: %q", out.String())
	}
}

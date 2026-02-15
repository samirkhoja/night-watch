package agent

import (
	"bufio"
	"context"
	"io"
	"strings"
	"testing"
)

func TestParseNumericChoice(t *testing.T) {
	idx, ok := parseNumericChoice("2", 4)
	if !ok || idx != 1 {
		t.Fatalf("expected numeric choice 2 -> index 1, got ok=%v idx=%d", ok, idx)
	}

	if _, ok := parseNumericChoice("0", 4); ok {
		t.Fatalf("expected 0 to be invalid")
	}
	if _, ok := parseNumericChoice("9", 4); ok {
		t.Fatalf("expected out-of-range to be invalid")
	}
}

func TestParseDecision(t *testing.T) {
	tests := []struct {
		input string
		want  ApprovalDecision
	}{
		{"allow", AllowOnce},
		{"always allow", AllowAlways},
		{"reject", RejectOnce},
		{"always reject", RejectAlways},
		{"unknown", ""},
	}

	for _, tc := range tests {
		got := parseDecision(tc.input)
		if got != tc.want {
			t.Fatalf("parseDecision(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestPolicyKeyScopedPerCommandName(t *testing.T) {
	a := policyKey(".", "pwd")
	b := policyKey(".", "pwd -P")
	if a != b {
		t.Fatalf("expected same policy key for same command name, got %q vs %q", a, b)
	}

	c := policyKey(".", "ls -la")
	if a == c {
		t.Fatalf("expected different policy keys for different command names")
	}
}

func TestCommandPolicyNameExtraction(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{command: "pwd", want: "pwd"},
		{command: "AWS_PROFILE=prod aws logs describe-log-groups", want: "aws"},
		{command: "env FOO=bar grep -R error .", want: "grep"},
		{command: "sudo /bin/ls -la", want: "ls"},
		{command: "python3 - <<'PY'\nprint('ok')\nPY", want: "python3"},
	}

	for _, tc := range tests {
		got := commandPolicyName(tc.command)
		if got != tc.want {
			t.Fatalf("commandPolicyName(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestApprovalRequestAutoApproveBypassesPrompt(t *testing.T) {
	reader := strings.NewReader("")
	approval := NewApprovalManager(
		reader,
		bufio.NewReader(reader),
		io.Discard,
		ApprovalOptions{AutoApprove: true},
	)
	approved, err := approval.Request(context.Background(), "grep -R error .", ".", 20)
	if err != nil {
		t.Fatalf("Request returned error: %v", err)
	}
	if !approved {
		t.Fatal("expected approval when auto-approval is enabled")
	}
}

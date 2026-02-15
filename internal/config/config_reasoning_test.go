package config

import "testing"

func TestNormalizeReasoningEffort(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "low", want: "low"},
		{input: "LOW", want: "low"},
		{input: "medium", want: "medium"},
		{input: "high", want: "high"},
		{input: "", want: "medium"},
		{input: "unexpected", want: "medium"},
	}

	for _, tc := range tests {
		got := NormalizeReasoningEffort(tc.input)
		if got != tc.want {
			t.Fatalf("NormalizeReasoningEffort(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

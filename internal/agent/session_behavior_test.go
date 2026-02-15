package agent

import "testing"

func TestLikelyNeedsTooling(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "What errors are in my logs", want: true},
		{input: "Correlate exceptions with recent commits", want: true},
		{input: "hello there", want: false},
	}

	for _, tc := range tests {
		got := likelyNeedsTooling(tc.input)
		if got != tc.want {
			t.Fatalf("likelyNeedsTooling(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestLooksLikeClarifyingQuestion(t *testing.T) {
	if !looksLikeClarifyingQuestion("Which AWS region should I use?") {
		t.Fatalf("expected clarifying question to be detected")
	}
	if looksLikeClarifyingQuestion("I analyzed your logs and found no errors.") {
		t.Fatalf("expected non-question statement to be false")
	}
}

func TestLikelyNeedsCorrelationDelegation(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{input: "Correlate exceptions with recent commits", want: true},
		{input: "Which commit likely caused this deployment regression?", want: true},
		{input: "List log groups in us-east-1", want: false},
	}

	for _, tc := range tests {
		got := likelyNeedsCorrelationDelegation(tc.input)
		if got != tc.want {
			t.Fatalf("likelyNeedsCorrelationDelegation(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

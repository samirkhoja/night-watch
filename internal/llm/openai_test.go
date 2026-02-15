package llm

import "testing"

func TestOpenAIInputPartType(t *testing.T) {
	tests := []struct {
		name string
		role string
		want string
	}{
		{
			name: "assistant role uses output text",
			role: "assistant",
			want: "output_text",
		},
		{
			name: "assistant role is case insensitive",
			role: "ASSISTANT",
			want: "output_text",
		},
		{
			name: "user role uses input text",
			role: "user",
			want: "input_text",
		},
		{
			name: "system role uses input text",
			role: "system",
			want: "input_text",
		},
		{
			name: "unknown role falls back to input text",
			role: "tool",
			want: "input_text",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := openAIInputPartType(tc.role)
			if got != tc.want {
				t.Fatalf("openAIInputPartType(%q) = %q, want %q", tc.role, got, tc.want)
			}
		})
	}
}

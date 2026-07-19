package main

import "testing"

func TestNormalizeVersionOutput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		parts  int
		want   string
	}{
		{name: "uv 带平台后缀", output: "uv 0.11.29 (x86_64-unknown-linux-gnu)\n", parts: 2, want: "uv 0.11.29"},
		{name: "mise 带平台后缀", output: "2026.7.7 linux-x64 (2026-07-07)\n", parts: 1, want: "2026.7.7"},
		{name: "Codex", output: "codex-cli 0.142.5\n", parts: 2, want: "codex-cli 0.142.5"},
		{name: "Corepack", output: "0.35.0\n", parts: 1, want: "0.35.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeVersionOutput(test.output, test.parts)
			if err != nil {
				t.Fatalf("normalizeVersionOutput() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("normalizeVersionOutput() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeVersionOutputRejectsMissingParts(t *testing.T) {
	t.Parallel()
	if _, err := normalizeVersionOutput("uv", 2); err == nil {
		t.Fatal("normalizeVersionOutput() error = nil, want non-nil")
	}
}

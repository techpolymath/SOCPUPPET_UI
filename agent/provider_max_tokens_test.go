package agent

import (
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestResolvedMaxTokensField(t *testing.T) {
	for _, tc := range []struct {
		name, model, base, explicit, want string
	}{
		{"official default gpt-6", "gpt-6.1-sol", "", "", llm.MaxTokensFieldCompletion},
		{"official URL gpt-5", "gpt-5", "https://api.openai.com/v1", "", llm.MaxTokensFieldCompletion},
		{"official o-series", "o3-pro", "https://api.openai.com:443/v1", "", llm.MaxTokensFieldCompletion},
		{"legacy official model", "gpt-4o", "", "", ""},
		{"compatible gateway stays legacy", "gpt-6.1-sol", "https://gateway.example/v1", "", ""},
		{"explicit compatible gateway", "gpt-6.1-sol", "https://gateway.example/v1", llm.MaxTokensFieldCompletion, llm.MaxTokensFieldCompletion},
		{"nonstandard official port stays legacy", "gpt-6.1-sol", "https://api.openai.com:8443/v1", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolvedMaxTokensField(Config{
				Format: llm.FormatOpenAI, Model: tc.model, BaseURL: tc.base, MaxTokensField: tc.explicit,
			})
			if got != tc.want {
				t.Fatalf("resolvedMaxTokensField()=%q, want %q", got, tc.want)
			}
		})
	}

	if got := resolvedMaxTokensField(Config{Format: llm.FormatAnthropic, Model: "gpt-6.1-sol"}); got != "" {
		t.Fatalf("non-OpenAI format resolved field %q, want empty", got)
	}
}

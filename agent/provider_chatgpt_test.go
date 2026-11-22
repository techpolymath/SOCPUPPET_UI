package agent

import (
	"context"
	"encoding/json"
	"testing"
)

type fixedChatGPTToken string

func (f fixedChatGPTToken) AccessToken(context.Context) (string, error) { return string(f), nil }
func (fixedChatGPTToken) InvalidateAccessToken(string)                  {}

func TestNormalizeChatGPTPlanBody(t *testing.T) {
	raw := []byte(`{
      "model":"gpt-test",
      "input":[{"role":"user","content":"hello"}],
      "tools":[{"type":"function","name":"Lookup","parameters":{"type":"object"}}],
      "max_output_tokens":8192,
      "temperature":0.2,
      "prompt_cache_retention":"24h",
      "stream":false,
      "store":true
    }`)
	gotRaw, err := normalizeChatGPTPlanBody(raw)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(gotRaw, &got); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"tools", "max_output_tokens", "temperature", "prompt_cache_retention"} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("forbidden field %q remained in %s", forbidden, gotRaw)
		}
	}
	if got["stream"] != true || got["store"] != false {
		t.Fatalf("stream/store = %v/%v, want true/false", got["stream"], got["store"])
	}
	input, ok := got["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v, want additional_tools + user message", got["input"])
	}
	add, _ := input[0].(map[string]any)
	if add["type"] != "additional_tools" || add["role"] != "developer" {
		t.Fatalf("additional_tools item = %#v", add)
	}
	if tools, ok := add["tools"].([]any); !ok || len(tools) != 1 {
		t.Fatalf("additional_tools.tools = %#v", add["tools"])
	}
}

func TestChatGPTConfigIsKeylessButRequiresTokenSource(t *testing.T) {
	c := ConfigFrom("chatgpt", "gpt-test", "https://evil.example/v1", "", "")
	if c.Format != FormatChatGPT || c.BaseURL != "https://api.openai.com/v1" || c.NeedsAPIKey() {
		t.Fatalf("unexpected config: format=%q base=%q needs_key=%v", c.Format, c.BaseURL, c.NeedsAPIKey())
	}
	if _, err := c.NewProvider(); err == nil {
		t.Fatal("provider without a ChatGPT token source unexpectedly succeeded")
	}
	c.ChatGPTTokenSource = fixedChatGPTToken("access-token")
	if _, err := c.NewProvider(); err != nil {
		t.Fatalf("provider with token source: %v", err)
	}
}

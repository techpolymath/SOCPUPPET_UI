package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type fixedChatGPTToken string

func (f fixedChatGPTToken) AccessToken(context.Context) (string, error) { return string(f), nil }
func (fixedChatGPTToken) AccountID() string                             { return "" }
func (fixedChatGPTToken) InvalidateAccessToken(string)                  {}

type workspaceChatGPTToken struct {
	token     string
	accountID string
}

func (f workspaceChatGPTToken) AccessToken(context.Context) (string, error) { return f.token, nil }
func (f workspaceChatGPTToken) AccountID() string                           { return f.accountID }
func (workspaceChatGPTToken) InvalidateAccessToken(string)                  {}

type chatGPTTestRoundTripper func(*http.Request) (*http.Response, error)

func (f chatGPTTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestChatGPTPlanTransportRoutesToSelectedWorkspace(t *testing.T) {
	var gotBody []byte
	transport := &chatGPTPlanTransport{
		tokens: workspaceChatGPTToken{token: "access-token", accountID: "workspace-123"},
		base: chatGPTTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer access-token" {
				t.Fatalf("authorization = %q", got)
			}
			if got := req.Header.Get("ChatGPT-Account-Id"); got != "workspace-123" {
				t.Fatalf("ChatGPT-Account-Id = %q", got)
			}
			gotBody, _ = io.ReadAll(req.Body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
				Request:    req,
			}, nil
		}),
	}
	req, err := http.NewRequest(http.MethodPost, ChatGPTPlanBaseURL+"/responses", bytes.NewBufferString(`{"model":"gpt-daybreak-blue-latest","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var body struct {
		AccessPrograms struct {
			Cyber string `json:"cyber"`
		} `json:"access_programs"`
	}
	if err := json.Unmarshal(gotBody, &body); err != nil {
		t.Fatal(err)
	}
	if body.AccessPrograms.Cyber != "daybreak_blue" {
		t.Fatalf("access_programs.cyber = %q; body=%s", body.AccessPrograms.Cyber, gotBody)
	}
}

func TestChatGPTPlanTransportRejectsSilentDaybreakDowngrade(t *testing.T) {
	transport := &chatGPTPlanTransport{
		tokens: workspaceChatGPTToken{token: "access-token", accountID: "workspace-123"},
		base: chatGPTTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			body := "data: " + `{"type":"response.created","response":{"access_programs":{"cyber":"standard"}}}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Request:    req,
			}, nil
		}),
	}
	req, err := http.NewRequest(http.MethodPost, ChatGPTPlanBaseURL+"/responses", bytes.NewBufferString(`{"model":"gpt-6-sol","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "access_program_not_enabled") || !strings.Contains(string(body), "OpenAI selected standard") {
		t.Fatalf("status=%d body=%s, want local 403 silent-downgrade rejection", resp.StatusCode, body)
	}
}

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

func TestNormalizeChatGPTPlanBodySelectsDaybreakFromModelAlias(t *testing.T) {
	for _, tc := range []struct {
		model string
		want  string
	}{
		{model: "gpt-daybreak-blue-latest", want: "daybreak_blue"},
		{model: "gpt-daybreak-red-latest", want: "daybreak_red"},
		{model: "gpt-6-sol", want: "daybreak_blue"},
		{model: "gpt-6-luna", want: "daybreak_blue"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			raw, err := normalizeChatGPTPlanBody([]byte(`{"model":"` + tc.model + `","input":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				AccessPrograms struct {
					Cyber string `json:"cyber"`
				} `json:"access_programs"`
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.AccessPrograms.Cyber != tc.want {
				t.Fatalf("access_programs.cyber=%q, want %q; body=%s", got.AccessPrograms.Cyber, tc.want, raw)
			}
		})
	}
}

func TestNormalizeChatGPTPlanBodyLeavesOrdinaryModelProgramUnspecified(t *testing.T) {
	raw, err := normalizeChatGPTPlanBody([]byte(`{"model":"gpt-6.1-sol","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["access_programs"]; present {
		t.Fatalf("ordinary model unexpectedly received access_programs: %s", raw)
	}
}

func TestChatGPTConfigIsKeylessButRequiresTokenSource(t *testing.T) {
	c := ConfigFrom("chatgpt", "gpt-test", "https://evil.example/v1", "", "")
	if c.Format != FormatChatGPT || c.BaseURL != ChatGPTPlanBaseURL || c.NeedsAPIKey() {
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

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testChatGPTJWT(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID},
	})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestChatGPTAccountID(t *testing.T) {
	if got := chatGPTAccountID(testChatGPTJWT(t, "workspace-123")); got != "workspace-123" {
		t.Fatalf("account id = %q", got)
	}
	for _, raw := range []string{"", "not-a-jwt", testChatGPTJWT(t, " workspace-123 ")} {
		if got := chatGPTAccountID(raw); got != "" {
			t.Fatalf("chatGPTAccountID(%q) = %q, want empty", raw, got)
		}
	}
}

func TestChatGPTListModelsPreservesDisplayOrder(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "workspace-123" {
			t.Errorf("ChatGPT-Account-Id = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-visible","display_name":"GPT Visible","visibility":"list"},
			{"slug":"gpt-hidden","display_name":"GPT Hidden","visibility":"hidden"},
			{"slug":"gpt-visible","display_name":"Duplicate","visibility":"list"},
			{"slug":"gpt-fallback","visibility":"list"}
		]}`))
	}))
	t.Cleanup(api.Close)

	now := time.Now()
	m := &chatGPTAuthManager{
		loaded: true,
		cred: chatGPTCredential{
			ClientID: "client", RefreshToken: "refresh", AccessToken: "access-token",
			AccountID: "workspace-123", Scopes: []string{chatGPTScope}, ExpiresAt: now.Add(time.Hour),
		},
		httpClient: api.Client(), apiBase: api.URL, now: func() time.Time { return now },
	}
	models, err := m.listModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].Slug != "gpt-visible" || models[0].DisplayName != "GPT Visible" ||
		models[1].Slug != "gpt-fallback" || models[1].DisplayName != "gpt-fallback" {
		t.Fatalf("models = %#v", models)
	}
}

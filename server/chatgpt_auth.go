package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/techpolymath/SOCPUPPET_UI/db"
)

const (
	chatGPTCredentialsSetting = "chatgpt_oauth_credentials"
	chatGPTHostIDSetting      = "chatgpt_oauth_host_id"
	chatGPTScope              = "chatgpt.tokens.use.direct"
	chatGPTResource           = "https://api.openai.com/v1"
)

type chatGPTCredential struct {
	Email        string    `json:"email,omitempty"`
	Issuer       string    `json:"issuer,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ClientID     string    `json:"client_id,omitempty"`
	HostID       string    `json:"ext_agent_host_id,omitempty"`
	IDToken      string    `json:"id_token,omitempty"`
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Scopes       []string  `json:"scopes,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	SavedAt      time.Time `json:"saved_at,omitempty"`
}

type chatGPTPendingAuth struct {
	State        string
	Nonce        string
	CodeVerifier string
	RedirectURI  string
	ClientID     string
	CreatedAt    time.Time
}

type chatGPTTokenResponse struct {
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	IDToken         string `json:"id_token"`
	TokenType       string `json:"token_type"`
	ExpiresIn       int64  `json:"expires_in"`
	Scope           string `json:"scope"`
	EarliestRefresh int64  `json:"earliest_refresh_at"`
}

type chatGPTModelOption struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
}

type chatGPTAuthManager struct {
	pg *db.DB

	mu      sync.Mutex
	loaded  bool
	cred    chatGPTCredential
	pending map[string]chatGPTPendingAuth

	httpClient   *http.Client
	authorizeURL string
	tokenURL     string
	discoveryURL string
	apiBase      string
	now          func() time.Time
}

func newChatGPTAuthManager(pg *db.DB) *chatGPTAuthManager {
	return &chatGPTAuthManager{
		pg: pg, pending: map[string]chatGPTPendingAuth{},
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		authorizeURL: "https://auth.openai.com/api/accounts/authorize",
		tokenURL:     "https://auth.openai.com/api/accounts/oauth/token",
		discoveryURL: "https://auth.openai.com/.well-known/openid-configuration",
		apiBase:      chatGPTResource,
		now:          time.Now,
	}
}

func (m *chatGPTAuthManager) loadLocked() error {
	if m.loaded {
		return nil
	}
	m.loaded = true
	raw, ok, err := m.pg.GetSetting(chatGPTCredentialsSetting)
	if err != nil || !ok || strings.TrimSpace(raw) == "" {
		return err
	}
	if err := json.Unmarshal([]byte(raw), &m.cred); err != nil {
		return fmt.Errorf("decode ChatGPT credentials: %w", err)
	}
	return nil
}

func (m *chatGPTAuthManager) saveLocked() error {
	raw, err := json.Marshal(m.cred)
	if err != nil {
		return err
	}
	return m.pg.SetSetting(chatGPTCredentialsSetting, string(raw))
}

func hasScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func (m *chatGPTAuthManager) connected() (chatGPTCredential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return chatGPTCredential{}, false, err
	}
	c := m.cred
	return c, c.ClientID != "" && c.RefreshToken != "" && hasScope(c.Scopes, chatGPTScope), nil
}

// AccessToken implements agent.ChatGPTTokenSource. Refresh is serialized because
// OpenAI rotates refresh tokens and concurrent use of the same token can revoke it.
func (m *chatGPTAuthManager) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return "", err
	}
	if m.cred.ClientID == "" || m.cred.RefreshToken == "" || !hasScope(m.cred.Scopes, chatGPTScope) {
		return "", errors.New("ChatGPT plan is not connected; open System → LLM and choose Continue with ChatGPT")
	}
	if m.cred.AccessToken != "" && m.cred.ExpiresAt.After(m.now().Add(2*time.Minute)) {
		return m.cred.AccessToken, nil
	}
	if err := m.refreshLocked(ctx); err != nil {
		return "", err
	}
	return m.cred.AccessToken, nil
}

func (m *chatGPTAuthManager) InvalidateAccessToken(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadLocked() == nil && token != "" && token == m.cred.AccessToken {
		m.cred.ExpiresAt = time.Time{}
	}
}

// AccountID implements agent.ChatGPTTokenSource. Older saved credentials did
// not persist the selected ChatGPT workspace, so migrate it lazily from the
// already verified ID token. The value is routing metadata, not a credential.
func (m *chatGPTAuthManager) AccountID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadLocked() != nil {
		return ""
	}
	if m.cred.AccountID != "" {
		return m.cred.AccountID
	}
	accountID := chatGPTAccountID(m.cred.IDToken)
	if accountID == "" {
		accountID = chatGPTAccountID(m.cred.AccessToken)
	}
	if accountID != "" {
		m.cred.AccountID = accountID
		_ = m.saveLocked()
	}
	return accountID
}

// chatGPTAccountID reads the workspace-routing claim from a JWT payload. Token
// signatures are verified separately during authorization; this helper is also
// used after refresh, where the token endpoint is reached over TLS and the
// extracted value only selects the workspace for that same bearer token.
func chatGPTAccountID(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	accountID := strings.TrimSpace(claims.Auth.AccountID)
	if accountID == "" || accountID != claims.Auth.AccountID {
		return ""
	}
	return accountID
}

func (m *chatGPTAuthManager) refreshLocked(ctx context.Context) error {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {m.cred.ClientID},
		"refresh_token": {m.cred.RefreshToken},
		"resource":      {chatGPTResource},
	}
	tok, err := m.tokenRequest(ctx, form)
	if err != nil {
		return fmt.Errorf("refresh ChatGPT session: %w", err)
	}
	if tok.AccessToken == "" {
		return errors.New("refresh ChatGPT session: token endpoint returned no access token")
	}
	m.cred.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		m.cred.RefreshToken = tok.RefreshToken
	}
	if tok.IDToken != "" {
		m.cred.IDToken = tok.IDToken
	}
	if accountID := chatGPTAccountID(m.cred.IDToken); accountID != "" {
		m.cred.AccountID = accountID
	} else if accountID := chatGPTAccountID(tok.AccessToken); accountID != "" {
		m.cred.AccountID = accountID
	}
	if tok.TokenType != "" {
		m.cred.TokenType = tok.TokenType
	}
	if tok.Scope != "" {
		m.cred.Scopes = strings.Fields(tok.Scope)
	}
	m.cred.SavedAt = m.now().UTC()
	m.cred.ExpiresAt = m.cred.SavedAt.Add(time.Duration(max64(tok.ExpiresIn, 3600)) * time.Second)
	return m.saveLocked()
}

func max64(a, fallback int64) int64 {
	if a > 0 {
		return a
	}
	return fallback
}

func (m *chatGPTAuthManager) tokenRequest(ctx context.Context, form url.Values) (chatGPTTokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return chatGPTTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return chatGPTTokenResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return chatGPTTokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var out struct {
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		_ = json.Unmarshal(raw, &out)
		msg := strings.TrimSpace(out.Error + ": " + out.ErrorDescription)
		if msg == ":" || msg == "" {
			msg = http.StatusText(resp.StatusCode)
		}
		return chatGPTTokenResponse{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, msg)
	}
	var out chatGPTTokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("decode token response: %w", err)
	}
	return out, nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (m *chatGPTAuthManager) hostIDLocked() (string, error) {
	if m.cred.HostID != "" {
		return m.cred.HostID, nil
	}
	if raw, ok, err := m.pg.GetSetting(chatGPTHostIDSetting); err != nil {
		return "", err
	} else if ok && strings.HasPrefix(raw, "urn:uuid:") {
		m.cred.HostID = raw
		return raw, nil
	}
	hostID := "urn:uuid:" + uuid.NewString()
	if err := m.pg.SetSetting(chatGPTHostIDSetting, hostID); err != nil {
		return "", err
	}
	m.cred.HostID = hostID
	return hostID, nil
}

func loopbackRedirectURI(r *http.Request) (string, error) {
	u, err := url.Parse("http://" + r.Host)
	if err != nil {
		return "", err
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	if _, err := strconv.Atoi(port); err != nil {
		return "", errors.New("cannot determine the console's loopback port")
	}
	return "http://127.0.0.1:" + port + "/auth/callback", nil
}

func (m *chatGPTAuthManager) authorizationURL(r *http.Request) (string, error) {
	redirectURI, err := loopbackRedirectURI(r)
	if err != nil {
		return "", err
	}
	state, err := randomURLSafe(32)
	if err != nil {
		return "", err
	}
	nonce, err := randomURLSafe(32)
	if err != nil {
		return "", err
	}
	verifier, err := randomURLSafe(64)
	if err != nil {
		return "", err
	}
	challengeBytes := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(challengeBytes[:])

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return "", err
	}
	hostID, err := m.hostIDLocked()
	if err != nil {
		return "", err
	}
	clientID := m.cred.ClientID
	if clientID == "" {
		clientID = "dynamic_agent_client"
	}
	for key, attempt := range m.pending {
		if m.now().Sub(attempt.CreatedAt) > 10*time.Minute {
			delete(m.pending, key)
		}
	}
	m.pending[state] = chatGPTPendingAuth{State: state, Nonce: nonce, CodeVerifier: verifier, RedirectURI: redirectURI, ClientID: clientID, CreatedAt: m.now()}

	q := url.Values{
		"client_id":             {clientID},
		"ext_agent_host_id":     {hostID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"openid profile email offline_access resource.invoke " + chatGPTScope},
		"resource":              {chatGPTResource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challenge},
	}
	if clientID == "dynamic_agent_client" {
		q.Set("agent_name_hint", "SOCPuppet")
	} else {
		if m.cred.IDToken != "" {
			q.Set("id_token_hint", m.cred.IDToken)
		}
		if m.cred.Email != "" {
			q.Set("login_hint", m.cred.Email)
		}
	}
	return m.authorizeURL + "?" + q.Encode(), nil
}

type openIDConfig struct {
	Issuer             string `json:"issuer"`
	JWKSURI            string `json:"jwks_uri"`
	RevocationEndpoint string `json:"revocation_endpoint"`
}

func (m *chatGPTAuthManager) discovery(ctx context.Context) (openIDConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.discoveryURL, nil)
	if err != nil {
		return openIDConfig{}, err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return openIDConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return openIDConfig{}, fmt.Errorf("OpenID discovery HTTP %d", resp.StatusCode)
	}
	var cfg openIDConfig
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&cfg); err != nil {
		return cfg, err
	}
	if cfg.Issuer == "" || cfg.JWKSURI == "" {
		return cfg, errors.New("OpenID discovery returned incomplete metadata")
	}
	return cfg, nil
}

func (m *chatGPTAuthManager) verifyIDToken(ctx context.Context, raw, clientID, nonce string) (issuer, subject, email string, err error) {
	cfg, err := m.discovery(ctx)
	if err != nil {
		return "", "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.JWKSURI, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("JWKS HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			KID string `json:"kid"`
			KTY string `json:"kty"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&set); err != nil {
		return "", "", "", err
	}
	claims := jwt.MapClaims{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		kid, _ := token.Header["kid"].(string)
		for _, key := range set.Keys {
			if key.KID != kid || key.KTY != "RSA" {
				continue
			}
			nBytes, nerr := base64.RawURLEncoding.DecodeString(key.N)
			eBytes, eerr := base64.RawURLEncoding.DecodeString(key.E)
			if nerr != nil || eerr != nil || len(eBytes) == 0 || len(eBytes) > 4 {
				return nil, errors.New("invalid RSA key in OpenAI JWKS")
			}
			e := 0
			for _, b := range eBytes {
				e = e<<8 | int(b)
			}
			return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
		}
		return nil, errors.New("ID token signing key not found")
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(cfg.Issuer), jwt.WithAudience(clientID), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return "", "", "", fmt.Errorf("verify OpenAI ID token: %w", err)
	}
	if got, _ := claims["nonce"].(string); got == "" || got != nonce {
		return "", "", "", errors.New("verify OpenAI ID token: nonce mismatch")
	}
	issuer, _ = claims["iss"].(string)
	subject, _ = claims["sub"].(string)
	email, _ = claims["email"].(string)
	if subject == "" {
		return "", "", "", errors.New("verify OpenAI ID token: missing subject")
	}
	return issuer, subject, email, nil
}

func (m *chatGPTAuthManager) finishAuthorization(ctx context.Context, query url.Values) (chatGPTCredential, error) {
	state := query.Get("state")
	m.mu.Lock()
	attempt, ok := m.pending[state]
	if ok {
		delete(m.pending, state)
	}
	m.mu.Unlock()
	if !ok || state == "" || m.now().Sub(attempt.CreatedAt) > 10*time.Minute {
		return chatGPTCredential{}, errors.New("the ChatGPT sign-in attempt expired or its state was invalid")
	}
	if oauthErr := query.Get("error"); oauthErr != "" {
		return chatGPTCredential{}, fmt.Errorf("ChatGPT sign-in was not completed: %s", oauthErr)
	}
	code := query.Get("code")
	if code == "" {
		return chatGPTCredential{}, errors.New("ChatGPT callback did not include an authorization code")
	}
	clientID := attempt.ClientID
	callbackClientID := query.Get("client_id")
	if clientID == "dynamic_agent_client" {
		if callbackClientID == "" || callbackClientID == clientID {
			return chatGPTCredential{}, errors.New("ChatGPT registration did not return an issued client ID")
		}
		clientID = callbackClientID
	} else if callbackClientID != "" && callbackClientID != clientID {
		return chatGPTCredential{}, errors.New("ChatGPT callback returned a different client ID")
	}
	tok, err := m.tokenRequest(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"code_verifier": {attempt.CodeVerifier},
		"redirect_uri":  {attempt.RedirectURI},
		"resource":      {chatGPTResource},
	})
	if err != nil {
		return chatGPTCredential{}, err
	}
	if tok.IDToken == "" || tok.AccessToken == "" || tok.RefreshToken == "" {
		return chatGPTCredential{}, errors.New("ChatGPT token response was incomplete")
	}
	issuer, subject, email, err := m.verifyIDToken(ctx, tok.IDToken, clientID, attempt.Nonce)
	if err != nil {
		return chatGPTCredential{}, err
	}
	scopes := strings.Fields(tok.Scope)
	if !hasScope(scopes, chatGPTScope) {
		return chatGPTCredential{}, errors.New("ChatGPT sign-in succeeded, but permission to use the ChatGPT plan was not granted")
	}
	now := m.now().UTC()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return chatGPTCredential{}, err
	}
	if m.cred.Subject != "" && attempt.ClientID != "dynamic_agent_client" && m.cred.Subject != subject {
		return chatGPTCredential{}, errors.New("ChatGPT sign-in returned a different account for the saved registration")
	}
	hostID, err := m.hostIDLocked()
	if err != nil {
		return chatGPTCredential{}, err
	}
	m.cred = chatGPTCredential{
		Email: email, Issuer: issuer, Subject: subject, AccountID: chatGPTAccountID(tok.IDToken), ClientID: clientID, HostID: hostID,
		IDToken: tok.IDToken, AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		TokenType: tok.TokenType, Scopes: scopes, SavedAt: now,
		ExpiresAt: now.Add(time.Duration(max64(tok.ExpiresIn, 3600)) * time.Second),
	}
	if err := m.saveLocked(); err != nil {
		return chatGPTCredential{}, err
	}
	return m.cred, nil
}

func (m *chatGPTAuthManager) listModels(ctx context.Context) ([]chatGPTModelOption, error) {
	token, err := m.AccessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.apiBase+"/models", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if accountID := m.AccountID(); accountID != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list ChatGPT models: HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	var out []chatGPTModelOption
	seen := map[string]bool{}
	for _, model := range payload.Models {
		slug := strings.TrimSpace(model.Slug)
		if slug != "" && !seen[slug] && (model.Visibility == "" || model.Visibility == "list") {
			display := strings.TrimSpace(model.DisplayName)
			if display == "" {
				display = slug
			}
			out = append(out, chatGPTModelOption{Slug: slug, DisplayName: display})
			seen[slug] = true
		}
	}
	for _, model := range payload.Data {
		slug := strings.TrimSpace(model.ID)
		if slug != "" && !seen[slug] {
			out = append(out, chatGPTModelOption{Slug: slug, DisplayName: slug})
			seen[slug] = true
		}
	}
	return out, nil
}

func (m *chatGPTAuthManager) disconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.loadLocked(); err != nil {
		return err
	}
	if m.cred.RefreshToken == "" {
		return nil
	}
	discovery, err := m.discovery(ctx)
	if err == nil && discovery.RevocationEndpoint != "" {
		form := url.Values{"token": {m.cred.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {m.cred.ClientID}}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, discovery.RevocationEndpoint, strings.NewReader(form.Encode()))
		if reqErr == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if resp, doErr := m.httpClient.Do(req); doErr == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
				resp.Body.Close()
			}
		}
	}
	// Preserve the issued client/account mapping and stable host ID for a later
	// sign-in, but remove every credential that can authorize a request.
	m.cred.AccessToken = ""
	m.cred.RefreshToken = ""
	m.cred.IDToken = ""
	m.cred.TokenType = ""
	m.cred.Scopes = nil
	m.cred.ExpiresAt = time.Time{}
	m.cred.SavedAt = m.now().UTC()
	return m.saveLocked()
}

func (s *Server) chatGPTStatus(w http.ResponseWriter, r *http.Request) {
	cred, connected, err := s.chatGPTAuth.connected()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"connected": connected, "sharing": connected, "email": cred.Email,
		"expires_at": cred.ExpiresAt, "manage_url": "https://chatgpt.com/#settings/Usage",
	})
}

func (s *Server) chatGPTConnect(w http.ResponseWriter, r *http.Request) {
	target, err := s.chatGPTAuth.authorizationURL(r)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) chatGPTCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if _, err := s.chatGPTAuth.finishAuthorization(r.Context(), r.URL.Query()); err != nil {
		http.Redirect(w, r, "/system/llm?chatgpt=error&message="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	http.Redirect(w, r, "/system/llm?chatgpt=connected", http.StatusSeeOther)
}

func (s *Server) chatGPTModels(w http.ResponseWriter, r *http.Request) {
	models, err := s.chatGPTAuth.listModels(r.Context())
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	slugs := make([]string, 0, len(models))
	for _, model := range models {
		slugs = append(slugs, model.Slug)
	}
	writeJSON(w, 200, map[string]any{"models": slugs, "model_options": models})
}

func (s *Server) chatGPTDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.chatGPTAuth.disconnect(r.Context()); err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	s.invalidateProfileAgents()
	s.reapplyActiveProfile()
	writeJSON(w, 200, map[string]any{"ok": true})
}

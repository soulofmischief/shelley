package chatgptauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	DefaultIssuer      = "https://auth.openai.com"
	DefaultClientID    = "app_EMoamEEZ73f0CkXaXp7hrann"
	DefaultRedirectURI = "http://localhost:1455/auth/callback"
	DefaultCodexURL    = "https://chatgpt.com/backend-api/codex"
	defaultScopes      = "openid profile email offline_access api.connectors.read api.connectors.invoke"
	pendingFlowTTL     = 10 * time.Minute
	refreshWindow      = 5 * time.Minute
)

var ErrNotAuthenticated = errors.New("ChatGPT is not authenticated")

type Credentials struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	AccountID    string
	ExpiresAt    time.Time
}

type Store interface {
	LoadChatGPTCredentials(context.Context) (Credentials, error)
	SaveChatGPTCredentials(context.Context, Credentials) error
	DeleteChatGPTCredentials(context.Context) error
}

type Config struct {
	Issuer      string
	ClientID    string
	RedirectURI string
	HTTPClient  *http.Client
	Now         func() time.Time
}

type Manager struct {
	store       Store
	issuer      string
	clientID    string
	redirectURI string
	httpc       *http.Client
	now         func() time.Time

	mu      sync.Mutex
	pending map[string]pendingFlow
}

type pendingFlow struct {
	verifier  string
	expiresAt time.Time
}

type Flow struct {
	AuthorizationURL string `json:"authorization_url"`
	State            string `json:"state"`
	RedirectURI      string `json:"redirect_uri"`
	ExpiresAt        string `json:"expires_at"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

func NewManager(store Store, cfg Config) *Manager {
	httpc := cfg.HTTPClient
	if httpc == nil {
		httpc = &http.Client{Timeout: 30 * time.Second}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		store:       store,
		issuer:      firstNonEmpty(cfg.Issuer, DefaultIssuer),
		clientID:    firstNonEmpty(cfg.ClientID, DefaultClientID),
		redirectURI: firstNonEmpty(cfg.RedirectURI, DefaultRedirectURI),
		httpc:       httpc,
		now:         now,
		pending:     make(map[string]pendingFlow),
	}
}

func (m *Manager) StartFlow() (Flow, error) {
	verifier, err := randomURLToken(32)
	if err != nil {
		return Flow{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	state, err := randomURLToken(32)
	if err != nil {
		return Flow{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	expiresAt := m.now().Add(pendingFlowTTL)

	m.mu.Lock()
	m.removeExpiredFlowsLocked()
	m.pending[state] = pendingFlow{verifier: verifier, expiresAt: expiresAt}
	m.mu.Unlock()

	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"response_type":              {"code"},
		"client_id":                  {m.clientID},
		"redirect_uri":               {m.redirectURI},
		"scope":                      {defaultScopes},
		"code_challenge":             {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method":      {"S256"},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"state":                      {state},
		"originator":                 {"shelley"},
	}
	return Flow{
		AuthorizationURL: strings.TrimSuffix(m.issuer, "/") + "/oauth/authorize?" + query.Encode(),
		State:            state,
		RedirectURI:      m.redirectURI,
		ExpiresAt:        expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

func (m *Manager) CompleteFlow(ctx context.Context, callback string) (Credentials, error) {
	values, err := callbackValues(callback)
	if err != nil {
		return Credentials{}, err
	}
	state := values.Get("state")
	if state == "" {
		return Credentials{}, fmt.Errorf("OAuth callback is missing state")
	}

	m.mu.Lock()
	flow, found := m.pending[state]
	if found {
		delete(m.pending, state)
	}
	m.mu.Unlock()
	if !found || m.now().After(flow.expiresAt) {
		return Credentials{}, fmt.Errorf("OAuth state is unknown or expired")
	}
	if oauthErr := values.Get("error"); oauthErr != "" {
		detail := values.Get("error_description")
		if detail != "" {
			return Credentials{}, fmt.Errorf("OAuth authorization failed: %s: %s", oauthErr, detail)
		}
		return Credentials{}, fmt.Errorf("OAuth authorization failed: %s", oauthErr)
	}
	code := values.Get("code")
	if code == "" {
		return Credentials{}, fmt.Errorf("OAuth callback is missing authorization code")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {m.clientID},
		"code":          {code},
		"redirect_uri":  {m.redirectURI},
		"code_verifier": {flow.verifier},
	}
	tokens, err := m.requestTokens(ctx, form, false)
	if err != nil {
		return Credentials{}, fmt.Errorf("exchange OAuth authorization code: %w", err)
	}
	credentials, err := m.credentialsFromTokens(tokens, Credentials{})
	if err != nil {
		return Credentials{}, err
	}
	if err := m.store.SaveChatGPTCredentials(ctx, credentials); err != nil {
		return Credentials{}, fmt.Errorf("save ChatGPT credentials: %w", err)
	}
	return credentials, nil
}

func (m *Manager) Credentials(ctx context.Context) (Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.credentialsLocked(ctx, false, "")
}

func (m *Manager) RefreshIfCurrent(ctx context.Context, accessToken string) (Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.credentialsLocked(ctx, true, accessToken)
}

func (m *Manager) credentialsLocked(ctx context.Context, force bool, usedAccessToken string) (Credentials, error) {
	credentials, err := m.store.LoadChatGPTCredentials(ctx)
	if err != nil {
		return Credentials{}, err
	}
	if credentials.AccessToken == "" {
		return Credentials{}, ErrNotAuthenticated
	}
	if force && usedAccessToken != "" && credentials.AccessToken != usedAccessToken {
		return credentials, nil
	}
	if !force && credentials.ExpiresAt.After(m.now().Add(refreshWindow)) {
		return credentials, nil
	}
	if credentials.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("%w: refresh token is missing", ErrNotAuthenticated)
	}

	tokens, err := m.requestTokens(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {m.clientID},
		"refresh_token": {credentials.RefreshToken},
	}, true)
	if err != nil {
		return Credentials{}, fmt.Errorf("refresh ChatGPT OAuth token: %w", err)
	}
	refreshed, err := m.credentialsFromTokens(tokens, credentials)
	if err != nil {
		return Credentials{}, err
	}
	if err := m.store.SaveChatGPTCredentials(ctx, refreshed); err != nil {
		return Credentials{}, fmt.Errorf("save refreshed ChatGPT credentials: %w", err)
	}
	return refreshed, nil
}

func (m *Manager) Logout(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.store.DeleteChatGPTCredentials(ctx); err != nil {
		return fmt.Errorf("delete ChatGPT credentials: %w", err)
	}
	return nil
}

func (m *Manager) HTTPClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	clone.Transport = &oauthTransport{base: transport, manager: m}
	return &clone
}

func (m *Manager) requestTokens(ctx context.Context, values url.Values, jsonBody bool) (tokenResponse, error) {
	var body io.Reader
	req, err := func() (*http.Request, error) {
		if jsonBody {
			encoded, marshalErr := json.Marshal(valuesToObject(values))
			if marshalErr != nil {
				return nil, marshalErr
			}
			body = strings.NewReader(string(encoded))
		} else {
			body = strings.NewReader(values.Encode())
		}
		return http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(m.issuer, "/")+"/oauth/token", body)
	}()
	if err != nil {
		return tokenResponse{}, err
	}
	if jsonBody {
		req.Header.Set("Content-Type", "application/json")
	} else {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("User-Agent", "shelley")
	resp, err := m.httpc.Do(req)
	if err != nil {
		return tokenResponse{}, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tokenResponse{}, fmt.Errorf("token endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(responseBody)))
	}
	var tokens tokenResponse
	if err := json.Unmarshal(responseBody, &tokens); err != nil {
		return tokenResponse{}, fmt.Errorf("decode token response: %w", err)
	}
	if tokens.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("token endpoint omitted access_token")
	}
	return tokens, nil
}

func (m *Manager) credentialsFromTokens(tokens tokenResponse, previous Credentials) (Credentials, error) {
	credentials := previous
	credentials.AccessToken = tokens.AccessToken
	if tokens.RefreshToken != "" {
		credentials.RefreshToken = tokens.RefreshToken
	}
	if tokens.IDToken != "" {
		credentials.IDToken = tokens.IDToken
	}
	credentials.ExpiresAt = m.now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	if tokens.ExpiresIn <= 0 {
		if expiresAt, ok := tokenExpiry(tokens.AccessToken); ok {
			credentials.ExpiresAt = expiresAt
		} else {
			credentials.ExpiresAt = m.now().Add(time.Hour)
		}
	}
	credentials.AccountID = accountIDFromTokens(credentials.IDToken, credentials.AccessToken)
	if credentials.AccountID == "" {
		return Credentials{}, fmt.Errorf("OAuth tokens omitted the ChatGPT account ID")
	}
	return credentials, nil
}

func (m *Manager) removeExpiredFlowsLocked() {
	now := m.now()
	for state, flow := range m.pending {
		if now.After(flow.expiresAt) {
			delete(m.pending, state)
		}
	}
}

type oauthTransport struct {
	base    http.RoundTripper
	manager *Manager
}

func (t *oauthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	credentials, err := t.manager.Credentials(req.Context())
	if err != nil {
		return nil, err
	}
	first := cloneRequest(req)
	setAuthHeaders(first, credentials)
	response, err := t.base.RoundTrip(first)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	response.Body.Close()

	credentials, err = t.manager.RefreshIfCurrent(req.Context(), credentials.AccessToken)
	if err != nil {
		return nil, err
	}
	retry := cloneRequest(req)
	setAuthHeaders(retry, credentials)
	return t.base.RoundTrip(retry)
}

func cloneRequest(req *http.Request) *http.Request {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err == nil {
			clone.Body = body
		}
	}
	return clone
}

func setAuthHeaders(req *http.Request, credentials Credentials) {
	req.Header.Set("Authorization", "Bearer "+credentials.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", credentials.AccountID)
	req.Header.Set("originator", "shelley")
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "shelley")
	}
}

func callbackValues(callback string) (url.Values, error) {
	callback = strings.TrimSpace(callback)
	if callback == "" {
		return nil, fmt.Errorf("OAuth callback is empty")
	}
	if parsed, err := url.Parse(callback); err == nil && parsed.RawQuery != "" {
		return parsed.Query(), nil
	}
	callback = strings.TrimPrefix(callback, "?")
	values, err := url.ParseQuery(callback)
	if err != nil {
		return nil, fmt.Errorf("parse OAuth callback: %w", err)
	}
	return values, nil
}

func randomURLToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func accountIDFromTokens(tokens ...string) string {
	for _, token := range tokens {
		claims, ok := jwtClaims(token)
		if !ok {
			continue
		}
		for _, key := range []string{"https://api.openai.com/auth/account_id", "chatgpt_account_id"} {
			if value, ok := claims[key].(string); ok && value != "" {
				return value
			}
		}
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			if value, ok := auth["chatgpt_account_id"].(string); ok && value != "" {
				return value
			}
		}
	}
	return ""
}

func tokenExpiry(token string) (time.Time, bool) {
	claims, ok := jwtClaims(token)
	if !ok {
		return time.Time{}, false
	}
	expires, ok := claims["exp"].(float64)
	if !ok || expires <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(expires), 0), true
}

func jwtClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

func valuesToObject(values url.Values) map[string]string {
	object := make(map[string]string, len(values))
	for key := range values {
		object[key] = values.Get(key)
	}
	return object
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

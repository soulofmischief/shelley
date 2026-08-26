package chatgptauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu          sync.Mutex
	credentials Credentials
	loaded      bool
}

func (s *memoryStore) LoadChatGPTCredentials(context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return Credentials{}, ErrNotAuthenticated
	}
	return s.credentials, nil
}

func (s *memoryStore) SaveChatGPTCredentials(_ context.Context, credentials Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials = credentials
	s.loaded = true
	return nil
}

func (s *memoryStore) DeleteChatGPTCredentials(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials = Credentials{}
	s.loaded = false
	return nil
}

func TestStartFlowUsesCodexPKCEContract(t *testing.T) {
	now := time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC)
	manager := NewManager(&memoryStore{}, Config{Now: func() time.Time { return now }})

	flow, err := manager.StartFlow()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(flow.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != DefaultIssuer+"/oauth/authorize" {
		t.Fatalf("authorization URL = %q", flow.AuthorizationURL)
	}
	for key, want := range map[string]string{
		"response_type":              "code",
		"client_id":                  DefaultClientID,
		"redirect_uri":               DefaultRedirectURI,
		"scope":                      defaultScopes,
		"code_challenge_method":      "S256",
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 "shelley",
	} {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Get("state") != flow.State || query.Get("code_challenge") == "" {
		t.Fatalf("flow state/challenge missing: %+v", flow)
	}
	if flow.ExpiresAt != now.Add(pendingFlowTTL).Format(time.RFC3339) {
		t.Fatalf("expires_at = %q", flow.ExpiresAt)
	}
}

func TestCompleteFlowPersistsCredentialsFromPastedCallback(t *testing.T) {
	now := time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC)
	accountID := "account-123"
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code_verifier") == "" {
			t.Fatalf("token form = %v", r.Form)
		}
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  jwt(t, map[string]any{"exp": now.Add(time.Hour).Unix()}),
			RefreshToken: "refresh-1",
			IDToken: jwt(t, map[string]any{
				"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": accountID},
			}),
			ExpiresIn: 3600,
		})
	}))
	defer issuer.Close()

	store := &memoryStore{}
	manager := NewManager(store, Config{Issuer: issuer.URL, HTTPClient: issuer.Client(), Now: func() time.Time { return now }})
	flow, err := manager.StartFlow()
	if err != nil {
		t.Fatal(err)
	}
	callback := DefaultRedirectURI + "?code=authorization-code&state=" + url.QueryEscape(flow.State)
	credentials, err := manager.CompleteFlow(context.Background(), callback)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccountID != accountID || credentials.RefreshToken != "refresh-1" {
		t.Fatalf("credentials = %+v", credentials)
	}
	if !credentials.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("expires_at = %s", credentials.ExpiresAt)
	}
}

func TestAuthenticatedTransportRefreshesOnceOnUnauthorized(t *testing.T) {
	now := time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC)
	store := &memoryStore{loaded: true, credentials: Credentials{
		AccessToken: "old-access", RefreshToken: "refresh-1", IDToken: jwt(t, map[string]any{"chatgpt_account_id": "account-123"}),
		AccountID: "account-123", ExpiresAt: now.Add(time.Hour),
	}}
	var mu sync.Mutex
	refreshes := 0
	requests := 0
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			mu.Lock()
			refreshes++
			mu.Unlock()
			if r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("refresh content type = %q", r.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"refresh_token":"refresh-1"`) {
				t.Fatalf("refresh body = %s", body)
			}
			json.NewEncoder(w).Encode(tokenResponse{AccessToken: "new-access", ExpiresIn: 3600})
			return
		}

		mu.Lock()
		requests++
		requestNumber := requests
		mu.Unlock()
		if r.Header.Get("ChatGPT-Account-ID") != "account-123" || r.Header.Get("originator") != "shelley" {
			t.Fatalf("auth headers = %v", r.Header)
		}
		if requestNumber == 1 {
			if r.Header.Get("Authorization") != "Bearer old-access" {
				t.Fatalf("first authorization = %q", r.Header.Get("Authorization"))
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-access" {
			t.Fatalf("retry authorization = %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer issuer.Close()

	manager := NewManager(store, Config{Issuer: issuer.URL, HTTPClient: issuer.Client(), Now: func() time.Time { return now }})
	response, err := manager.HTTPClient(issuer.Client()).Get(issuer.URL + "/models")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent || refreshes != 1 || requests != 2 {
		t.Fatalf("status=%d refreshes=%d requests=%d", response.StatusCode, refreshes, requests)
	}
	stored, err := store.LoadChatGPTCredentials(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.RefreshToken != "refresh-1" {
		t.Fatalf("rotated response erased refresh token: %+v", stored)
	}
}

func TestCredentialsCoalesceConcurrentRefreshes(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	store := &memoryStore{loaded: true, credentials: Credentials{
		AccessToken: "expired", RefreshToken: "refresh-1",
		IDToken:   jwt(t, map[string]any{"chatgpt_account_id": "account-123"}),
		AccountID: "account-123", ExpiresAt: now,
	}}
	var mu sync.Mutex
	refreshes := 0
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		refreshes++
		mu.Unlock()
		json.NewEncoder(w).Encode(tokenResponse{AccessToken: "fresh", ExpiresIn: 3600})
	}))
	defer issuer.Close()

	manager := NewManager(store, Config{Issuer: issuer.URL, HTTPClient: issuer.Client(), Now: func() time.Time { return now }})
	start := make(chan struct{})
	errors := make(chan error, 8)
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			credentials, err := manager.Credentials(context.Background())
			if err == nil && credentials.AccessToken != "fresh" {
				err = fmt.Errorf("access token = %q", credentials.AccessToken)
			}
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refreshes = %d, want 1", refreshes)
	}
}

func TestConcurrentPKCEFlowsRemainIndependent(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			return
		}
		code := r.Form.Get("code")
		json.NewEncoder(w).Encode(tokenResponse{
			AccessToken:  code + "-access",
			RefreshToken: code + "-refresh",
			IDToken:      jwt(t, map[string]any{"chatgpt_account_id": "account-123"}),
			ExpiresIn:    3600,
		})
	}))
	defer issuer.Close()

	manager := NewManager(&memoryStore{}, Config{Issuer: issuer.URL, HTTPClient: issuer.Client(), Now: func() time.Time { return now }})
	first, err := manager.StartFlow()
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.StartFlow()
	if err != nil {
		t.Fatal(err)
	}
	if first.State == second.State {
		t.Fatal("concurrent flows received the same state")
	}
	for _, flow := range []struct {
		value Flow
		code  string
	}{{second, "second"}, {first, "first"}} {
		callback := DefaultRedirectURI + "?code=" + flow.code + "&state=" + url.QueryEscape(flow.value.State)
		credentials, err := manager.CompleteFlow(context.Background(), callback)
		if err != nil {
			t.Fatal(err)
		}
		if credentials.AccessToken != flow.code+"-access" {
			t.Fatalf("access token = %q", credentials.AccessToken)
		}
	}
	callback := DefaultRedirectURI + "?code=replay&state=" + url.QueryEscape(first.State)
	if _, err := manager.CompleteFlow(context.Background(), callback); err == nil {
		t.Fatal("completed OAuth state was accepted twice")
	}
}

func TestAuthenticatedTransportDoesNotRetryNonReplayableBody(t *testing.T) {
	now := time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC)
	store := &memoryStore{loaded: true, credentials: Credentials{
		AccessToken: "access", RefreshToken: "refresh", AccountID: "account-123", ExpiresAt: now.Add(time.Hour),
	}}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	}))
	defer server.Close()

	manager := NewManager(store, Config{HTTPClient: server.Client(), Now: func() time.Time { return now }})
	req, err := http.NewRequest(http.MethodPost, server.URL, io.NopCloser(strings.NewReader("payload")))
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.HTTPClient(server.Client()).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized || requests != 1 || string(body) != "unauthorized" {
		t.Fatalf("status=%d requests=%d body=%q", response.StatusCode, requests, body)
	}
}

func TestAccountIDFromTokensSupportsKnownClaimShapes(t *testing.T) {
	for name, claims := range map[string]map[string]any{
		"namespaced string": {"https://api.openai.com/auth/account_id": "account-a"},
		"top level":         {"chatgpt_account_id": "account-a"},
		"auth object":       {"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-a"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := accountIDFromTokens(jwt(t, claims)); got != "account-a" {
				t.Fatalf("account ID = %q", got)
			}
		})
	}
}

func TestTokenFileHTTPClientReadsRotatedTokenOnEveryRequest(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var authorizations []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := TokenFileHTTPClient(server.Client(), tokenFile)
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if err := os.WriteFile(tokenFile, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	response, err = client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(authorizations) != 2 || authorizations[0] != "Bearer first" || authorizations[1] != "Bearer second" {
		t.Fatalf("authorizations = %v", authorizations)
	}
}

func jwt(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

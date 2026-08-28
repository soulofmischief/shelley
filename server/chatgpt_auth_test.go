package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"shelley.exe.dev/chatgptauth"
)

type chatGPTAuthTestStore struct {
	mu          sync.Mutex
	credentials chatgptauth.Credentials
	loaded      bool
	onLoad      func()
}

func (s *chatGPTAuthTestStore) LoadChatGPTCredentials(context.Context) (chatgptauth.Credentials, error) {
	if s.onLoad != nil {
		s.onLoad()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return chatgptauth.Credentials{}, chatgptauth.ErrNotAuthenticated
	}
	return s.credentials, nil
}

func (s *chatGPTAuthTestStore) SaveChatGPTCredentials(_ context.Context, credentials chatgptauth.Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials = credentials
	s.loaded = true
	return nil
}

func (s *chatGPTAuthTestStore) DeleteChatGPTCredentials(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.credentials = chatgptauth.Credentials{}
	s.loaded = false
	return nil
}

func TestChatGPTAuthPillarModeHidesStandaloneControls(t *testing.T) {
	t.Parallel()

	controller := newChatGPTAuthController(ChatGPTAuthConfig{
		Mode: ChatGPTAuthModePillar,
		PlatformStatus: func(context.Context) (chatgptauth.PlatformStatus, error) {
			return chatgptauth.PlatformStatus{
				Authenticated: true,
				AccountID:     "account-123",
				ExpiresAt:     "2026-08-25T12:00:00Z",
			}, nil
		},
	}, func(context.Context) error { return nil }, nil)
	mux := http.NewServeMux()
	controller.registerRoutes(mux)

	statusRecorder := httptest.NewRecorder()
	mux.ServeHTTP(statusRecorder, httptest.NewRequest(http.MethodGet, "/api/chatgpt-auth", nil))
	if statusRecorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", statusRecorder.Code, http.StatusOK)
	}
	var status chatGPTAuthStatus
	if err := json.NewDecoder(statusRecorder.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Mode != ChatGPTAuthModePillar || !status.Configured || !status.Authenticated || status.AccountID != "account-123" {
		t.Fatalf("unexpected Pillar status: %+v", status)
	}

	startRecorder := httptest.NewRecorder()
	mux.ServeHTTP(startRecorder, httptest.NewRequest(http.MethodPost, "/api/chatgpt-auth/start", nil))
	if startRecorder.Code != http.StatusConflict {
		t.Fatalf("start code = %d, want %d", startRecorder.Code, http.StatusConflict)
	}
}

func TestChatGPTAuthPillarModeReportsReauthentication(t *testing.T) {
	t.Parallel()

	controller := newChatGPTAuthController(ChatGPTAuthConfig{
		Mode: ChatGPTAuthModePillar,
		PlatformStatus: func(context.Context) (chatgptauth.PlatformStatus, error) {
			return chatgptauth.PlatformStatus{
				ReauthRequired: true,
				ReauthCommand:  "ssh -L 1455:localhost:8080 x.spacecats.dev codex oauth",
			}, nil
		},
	}, func(context.Context) error { return nil }, nil)
	mux := http.NewServeMux()
	controller.registerRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/chatgpt-auth", nil))

	var status chatGPTAuthStatus
	if err := json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if status.Authenticated || !status.ReauthRequired || status.ReauthCommand == "" {
		t.Fatalf("unexpected Pillar status: %+v", status)
	}
}

func TestChatGPTAuthStandaloneStartSupportsPasteFallback(t *testing.T) {
	t.Parallel()

	manager := chatgptauth.NewManager(nil, chatgptauth.Config{})
	controller := newChatGPTAuthController(ChatGPTAuthConfig{
		Mode:    ChatGPTAuthModeStandalone,
		Manager: manager,
	}, func(context.Context) error { return nil }, nil)
	controller.listen = func(string, string) (net.Listener, error) {
		return nil, errors.New("address already in use")
	}
	mux := http.NewServeMux()
	controller.registerRoutes(mux)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/chatgpt-auth/start", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		chatgptauth.Flow
		CallbackListener bool   `json:"callback_listener"`
		ListenerError    string `json:"listener_error"`
	}
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.AuthorizationURL == "" || response.State == "" {
		t.Fatalf("missing OAuth flow fields: %+v", response)
	}
	if response.CallbackListener {
		t.Fatal("callback_listener = true, want false")
	}
	if response.ListenerError != "address already in use" {
		t.Fatalf("listener_error = %q", response.ListenerError)
	}
}

func TestChatGPTAuthStandaloneStatusUsesTransitionLock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 7, 0, 0, 0, time.UTC)
	store := &chatGPTAuthTestStore{loaded: true, credentials: chatgptauth.Credentials{
		AccessToken: "access", RefreshToken: "refresh", AccountID: "account-123", ExpiresAt: now.Add(time.Hour),
	}}
	manager := chatgptauth.NewManager(store, chatgptauth.Config{Now: func() time.Time { return now }})
	controller := newChatGPTAuthController(ChatGPTAuthConfig{
		Mode: ChatGPTAuthModeStandalone, Manager: manager,
	}, func(context.Context) error { return nil }, nil)
	store.onLoad = func() {
		if controller.transitionMu.TryLock() {
			controller.transitionMu.Unlock()
			t.Error("credential status read outside transition lock")
		}
	}

	recorder := httptest.NewRecorder()
	controller.handleStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/chatgpt-auth", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d", recorder.Code, http.StatusOK)
	}
	var status chatGPTAuthStatus
	if err := json.NewDecoder(recorder.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated {
		t.Fatalf("status = %+v, want authenticated", status)
	}
}

func TestChatGPTAuthStandaloneCompletionRefreshesUnderTransitionLock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 27, 7, 0, 0, 0, time.UTC)
	accessToken := chatGPTAuthTestJWT(t, map[string]any{"exp": now.Add(time.Hour).Unix()})
	idToken := chatGPTAuthTestJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-123"},
	})
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{
			"access_token":  accessToken,
			"refresh_token": "refresh-1",
			"id_token":      idToken,
			"expires_in":    3600,
		})
	}))
	defer issuer.Close()

	manager := chatgptauth.NewManager(&chatGPTAuthTestStore{}, chatgptauth.Config{
		Issuer: issuer.URL, HTTPClient: issuer.Client(), Now: func() time.Time { return now },
	})
	var controller *chatGPTAuthController
	controller = newChatGPTAuthController(ChatGPTAuthConfig{
		Mode: ChatGPTAuthModeStandalone, Manager: manager,
	}, func(context.Context) error {
		if controller.transitionMu.TryLock() {
			controller.transitionMu.Unlock()
			t.Error("model refresh ran outside transition lock")
		}
		return nil
	}, nil)
	flow, err := manager.StartFlow()
	if err != nil {
		t.Fatal(err)
	}
	callback := chatgptauth.DefaultRedirectURI + "?code=authorization-code&state=" + url.QueryEscape(flow.State)
	credentials, err := controller.completeFlow(manager, callback)
	if err != nil {
		t.Fatal(err)
	}
	if credentials.AccountID != "account-123" {
		t.Fatalf("account ID = %q, want account-123", credentials.AccountID)
	}
}

func chatGPTAuthTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "none", "typ": "JWT"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

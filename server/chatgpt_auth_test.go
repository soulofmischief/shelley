package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"shelley.exe.dev/chatgptauth"
)

func TestChatGPTAuthPillarModeHidesStandaloneControls(t *testing.T) {
	t.Parallel()

	controller := newChatGPTAuthController(ChatGPTAuthConfig{
		Mode:      ChatGPTAuthModePillar,
		ReauthURL: "https://pillar.example/auth",
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
	if status.Mode != ChatGPTAuthModePillar || !status.Configured || status.ReauthURL != "https://pillar.example/auth" {
		t.Fatalf("unexpected Pillar status: %+v", status)
	}

	startRecorder := httptest.NewRecorder()
	mux.ServeHTTP(startRecorder, httptest.NewRequest(http.MethodPost, "/api/chatgpt-auth/start", nil))
	if startRecorder.Code != http.StatusConflict {
		t.Fatalf("start code = %d, want %d", startRecorder.Code, http.StatusConflict)
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

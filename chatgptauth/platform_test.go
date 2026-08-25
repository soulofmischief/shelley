package chatgptauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestFetchPlatformStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/chatgpt/status" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer workspace-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"authenticated":true,"account_id":"account-123","expires_at":"2026-08-25T12:00:00Z"}`))
	}))
	defer server.Close()

	tokenFile := t.TempDir() + "/token"
	if err := os.WriteFile(tokenFile, []byte("workspace-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	client := TokenFileHTTPClient(server.Client(), tokenFile)
	status, err := FetchPlatformStatus(context.Background(), client, server.URL+"/api/v1/chatgpt")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Authenticated || status.AccountID != "account-123" || status.ExpiresAt != "2026-08-25T12:00:00Z" {
		t.Fatalf("status = %+v", status)
	}
}

func TestFetchPlatformStatusPropagatesGatewayFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, "gateway unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	_, err := FetchPlatformStatus(context.Background(), server.Client(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "HTTP 503: gateway unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

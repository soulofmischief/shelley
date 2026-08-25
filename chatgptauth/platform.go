package chatgptauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type PlatformStatus struct {
	Authenticated  bool   `json:"authenticated"`
	ReauthRequired bool   `json:"reauth_required,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	ReauthURL      string `json:"reauth_url,omitempty"`
	ReauthCommand  string `json:"reauth_command,omitempty"`
}

func FetchPlatformStatus(ctx context.Context, client *http.Client, baseURL string) (PlatformStatus, error) {
	if client == nil {
		return PlatformStatus{}, fmt.Errorf("Pillar ChatGPT HTTP client is nil")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/status", nil)
	if err != nil {
		return PlatformStatus{}, fmt.Errorf("create Pillar ChatGPT status request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return PlatformStatus{}, fmt.Errorf("request Pillar ChatGPT status: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 8<<10))
		if readErr != nil {
			return PlatformStatus{}, fmt.Errorf("Pillar ChatGPT status returned HTTP %d: %w", response.StatusCode, readErr)
		}
		return PlatformStatus{}, fmt.Errorf("Pillar ChatGPT status returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	var status PlatformStatus
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return PlatformStatus{}, fmt.Errorf("decode Pillar ChatGPT status: %w", err)
	}
	return status, nil
}

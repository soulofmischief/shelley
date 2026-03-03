package codex

import (
	"strings"
	"testing"
)

func TestGeneratePKCE(t *testing.T) {
	verifier, challenge, err := generatePKCE()

	if err != nil {
		t.Fatalf("generatePKCE failed: %v", err)
	}

	if len(verifier) == 0 {
		t.Error("verifier should not be empty")
	}

	if len(challenge) == 0 {
		t.Error("challenge should not be empty")
	}

	// Verifier should be base64url encoded (43 chars from 32 random bytes)
	if len(verifier) != 43 {
		t.Errorf("verifier length should be 43, got %d", len(verifier))
	}

	// Challenge should be base64url encoded SHA256 (43 chars)
	if len(challenge) != 43 {
		t.Errorf("challenge length should be 43, got %d", len(challenge))
	}
}

func TestGenerateState(t *testing.T) {
	state, err := generateState()

	if err != nil {
		t.Fatalf("generateState failed: %v", err)
	}

	if len(state) == 0 {
		t.Error("state should not be empty")
	}

	// State should be base64url encoded (22 chars from 16 random bytes)
	if len(state) != 22 {
		t.Errorf("state length should be 22, got %d", len(state))
	}
}

func TestCreatePkceChallenge(t *testing.T) {
	result, err := CreatePkceChallenge()

	if err != nil {
		t.Fatalf("CreatePkceChallenge failed: %v", err)
	}

	if result.AuthURL == "" {
		t.Error("AuthURL should not be empty")
	}

	if result.CodeVerifier == "" {
		t.Error("CodeVerifier should not be empty")
	}

	if result.State == "" {
		t.Error("State should not be empty")
	}

	// AuthURL should contain expected parameters
	expectedParams := []string{
		"response_type=code",
		"client_id=" + ClientID,
		"redirect_uri=",
		"code_challenge=",
		"code_challenge_method=S256",
		"state=",
	}

	for _, param := range expectedParams {
		if !strings.Contains(result.AuthURL, param) {
			t.Errorf("AuthURL should contain %s", param)
		}
	}
}

func TestParseCallbackURL(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantCode  string
		wantState string
		wantErr   bool
	}{
		{
			name:      "valid callback URL",
			url:       "http://localhost:1455/auth/callback?code=abc123&state=xyz789",
			wantCode:  "abc123",
			wantState: "xyz789",
			wantErr:   false,
		},
		{
			name:    "missing code",
			url:     "http://localhost:1455/auth/callback?state=xyz789",
			wantErr: true,
		},
		{
			name:      "missing state (allowed)",
			url:       "http://localhost:1455/auth/callback?code=abc123",
			wantCode:  "abc123",
			wantState: "", // state is optional
			wantErr:   false,
		},
		{
			name:    "invalid URL",
			url:     "not a url",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, state, err := ParseCallbackURL(tt.url)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}

			if state != tt.wantState {
				t.Errorf("state = %q, want %q", state, tt.wantState)
			}
		})
	}
}

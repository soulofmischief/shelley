// Package codex provides OAuth authentication and LLM service for OpenAI Codex
// using ChatGPT subscription credentials.
package codex

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	AuthBaseURL       = "https://auth.openai.com"
	ClientID          = "app_EMoamEEZ73f0CkXaXp7hrann"
	RedirectURI       = "http://localhost:1455/auth/callback"
	DefaultOriginator = "codex_cli_rs"
)

// PkceChallenge contains the PKCE parameters and auth URL for the OAuth flow.
type PkceChallenge struct {
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state"`
	AuthURL      string `json:"auth_url"`
}

// TokenResponse contains the OAuth tokens returned after authentication.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	IDToken      string `json:"id_token,omitempty"`
}

// Credentials contains the OAuth credentials for making API calls.
type Credentials struct {
	AccessToken  string
	RefreshToken string
	AccountID    string
	ExpiresAt    int64 // Unix timestamp
}

// NeedsRefresh returns true if the access token needs to be refreshed.
func (c *Credentials) NeedsRefresh() bool {
	// Refresh if expires within 5 minutes
	return time.Now().Unix() > c.ExpiresAt-300
}

// generateRandomBytes generates cryptographically secure random bytes.
func generateRandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	return b, err
}

// generatePKCE generates the code verifier and code challenge for PKCE.
func generatePKCE() (verifier, challenge string, err error) {
	bytes, err := generateRandomBytes(32)
	if err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(bytes)

	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// generateState generates a random state parameter for CSRF protection.
func generateState() (string, error) {
	bytes, err := generateRandomBytes(16)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

// CreatePkceChallenge creates a new PKCE challenge for the OAuth flow.
// Returns the challenge containing the auth URL the user should visit.
func CreatePkceChallenge() (*PkceChallenge, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("failed to generate PKCE: %w", err)
	}

	state, err := generateState()
	if err != nil {
		return nil, fmt.Errorf("failed to generate state: %w", err)
	}

	params := url.Values{
		"response_type":              {"code"},
		"client_id":                  {ClientID},
		"redirect_uri":               {RedirectURI},
		"scope":                      {"openid profile email offline_access"},
		"code_challenge":             {challenge},
		"code_challenge_method":      {"S256"},
		"state":                      {state},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
		"originator":                 {DefaultOriginator},
	}

	authURL := fmt.Sprintf("%s/oauth/authorize?%s", AuthBaseURL, params.Encode())

	return &PkceChallenge{
		CodeVerifier: verifier,
		State:        state,
		AuthURL:      authURL,
	}, nil
}

// ParseCallbackURL extracts the authorization code and state from a callback URL.
func ParseCallbackURL(callbackURL string) (code, state string, err error) {
	parsed, err := url.Parse(callbackURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid callback URL: %w", err)
	}

	code = parsed.Query().Get("code")
	state = parsed.Query().Get("state")

	if code == "" {
		errMsg := parsed.Query().Get("error")
		errDesc := parsed.Query().Get("error_description")
		if errMsg != "" {
			return "", "", fmt.Errorf("OAuth error: %s - %s", errMsg, errDesc)
		}
		return "", "", fmt.Errorf("no authorization code in callback URL")
	}

	return code, state, nil
}

// ExchangeCode exchanges an authorization code for tokens.
func ExchangeCode(code, codeVerifier string, client *http.Client) (*TokenResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}

	data := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {ClientID},
		"code":          {code},
		"redirect_uri":  {RedirectURI},
		"code_verifier": {codeVerifier},
	}

	req, err := http.NewRequest("POST", AuthBaseURL+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange code: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed: %d %s", resp.StatusCode, string(body))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &tokens, nil
}

// RefreshAccessToken refreshes an expired access token using the refresh token.
func RefreshAccessToken(refreshToken string, client *http.Client) (*TokenResponse, error) {
	if client == nil {
		client = http.DefaultClient
	}

	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {ClientID},
		"refresh_token": {refreshToken},
	}

	req, err := http.NewRequest("POST", AuthBaseURL+"/oauth/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token refresh failed: %d %s", resp.StatusCode, string(body))
	}

	var tokens TokenResponse
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("failed to parse token response: %w", err)
	}

	return &tokens, nil
}

// ExtractAccountID extracts the ChatGPT account ID from the ID token.
// The account ID is needed for the ChatGPT-Account-ID header.
func ExtractAccountID(idToken string) string {
	// ID token is a JWT - decode the payload (middle part)
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}

	// Decode base64url payload
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	// Parse JSON to extract organization/account info
	var claims struct {
		Organizations []struct {
			ID string `json:"id"`
		} `json:"https://api.openai.com/auth/organizations"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	if len(claims.Organizations) > 0 {
		return claims.Organizations[0].ID
	}
	return ""
}

// CreateCredentials creates Credentials from a TokenResponse.
func CreateCredentials(tokens *TokenResponse) *Credentials {
	return &Credentials{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		AccountID:    ExtractAccountID(tokens.IDToken),
		ExpiresAt:    time.Now().Unix() + tokens.ExpiresIn,
	}
}

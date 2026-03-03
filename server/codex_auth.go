package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"shelley.exe.dev/db/generated"
	"shelley.exe.dev/llm/codex"
)

// codexAuthState holds the in-memory state for an ongoing PKCE auth flow
type codexAuthState struct {
	CodeVerifier string
	State        string
	ExpiresAt    time.Time
}

var pendingCodexAuth *codexAuthState

// CodexAuthStatusResponse is the response for GET /api/codex-auth/status
type CodexAuthStatusResponse struct {
	Authenticated bool    `json:"authenticated"`
	AccountID     *string `json:"account_id,omitempty"`
	ExpiresAt     *int64  `json:"expires_at,omitempty"`
}

// CodexPkceStartResponse is the response for POST /api/codex-auth/pkce/start
type CodexPkceStartResponse struct {
	AuthURL string `json:"auth_url"`
	State   string `json:"state"`
}

// CodexPkceCompleteRequest is the request for POST /api/codex-auth/pkce/complete
type CodexPkceCompleteRequest struct {
	CallbackURL string `json:"callback_url"`
}

// handleCodexAuthStatus handles GET /api/codex-auth/status
func (s *Server) handleCodexAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cred, err := s.db.GetOAuthCredentials(r.Context(), "codex")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(CodexAuthStatusResponse{Authenticated: false})
			return
		}
		http.Error(w, "Failed to get auth status", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CodexAuthStatusResponse{
		Authenticated: true,
		AccountID:     cred.AccountID,
		ExpiresAt:     &cred.ExpiresAt,
	})
}

// handleCodexPkceStart handles POST /api/codex-auth/pkce/start
func (s *Server) handleCodexPkceStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	challenge, err := codex.CreatePkceChallenge()
	if err != nil {
		http.Error(w, "Failed to create PKCE challenge: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Store the pending auth state
	pendingCodexAuth = &codexAuthState{
		CodeVerifier: challenge.CodeVerifier,
		State:        challenge.State,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CodexPkceStartResponse{
		AuthURL: challenge.AuthURL,
		State:   challenge.State,
	})
}

// handleCodexPkceComplete handles POST /api/codex-auth/pkce/complete
func (s *Server) handleCodexPkceComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CodexPkceCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if pendingCodexAuth == nil {
		http.Error(w, "No pending auth flow - please start again", http.StatusBadRequest)
		return
	}

	if time.Now().After(pendingCodexAuth.ExpiresAt) {
		pendingCodexAuth = nil
		http.Error(w, "Auth flow expired - please start again", http.StatusBadRequest)
		return
	}

	// Parse the callback URL to extract the code
	code, state, err := codex.ParseCallbackURL(req.CallbackURL)
	if err != nil {
		http.Error(w, "Invalid callback URL: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Verify state matches
	if state != pendingCodexAuth.State {
		http.Error(w, "State mismatch - possible CSRF attack", http.StatusBadRequest)
		return
	}

	// Exchange code for tokens
	tokens, err := codex.ExchangeCode(code, pendingCodexAuth.CodeVerifier, nil)
	if err != nil {
		http.Error(w, "Failed to exchange code: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Extract account ID from ID token
	accountID := codex.ExtractAccountID(tokens.IDToken)

	// Calculate expiry
	expiresAt := time.Now().Unix() + tokens.ExpiresIn

	// Save credentials to database
	var accountIDPtr *string
	if accountID != "" {
		accountIDPtr = &accountID
	}

	_, err = s.db.UpsertOAuthCredentials(r.Context(), generated.UpsertOAuthCredentialsParams{
		Provider:     "codex",
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		AccountID:    accountIDPtr,
		ExpiresAt:    expiresAt,
	})
	if err != nil {
		http.Error(w, "Failed to save credentials: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Clear pending auth
	pendingCodexAuth = nil

	// Refresh models to pick up codex models
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models after codex auth", "error", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(CodexAuthStatusResponse{
		Authenticated: true,
		AccountID:     accountIDPtr,
		ExpiresAt:     &expiresAt,
	})
}

// handleCodexLogout handles POST /api/codex-auth/logout
func (s *Server) handleCodexLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := s.db.DeleteOAuthCredentials(r.Context(), "codex")
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Failed to logout: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Clear any pending auth
	pendingCodexAuth = nil

	// Refresh models
	if err := s.llmManager.RefreshCustomModels(); err != nil {
		s.logger.Warn("Failed to refresh custom models after codex logout", "error", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

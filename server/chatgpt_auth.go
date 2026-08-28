package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"shelley.exe.dev/chatgptauth"
)

const (
	ChatGPTAuthModeStandalone = "standalone"
	ChatGPTAuthModePillar     = "pillar"
	chatGPTAuthUpdateTimeout  = 30 * time.Second
)

type chatGPTModelRefreshError struct{ err error }

func (e *chatGPTModelRefreshError) Error() string {
	return "authenticated, but model refresh failed: " + e.err.Error()
}

func (e *chatGPTModelRefreshError) Unwrap() error { return e.err }

type chatGPTAuthController struct {
	config        ChatGPTAuthConfig
	refreshModels func(context.Context) error
	logger        *slog.Logger
	listen        func(string, string) (net.Listener, error)
	transitionMu  sync.Mutex

	callbackMu     sync.Mutex
	callbackServer *http.Server
	callbackTimer  *time.Timer
}

type chatGPTAuthStatus struct {
	Mode           string `json:"mode"`
	Configured     bool   `json:"configured"`
	Authenticated  bool   `json:"authenticated"`
	ReauthRequired bool   `json:"reauth_required,omitempty"`
	AccountID      string `json:"account_id,omitempty"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	ReauthURL      string `json:"reauth_url,omitempty"`
	ReauthCommand  string `json:"reauth_command,omitempty"`
	Error          string `json:"error,omitempty"`
}

func newChatGPTAuthController(config ChatGPTAuthConfig, refreshModels func(context.Context) error, logger *slog.Logger) *chatGPTAuthController {
	if logger == nil {
		logger = slog.Default()
	}
	return &chatGPTAuthController{
		config: config, refreshModels: refreshModels, logger: logger, listen: net.Listen,
	}
}

func (c *chatGPTAuthController) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/chatgpt-auth", c.handleStatus)
	mux.HandleFunc("POST /api/chatgpt-auth/start", c.handleStart)
	mux.HandleFunc("POST /api/chatgpt-auth/complete", c.handleComplete)
	mux.HandleFunc("DELETE /api/chatgpt-auth", c.handleLogout)
}

func (c *chatGPTAuthController) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := chatGPTAuthStatus{Mode: c.config.Mode}
	switch c.config.Mode {
	case ChatGPTAuthModePillar:
		status.Configured = c.config.PlatformStatus != nil
		if c.config.PlatformStatus != nil {
			platformStatus, err := c.config.PlatformStatus(r.Context())
			if err != nil {
				status.Error = err.Error()
				break
			}
			status.Authenticated = platformStatus.Authenticated
			status.ReauthRequired = platformStatus.ReauthRequired
			status.AccountID = platformStatus.AccountID
			status.ExpiresAt = platformStatus.ExpiresAt
			status.ReauthURL = platformStatus.ReauthURL
			status.ReauthCommand = platformStatus.ReauthCommand
		}
	case ChatGPTAuthModeStandalone:
		status.Configured = c.config.Manager != nil
		if c.config.Manager != nil {
			credentials, err := c.credentials(r.Context(), c.config.Manager)
			if err == nil {
				status.Authenticated = true
				status.AccountID = credentials.AccountID
				status.ExpiresAt = credentials.ExpiresAt.UTC().Format(time.RFC3339)
			} else if !errors.Is(err, chatgptauth.ErrNotAuthenticated) {
				status.Error = err.Error()
			}
		}
	default:
		status.Error = fmt.Sprintf("unsupported ChatGPT auth mode %q", c.config.Mode)
	}
	writeJSON(w, status)
}

func (c *chatGPTAuthController) handleStart(w http.ResponseWriter, r *http.Request) {
	manager, ok := c.standaloneManager(w)
	if !ok {
		return
	}
	flow, err := manager.StartFlow()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	listenerReady, listenerError := c.ensureCallbackListener(manager.RedirectURI())
	response := struct {
		chatgptauth.Flow
		CallbackListener bool   `json:"callback_listener"`
		ListenerError    string `json:"listener_error,omitempty"`
	}{Flow: flow, CallbackListener: listenerReady}
	if listenerError != nil {
		response.ListenerError = listenerError.Error()
	}
	writeJSON(w, response)
}

func (c *chatGPTAuthController) handleComplete(w http.ResponseWriter, r *http.Request) {
	manager, ok := c.standaloneManager(w)
	if !ok {
		return
	}
	var request struct {
		CallbackURL string `json:"callback_url"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid callback payload: "+err.Error(), http.StatusBadRequest)
		return
	}
	credentials, err := c.completeFlow(manager, request.CallbackURL)
	if err != nil {
		statusCode := http.StatusBadRequest
		var refreshErr *chatGPTModelRefreshError
		if errors.As(err, &refreshErr) {
			statusCode = http.StatusBadGateway
		}
		http.Error(w, err.Error(), statusCode)
		return
	}
	go c.closeCallbackListener()
	writeJSON(w, chatGPTAuthStatus{
		Mode: ChatGPTAuthModeStandalone, Configured: true, Authenticated: true,
		AccountID: credentials.AccountID, ExpiresAt: credentials.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (c *chatGPTAuthController) handleLogout(w http.ResponseWriter, r *http.Request) {
	manager, ok := c.standaloneManager(w)
	if !ok {
		return
	}
	if err := c.logout(manager); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *chatGPTAuthController) standaloneManager(w http.ResponseWriter) (*chatgptauth.Manager, bool) {
	if c.config.Mode != ChatGPTAuthModeStandalone || c.config.Manager == nil {
		http.Error(w, "ChatGPT authentication is managed by Pillar", http.StatusConflict)
		return nil, false
	}
	return c.config.Manager, true
}

func (c *chatGPTAuthController) ensureCallbackListener(redirectURI string) (bool, error) {
	parsed, err := url.Parse(redirectURI)
	if err != nil {
		return false, fmt.Errorf("parse OAuth redirect URI: %w", err)
	}
	port := parsed.Port()
	if port == "" {
		return false, fmt.Errorf("OAuth redirect URI has no port")
	}

	c.callbackMu.Lock()
	defer c.callbackMu.Unlock()
	if c.callbackServer != nil {
		c.resetCallbackTimerLocked()
		return true, nil
	}
	listener, err := c.listen("tcp4", "127.0.0.1:"+port)
	if err != nil {
		c.logger.Warn("ChatGPT OAuth callback listener unavailable; callback can be pasted in the UI", "error", err)
		return false, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+parsed.Path, c.handleLoopbackCallback)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	c.callbackServer = server
	c.resetCallbackTimerLocked()
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			c.logger.Error("ChatGPT OAuth callback listener failed", "error", serveErr)
		}
	}()
	return true, nil
}

func (c *chatGPTAuthController) handleLoopbackCallback(w http.ResponseWriter, r *http.Request) {
	credentials, err := c.completeFlow(c.config.Manager, r.URL.String())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, "<!doctype html><title>Shelley sign-in failed</title><h1>Sign-in failed</h1><p>%s</p>", html.EscapeString(err.Error()))
		return
	}
	fmt.Fprintf(w, "<!doctype html><title>Signed in to Shelley</title><h1>Signed in</h1><p>ChatGPT account %s is now available in Shelley. You can close this tab.</p>", html.EscapeString(credentials.AccountID))
	go c.closeCallbackListener()
}

func (c *chatGPTAuthController) completeFlow(manager *chatgptauth.Manager, callbackURL string) (chatgptauth.Credentials, error) {
	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()

	// OAuth completion must outlive the browser callback request. Once the code
	// exchange starts, credentials and the model catalog become one transition.
	ctx, cancel := context.WithTimeout(context.Background(), chatGPTAuthUpdateTimeout)
	defer cancel()
	credentials, err := manager.CompleteFlow(ctx, callbackURL)
	if err != nil {
		return chatgptauth.Credentials{}, err
	}
	if err := c.refreshModels(ctx); err != nil {
		return chatgptauth.Credentials{}, &chatGPTModelRefreshError{err: err}
	}
	return credentials, nil
}

func (c *chatGPTAuthController) credentials(ctx context.Context, manager *chatgptauth.Manager) (chatgptauth.Credentials, error) {
	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()
	return manager.Credentials(ctx)
}

func (c *chatGPTAuthController) logout(manager *chatgptauth.Manager) error {
	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), chatGPTAuthUpdateTimeout)
	defer cancel()
	if err := manager.Logout(ctx); err != nil {
		return err
	}
	if err := c.refreshModels(ctx); err != nil {
		return fmt.Errorf("signed out, but model refresh failed: %w", err)
	}
	return nil
}

func (c *chatGPTAuthController) resetCallbackTimerLocked() {
	if c.callbackTimer != nil {
		c.callbackTimer.Stop()
	}
	c.callbackTimer = time.AfterFunc(10*time.Minute, c.closeCallbackListener)
}

func (c *chatGPTAuthController) closeCallbackListener() {
	c.callbackMu.Lock()
	server := c.callbackServer
	c.callbackServer = nil
	if c.callbackTimer != nil {
		c.callbackTimer.Stop()
		c.callbackTimer = nil
	}
	c.callbackMu.Unlock()
	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			c.logger.Warn("failed to stop ChatGPT OAuth callback listener", "error", err)
		}
	}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}

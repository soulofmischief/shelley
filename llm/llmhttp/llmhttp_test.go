package llmhttp

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestContextFunctions(t *testing.T) {
	ctx := context.Background()

	// Test ConversationID
	ctx = WithConversationID(ctx, "conv-123")
	if got := ConversationIDFromContext(ctx); got != "conv-123" {
		t.Errorf("ConversationIDFromContext() = %q, want %q", got, "conv-123")
	}

	// Test ModelID
	ctx = WithModelID(ctx, "model-456")
	if got := ModelIDFromContext(ctx); got != "model-456" {
		t.Errorf("ModelIDFromContext() = %q, want %q", got, "model-456")
	}

	// Test Provider
	ctx = WithProvider(ctx, "anthropic")
	if got := ProviderFromContext(ctx); got != "anthropic" {
		t.Errorf("ProviderFromContext() = %q, want %q", got, "anthropic")
	}

	// Test empty context
	emptyCtx := context.Background()
	if got := ConversationIDFromContext(emptyCtx); got != "" {
		t.Errorf("ConversationIDFromContext(empty) = %q, want empty", got)
	}
	if got := ModelIDFromContext(emptyCtx); got != "" {
		t.Errorf("ModelIDFromContext(empty) = %q, want empty", got)
	}
	if got := ProviderFromContext(emptyCtx); got != "" {
		t.Errorf("ProviderFromContext(empty) = %q, want empty", got)
	}
}

func TestTransportAddsHeaders(t *testing.T) {
	// Create a test server that echoes request headers
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create client with our transport
	client := NewClient(nil, nil)

	// Make a request with conversation ID in context
	ctx := WithConversationID(context.Background(), "test-conv-id")
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	// Verify User-Agent header was added
	if !strings.HasPrefix(receivedHeaders.Get("User-Agent"), "Shelley") {
		t.Errorf("User-Agent = %q, want prefix 'Shelley'", receivedHeaders.Get("User-Agent"))
	}

	// Verify Shelley-Conversation-Id header was added
	if got := receivedHeaders.Get("Shelley-Conversation-Id"); got != "test-conv-id" {
		t.Errorf("Shelley-Conversation-Id = %q, want %q", got, "test-conv-id")
	}

	// Verify x-session-affinity is NOT added for non-fireworks providers
	if got := receivedHeaders.Get("x-session-affinity"); got != "" {
		t.Errorf("x-session-affinity = %q, want empty for non-fireworks", got)
	}
}

func TestTransportAddsSessionAffinityForFireworks(t *testing.T) {
	// Create a test server that echoes request headers
	var receivedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create client with our transport
	client := NewClient(nil, nil)

	// Make a request with conversation ID and provider=fireworks in context
	ctx := context.Background()
	ctx = WithConversationID(ctx, "test-conv-id")
	ctx = WithProvider(ctx, "fireworks")
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	// Verify x-session-affinity header was added for fireworks
	if got := receivedHeaders.Get("x-session-affinity"); got != "test-conv-id" {
		t.Errorf("x-session-affinity = %q, want %q", got, "test-conv-id")
	}

	// Verify Shelley-Conversation-Id header was also added
	if got := receivedHeaders.Get("Shelley-Conversation-Id"); got != "test-conv-id" {
		t.Errorf("Shelley-Conversation-Id = %q, want %q", got, "test-conv-id")
	}
}

func TestTransportRecordsRequest(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("response body: " + string(body)))
	}))
	defer server.Close()

	// Track recorded values
	var (
		recordedURL         string
		recordedRequestBody []byte
		recordedRespBody    []byte
		recordedStatusCode  int
		recordedDuration    time.Duration
		recorderCalled      bool
	)

	recorder := func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration) {
		recorderCalled = true
		recordedURL = url
		recordedRequestBody = requestBody
		recordedRespBody = responseBody
		recordedStatusCode = statusCode
		recordedDuration = duration
	}

	// Create client with recorder
	client := NewClient(nil, recorder)

	// Make a request with body
	req, _ := http.NewRequest("POST", server.URL, strings.NewReader("test body"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	// Read response body to ensure it's still accessible
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(respBody) != "response body: test body" {
		t.Errorf("Response body = %q, want %q", string(respBody), "response body: test body")
	}

	// Verify recorder was called with correct values
	if !recorderCalled {
		t.Fatal("Recorder was not called")
	}

	if recordedURL != server.URL {
		t.Errorf("Recorded URL = %q, want %q", recordedURL, server.URL)
	}

	if string(recordedRequestBody) != "test body" {
		t.Errorf("Recorded request body = %q, want %q", string(recordedRequestBody), "test body")
	}

	if string(recordedRespBody) != "response body: test body" {
		t.Errorf("Recorded response body = %q, want %q", string(recordedRespBody), "response body: test body")
	}

	if recordedStatusCode != http.StatusOK {
		t.Errorf("Recorded status code = %d, want %d", recordedStatusCode, http.StatusOK)
	}

	if recordedDuration <= 0 {
		t.Error("Recorded duration should be positive")
	}
}

func TestTransportWithoutRecorder(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	// Create client without recorder
	client := NewClient(nil, nil)

	// Make a request
	req, _ := http.NewRequest("GET", server.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("Status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestTransportDoesNotBufferSSEForRecorder(t *testing.T) {
	testStreamingPassThrough(t, true, false)
}

func TestTransportDoesNotBufferWhenSSERequestedEvenWithoutSSEContentType(t *testing.T) {
	testStreamingPassThrough(t, false, true)
}

func testStreamingPassThrough(t *testing.T, setSSEContentType bool, setSSEAccept bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	sent := make(chan int, 3)
	ack := make(chan int, 2)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if setSSEContentType {
			w.Header().Set("Content-Type", "text/event-stream")
		}

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}

		for i := 1; i <= 3; i++ {
			fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			flusher.Flush()
			sent <- i
			if i < 3 {
				select {
				case got := <-ack:
					if got != i {
						t.Errorf("ack = %d, want %d", got, i)
						return
					}
				case <-ctx.Done():
					t.Error("timed out waiting for client ack")
					return
				}
			}
		}
	}))
	defer server.Close()

	client := NewClient(nil, func(ctx context.Context, url string, requestBody, responseBody []byte, statusCode int, err error, duration time.Duration) {
		// no-op
	})

	req, _ := http.NewRequest("GET", server.URL, nil)
	if setSSEAccept {
		req.Header.Set("Accept", "text/event-stream")
	}

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	var resp *http.Response
	select {
	case err := <-errCh:
		t.Fatalf("request failed: %v", err)
	case resp = <-respCh:
		// good
	case <-ctx.Done():
		t.Fatal("client did not receive response in time (possible buffering)")
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	seen := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		seen++

		select {
		case expected := <-sent:
			if expected != seen {
				t.Fatalf("received event %d but server sent %d", seen, expected)
			}
		case <-ctx.Done():
			t.Fatal("timed out waiting for server send signal")
		}

		if seen < 3 {
			ack <- seen
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error: %v", err)
	}

	if seen != 3 {
		t.Fatalf("expected 3 events, got %d", seen)
	}
}

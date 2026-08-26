package client

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestResolvePrompt(t *testing.T) {
	tests := []struct {
		name       string
		flagPrompt string
		args       []string
		stdin      string
		want       string
		wantError  bool
	}{
		{name: "flag", flagPrompt: "hello", want: "hello"},
		{name: "positional", args: []string{"hello", "world"}, want: "hello world"},
		{name: "stdin", flagPrompt: "-", stdin: "from stdin\n", want: "from stdin\n"},
		{name: "ambiguous", flagPrompt: "hello", args: []string{"world"}, wantError: true},
		{name: "empty", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolvePrompt(test.flagPrompt, test.args, strings.NewReader(test.stdin))
			if test.wantError {
				if err == nil {
					t.Fatalf("resolvePrompt() = %q, nil", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("resolvePrompt() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRunChatStreamsCurrentTurnAndArchivesEphemeralConversation(t *testing.T) {
	var mu sync.Mutex
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		operations = append(operations, request.Method+" "+request.URL.Path)
		mu.Unlock()
		switch request.Method + " " + request.URL.Path {
		case "POST /api/conversations/new":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["message"] != "hello from CLI" || body["model"] != "gpt-5.6" {
				t.Errorf("unexpected chat body: %#v", body)
			}
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"conversation_id":"abc123"}`))
		case "GET /api/conversation/abc123/stream":
			writer.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(writer, "data: {\"messages\":[%s],\"snapshot_complete\":true}\n\n", messageJSON(1, "user", "hello from CLI", false))
			fmt.Fprint(writer, "data: {\"stream_delta\":{\"type\":\"text\",\"text\":\"hello back\",\"seq\":1}}\n\n")
			fmt.Fprintf(writer, "data: {\"messages\":[%s]}\n\n", messageJSON(2, "agent", "hello back", true))
		case "POST /api/conversation/abc123/archive":
			writer.WriteHeader(http.StatusOK)
		default:
			http.Error(writer, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := run([]string{"-url", server.URL, "chat", "-model", "gpt-5.6", "-ephemeral", "hello", "from", "CLI"},
		strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello back\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Conversation ID: abc123") || !strings.Contains(stderr.String(), "Archived abc123") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"POST /api/conversations/new",
		"GET /api/conversation/abc123/stream",
		"POST /api/conversation/abc123/archive",
	}
	if fmt.Sprint(operations) != fmt.Sprint(want) {
		t.Fatalf("operations = %v, want %v", operations, want)
	}
}

func TestRunChatImmediateJSONReturnsConversationEventWithoutStreaming(t *testing.T) {
	streamRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			streamRequested = true
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"conversation_id":"quick"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	err := run([]string{"-url", server.URL, "-json", "chat", "-immediate", "hello"},
		strings.NewReader(""), &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if streamRequested {
		t.Fatal("immediate chat opened the stream")
	}
	var event conversationStartedEvent
	if err := json.Unmarshal(stdout.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "conversation_started" || event.ConversationID != "quick" {
		t.Fatalf("event = %+v", event)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsImmediateEphemeralCombination(t *testing.T) {
	err := run([]string{"chat", "-immediate", "-ephemeral", "hello"},
		strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDispatchesTagCommands(t *testing.T) {
	tagServer := &fakeTagServer{
		tags: map[string][]string{"conv1": {"alpha"}},
		list: []map[string]any{{"conversation_id": "conv1", "tags": `["alpha","beta"]`}},
	}
	server := httptest.NewServer(tagServer.handler())
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-url", server.URL, "tag", "conv1", "beta"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(tagServer.tags["conv1"]), "[alpha beta]"; got != want {
		t.Fatalf("stored tags = %s, want %s", got, want)
	}
	var tagged struct {
		ConversationID string   `json:"conversation_id"`
		Tags           []string `json:"tags"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &tagged); err != nil {
		t.Fatal(err)
	}
	if tagged.ConversationID != "conv1" || fmt.Sprint(tagged.Tags) != "[alpha beta]" {
		t.Fatalf("tag output = %+v", tagged)
	}

	stdout.Reset()
	if err := run([]string{"-url", server.URL, "tags"},
		strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var listed []tagCount
	decoder := json.NewDecoder(&stdout)
	for {
		var tag tagCount
		err := decoder.Decode(&tag)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		listed = append(listed, tag)
	}
	if got, want := fmt.Sprint(listed), "[{alpha 1} {beta 1}]"; got != want {
		t.Fatalf("tags output = %s, want %s", got, want)
	}
}

func TestRunHelpAndMissingCommandAreDistinct(t *testing.T) {
	var help bytes.Buffer
	if err := run([]string{"-h"}, strings.NewReader(""), &bytes.Buffer{}, &help); err != flag.ErrHelp {
		t.Fatalf("help error = %v", err)
	}
	if !strings.Contains(help.String(), "Shelley CLI client") {
		t.Fatalf("help output = %q", help.String())
	}

	var usage bytes.Buffer
	err := run(nil, strings.NewReader(""), &bytes.Buffer{}, &usage)
	if err == nil || err.Error() != "command required" {
		t.Fatalf("missing-command error = %v", err)
	}
}

func TestOutputColorAndRecursiveToolResult(t *testing.T) {
	var output bytes.Buffer
	configured := outputConfig{writer: &output, color: true}
	msg := messageWithContent(1, "agent", true, llmContentWire{
		Type: contentTypeToolResult,
		ToolResult: []llmContentWire{{
			Type: contentTypeText,
			Text: "tool output",
		}},
	})
	if err := configured.printMessage(msg, true, true); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != colorDim+"tool output"+colorReset+"\n" {
		t.Fatalf("output = %q", got)
	}
	if got := (outputConfig{}).cyan("plain"); got != "plain" {
		t.Fatalf("color-disabled output = %q", got)
	}
}

func messageJSON(sequence int64, role, text string, endOfTurn bool) string {
	encoded, err := json.Marshal(message(sequence, role, text, endOfTurn))
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

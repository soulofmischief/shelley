package client

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStreamRendererFiltersReplayBeforeLatestUser(t *testing.T) {
	var output bytes.Buffer
	renderer := newStreamRenderer(outputConfig{writer: &output}, streamMode{
		onlyAfterLastUser: true,
		stopAtEndOfTurn:   true,
	})

	finished, err := renderer.consume(streamResponseWire{
		Messages: []messageWire{
			message(1, "user", "old question", false),
			message(2, "agent", "old answer", true),
			message(3, "user", "new question", false),
			message(4, "agent", "new answer", true),
		},
		SnapshotComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !finished {
		t.Fatal("expected completed replayed turn")
	}
	if got := output.String(); got != "new answer" {
		t.Fatalf("output = %q, want %q", got, "new answer")
	}
}

func TestStreamRendererDoesNotStopOnHistoricalTurn(t *testing.T) {
	var output bytes.Buffer
	renderer := newStreamRenderer(outputConfig{writer: &output}, streamMode{stopAtEndOfTurn: true})
	finished, err := renderer.consume(streamResponseWire{
		Messages: []messageWire{
			message(1, "user", "old question", false),
			message(2, "agent", "old answer", true),
			message(3, "user", "new question", false),
		},
		SnapshotComplete: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		t.Fatal("historical end-of-turn completed the current turn")
	}
}

func TestStreamRendererStreamsDeltasWithoutRepeatingFinalText(t *testing.T) {
	var output bytes.Buffer
	renderer := newStreamRenderer(outputConfig{writer: &output}, streamMode{stopAtEndOfTurn: true})
	if _, err := renderer.consume(streamResponseWire{SnapshotComplete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.consume(streamResponseWire{
		StreamDelta: &streamDeltaWire{Type: "thinking", Text: "consider", Seq: 10},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.consume(streamResponseWire{
		StreamDelta: &streamDeltaWire{Type: "text", Text: "answer", Seq: 11},
	}); err != nil {
		t.Fatal(err)
	}
	finished, err := renderer.consume(streamResponseWire{Messages: []messageWire{
		messageWithContent(1, "agent", true,
			llmContentWire{Type: contentTypeThinking, Thinking: "consider"},
			llmContentWire{Type: contentTypeText, Text: "answer"},
			llmContentWire{Type: contentTypeToolUse, ToolName: "shell"},
		),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !finished {
		t.Fatal("expected end of turn")
	}
	if got := output.String(); got != "consideranswer\n[shell]\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestStreamRendererJSONLinesEmitCompletedMessages(t *testing.T) {
	var output bytes.Buffer
	renderer := newStreamRenderer(outputConfig{writer: &output, jsonLines: true}, streamMode{stopAtEndOfTurn: true})
	if _, err := renderer.consume(streamResponseWire{SnapshotComplete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.consume(streamResponseWire{
		StreamDelta: &streamDeltaWire{Type: "text", Text: "partial", Seq: 1},
	}); err != nil {
		t.Fatal(err)
	}
	finished, err := renderer.consume(streamResponseWire{Messages: []messageWire{
		messageWithContent(7, "agent", true,
			llmContentWire{Type: contentTypeThinking, Thinking: "reason"},
			llmContentWire{Type: contentTypeText, Text: "complete"},
		),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !finished {
		t.Fatal("expected end of turn")
	}
	var event streamEvent
	if err := json.Unmarshal(output.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	if event.SequenceID != 7 || event.Text != "complete" || event.Thinking != "reason" || !event.EndOfTurn {
		t.Fatalf("unexpected event: %+v", event)
	}
	if strings.Contains(output.String(), "partial") {
		t.Fatalf("JSON output leaked transient delta: %s", output.String())
	}
}

func TestStreamRendererRejectsDeltaGap(t *testing.T) {
	renderer := newStreamRenderer(outputConfig{writer: &bytes.Buffer{}}, streamMode{})
	if _, err := renderer.consume(streamResponseWire{SnapshotComplete: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := renderer.consume(streamResponseWire{StreamDelta: &streamDeltaWire{Type: "text", Seq: 4}}); err != nil {
		t.Fatal(err)
	}
	_, err := renderer.consume(streamResponseWire{StreamDelta: &streamDeltaWire{Type: "text", Seq: 6}})
	if err == nil || !strings.Contains(err.Error(), "jumped from 4 to 6") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStreamConversationRequiresSnapshotAndEndOfTurn(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "snapshot", body: "data: {\"heartbeat\":true}\n\n", want: "before snapshot completion"},
		{name: "turn", body: "data: {\"snapshot_complete\":true}\n\n", want: "before the agent turn completed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			cc := &clientConfig{
				serverURL: server.URL,
				output:    outputConfig{writer: &bytes.Buffer{}},
			}
			err := streamConversation(cc, server.Client(), server.URL, "conversation", streamMode{stopAtEndOfTurn: true})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func message(sequence int64, role, text string, endOfTurn bool) messageWire {
	return messageWithContent(sequence, role, endOfTurn, llmContentWire{Type: contentTypeText, Text: text})
}

func messageWithContent(sequence int64, role string, endOfTurn bool, content ...llmContentWire) messageWire {
	data, err := json.Marshal(llmMessageWire{Content: content})
	if err != nil {
		panic(err)
	}
	raw := string(data)
	return messageWire{SequenceID: sequence, Type: role, LlmData: &raw, EndOfTurn: &endOfTurn}
}

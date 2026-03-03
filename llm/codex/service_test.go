package codex

import (
	"strings"
	"testing"
)

func TestParseSSEReasoningEvents(t *testing.T) {
	// Simulate a stream with reasoning events matching upstream codex format
	sse := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp-1"}}`,
		`data: {"type":"response.output_item.added","item":{"type":"reasoning","id":"rs-1","summary":[]}}`,
		`data: {"type":"response.reasoning_summary_part.added","summary_index":0}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"First ","summary_index":0}`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"summary.","summary_index":0}`,
		`data: {"type":"response.reasoning_text.delta","delta":"raw ","content_index":0}`,
		`data: {"type":"response.reasoning_text.delta","delta":"content.","content_index":0}`,
		`data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"First summary."}],"content":[{"type":"reasoning_text","text":"raw content."}]}}`,
		`data: {"type":"response.output_item.added","item":{"type":"message","id":"msg-1","role":"assistant"}}`,
		`data: {"type":"response.output_text.delta","delta":"Hello "}`,
		`data: {"type":"response.output_text.delta","delta":"world."}`,
		`data: {"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant"}}`,
		`data: {"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
		"",
	}, "\n")

	var thinkingChunks []string
	var textChunks []string

	resp, err := parseSSEStreamWithCallbacks(
		strings.NewReader(sse),
		func(text string) { textChunks = append(textChunks, text) },
		func(thinking string) { thinkingChunks = append(thinkingChunks, thinking) },
	)
	if err != nil {
		t.Fatalf("parseSSEStreamWithCallbacks: %v", err)
	}

	// Verify streaming callbacks
	if got := strings.Join(textChunks, ""); got != "Hello world." {
		t.Errorf("text stream = %q, want %q", got, "Hello world.")
	}
	if got := strings.Join(thinkingChunks, ""); got != "First summary." {
		t.Errorf("thinking stream = %q, want %q", got, "First summary.")
	}

	// Verify accumulated response: output_item.done should populate reasoning
	var hasReasoning, hasMessage bool
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			hasReasoning = true
			if len(item.Summary) == 0 || item.Summary[0] != "First summary." {
				t.Errorf("reasoning summary = %v, want [First summary.]", item.Summary)
			}
			if len(item.ReasoningContent) == 0 || item.ReasoningContent[0] != "raw content." {
				t.Errorf("reasoning content = %v, want [raw content.]", item.ReasoningContent)
			}
		case "message":
			hasMessage = true
		}
	}
	if !hasReasoning {
		t.Error("expected reasoning item in output")
	}
	if !hasMessage {
		t.Error("expected message item in output")
	}
}

func TestParseSSEReasoningFailed(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp-1"}}`,
		`data: {"type":"response.failed","response":{"id":"resp-1","error":{"code":"rate_limit_exceeded","message":"Too fast"}}}`,
		"",
	}, "\n")

	_, err := parseSSEStreamWithCallbacks(strings.NewReader(sse), nil, nil)
	if err == nil {
		t.Fatal("expected error from response.failed")
	}
	if !strings.Contains(err.Error(), "rate_limit_exceeded") {
		t.Errorf("error = %v, want rate_limit_exceeded", err)
	}
}

func TestParseSSEReasoningIncomplete(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.incomplete","response":{"id":"resp-1"}}`,
		"",
	}, "\n")

	_, err := parseSSEStreamWithCallbacks(strings.NewReader(sse), nil, nil)
	if err == nil {
		t.Fatal("expected error from response.incomplete")
	}
}

func TestParseSSECompletedWithTaggedSummary(t *testing.T) {
	// Test that response.completed with tagged-union summary format parses correctly
	sse := strings.Join([]string{
		`data: {"type":"response.completed","response":{"id":"resp-1","output":[{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"The model reasoned about X."}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Answer."}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":20}}}}`,
		"",
	}, "\n")

	resp, err := parseSSEStreamWithCallbacks(strings.NewReader(sse), nil, nil)
	if err != nil {
		t.Fatalf("parseSSEStreamWithCallbacks: %v", err)
	}

	if len(resp.Output) != 2 {
		t.Fatalf("output len = %d, want 2", len(resp.Output))
	}
	if resp.Output[0].Type != "reasoning" {
		t.Errorf("output[0].type = %s, want reasoning", resp.Output[0].Type)
	}
	if len(resp.Output[0].Summary) != 1 || resp.Output[0].Summary[0] != "The model reasoned about X." {
		t.Errorf("output[0].summary = %v, want [The model reasoned about X.]", resp.Output[0].Summary)
	}
}

func TestReasoningSummariesUnmarshal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", `[]`, nil},
		{"plain_strings", `["hello","world"]`, []string{"hello", "world"}},
		{"tagged_union", `[{"type":"summary_text","text":"hello"},{"type":"summary_text","text":"world"}]`, []string{"hello", "world"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r reasoningSummaries
			if err := r.UnmarshalJSON([]byte(tt.input)); err != nil {
				t.Fatalf("UnmarshalJSON(%s): %v", tt.input, err)
			}
			if len(r) != len(tt.want) {
				t.Fatalf("got %v, want %v", r, tt.want)
			}
			for i := range r {
				if r[i] != tt.want[i] {
					t.Errorf("[%d] got %q, want %q", i, r[i], tt.want[i])
				}
			}
		})
	}
}

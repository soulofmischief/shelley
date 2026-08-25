package client

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type streamResponseWire struct {
	Messages         []messageWire    `json:"messages"`
	Heartbeat        bool             `json:"heartbeat"`
	SnapshotComplete bool             `json:"snapshot_complete"`
	StreamDelta      *streamDeltaWire `json:"stream_delta,omitempty"`
}

type streamDeltaWire struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Index int    `json:"index"`
	Seq   int64  `json:"seq"`
}

type messageWire struct {
	SequenceID int64   `json:"sequence_id"`
	Type       string  `json:"type"`
	LlmData    *string `json:"llm_data,omitempty"`
	EndOfTurn  *bool   `json:"end_of_turn,omitempty"`
}

type llmMessageWire struct {
	Content []llmContentWire `json:"Content"`
}

type llmContentWire struct {
	Type       int              `json:"Type"`
	Text       string           `json:"Text,omitempty"`
	Thinking   string           `json:"Thinking,omitempty"`
	ToolName   string           `json:"ToolName,omitempty"`
	ToolInput  json.RawMessage  `json:"ToolInput,omitempty"`
	ToolResult []llmContentWire `json:"ToolResult,omitempty"`
}

type conversationWire struct {
	ConversationID string  `json:"conversation_id"`
	Slug           *string `json:"slug"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	Working        bool    `json:"working"`
	Model          *string `json:"model"`
}

type modelWire struct {
	ID                    string   `json:"id"`
	DisplayName           string   `json:"display_name,omitempty"`
	Source                string   `json:"source,omitempty"`
	BaseURL               string   `json:"base_url,omitempty"`
	APIType               string   `json:"api_type,omitempty"`
	Ready                 bool     `json:"ready"`
	MaxContextTokens      int      `json:"max_context_tokens,omitempty"`
	IsDefault             bool     `json:"is_default,omitempty"`
	Tier                  int      `json:"tier,omitempty"`
	SupportsImages        bool     `json:"supports_images"`
	SupportsReasoning     bool     `json:"supports_reasoning"`
	ReasoningLevels       []string `json:"reasoning_levels,omitempty"`
	SupportsProMode       bool     `json:"supports_pro_mode,omitempty"`
	SupportsFastMode      bool     `json:"supports_fast_mode,omitempty"`
	DefaultReasoningLevel string   `json:"default_reasoning_level,omitempty"`
}

type streamEvent struct {
	SequenceID int64  `json:"sequence_id"`
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	EndOfTurn  bool   `json:"end_of_turn"`
}

const (
	contentTypeText       = 2
	contentTypeThinking   = 3
	contentTypeToolUse    = 5
	contentTypeToolResult = 6
)

func decodeLLMMessage(raw string) (llmMessageWire, bool) {
	var message llmMessageWire
	if err := json.Unmarshal([]byte(raw), &message); err != nil {
		return llmMessageWire{}, false
	}
	return message, true
}

func simplifyMessage(msg messageWire) streamEvent {
	event := streamEvent{SequenceID: msg.SequenceID, Type: msg.Type}
	if msg.EndOfTurn != nil {
		event.EndOfTurn = *msg.EndOfTurn
	}
	if msg.LlmData == nil {
		return event
	}
	message, ok := decodeLLMMessage(*msg.LlmData)
	if !ok {
		return event
	}

	var texts, thinking []string
	for _, content := range message.Content {
		switch content.Type {
		case contentTypeText:
			if content.Text != "" {
				texts = append(texts, content.Text)
			}
		case contentTypeThinking:
			if content.Thinking != "" {
				thinking = append(thinking, content.Thinking)
			}
		case contentTypeToolUse:
			if event.ToolName == "" {
				event.ToolName = content.ToolName
			}
		case contentTypeToolResult:
			texts = append(texts, contentTexts(content.ToolResult)...)
		}
	}
	event.Text = strings.Join(texts, "\n")
	event.Thinking = strings.Join(thinking, "\n")
	return event
}

func contentTexts(contents []llmContentWire) []string {
	var texts []string
	for _, content := range contents {
		if content.Text != "" {
			texts = append(texts, content.Text)
		}
		texts = append(texts, contentTexts(content.ToolResult)...)
	}
	return texts
}

func writeJSONLine(writer io.Writer, value any) error {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return fmt.Errorf("write JSON output: %w", err)
	}
	return nil
}

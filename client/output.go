package client

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	colorReset  = "\033[0m"
	colorDim    = "\033[2m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
)

type outputConfig struct {
	jsonLines bool
	color     bool
	writer    io.Writer
}

func (o outputConfig) style(code, value string) string {
	if !o.color {
		return value
	}
	return code + value + colorReset
}

func (o outputConfig) dim(value string) string    { return o.style(colorDim, value) }
func (o outputConfig) cyan(value string) string   { return o.style(colorCyan, value) }
func (o outputConfig) yellow(value string) string { return o.style(colorYellow, value) }
func (o outputConfig) green(value string) string  { return o.style(colorGreen, value) }
func (o outputConfig) red(value string) string    { return o.style(colorRed, value) }

func colorEnabled(noColor bool, stdout io.Writer) bool {
	if noColor || os.Getenv("NO_COLOR") != "" {
		return false
	}
	file, ok := stdout.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (o outputConfig) printMessage(msg messageWire, includeText, includeThinking bool) error {
	if msg.LlmData == nil {
		return nil
	}
	llmMessage, ok := decodeLLMMessage(*msg.LlmData)
	if !ok {
		return fmt.Errorf("decode LLM data for message %d", msg.SequenceID)
	}

	for _, content := range llmMessage.Content {
		switch content.Type {
		case contentTypeThinking:
			if includeThinking && content.Thinking != "" {
				if _, err := fmt.Fprint(o.writer, o.dim(content.Thinking)); err != nil {
					return err
				}
			}
		case contentTypeText:
			if includeText && content.Text != "" {
				if _, err := fmt.Fprint(o.writer, content.Text); err != nil {
					return err
				}
			}
		case contentTypeToolUse:
			if content.ToolName != "" {
				if _, err := fmt.Fprintf(o.writer, "\n%s\n", o.cyan("["+content.ToolName+"]")); err != nil {
					return err
				}
			}
		case contentTypeToolResult:
			for _, result := range contentTexts(content.ToolResult) {
				text := result
				if len(text) > 500 {
					text = text[:500] + "..."
				}
				if _, err := fmt.Fprintf(o.writer, "%s\n", o.dim(text)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (o outputConfig) printReadMessage(msg messageWire) error {
	prefix := ""
	switch msg.Type {
	case "user":
		prefix = o.green("USER: ")
	case "agent":
		prefix = o.cyan("AGENT: ")
	case "error":
		prefix = o.red("ERROR: ")
	default:
		return nil
	}
	if msg.LlmData == nil {
		return nil
	}
	llmMessage, ok := decodeLLMMessage(*msg.LlmData)
	if !ok {
		return fmt.Errorf("decode LLM data for message %d", msg.SequenceID)
	}

	var parts []string
	for _, content := range llmMessage.Content {
		switch content.Type {
		case contentTypeText:
			if content.Text != "" {
				parts = append(parts, content.Text)
			}
		case contentTypeThinking:
			if content.Thinking != "" {
				parts = append(parts, o.dim("[thinking] "+content.Thinking))
			}
		case contentTypeToolUse:
			if content.ToolName != "" {
				parts = append(parts, o.yellow("[tool: "+content.ToolName+"]"))
			}
		}
	}
	if len(parts) > 0 {
		if _, err := fmt.Fprintf(o.writer, "%s%s\n\n", prefix, strings.Join(parts, "\n")); err != nil {
			return err
		}
	}
	return nil
}

func (o outputConfig) printConversations(conversations []conversationWire) error {
	for _, conversation := range conversations {
		if o.jsonLines {
			if err := writeJSONLine(o.writer, conversation); err != nil {
				return err
			}
			continue
		}
		slug := ""
		if conversation.Slug != nil {
			slug = *conversation.Slug
		}
		model := ""
		if conversation.Model != nil {
			model = *conversation.Model
		}
		working := ""
		if conversation.Working {
			working = o.yellow(" [working]")
		}
		if _, err := fmt.Fprintf(o.writer, "%s  %s  %s%s\n",
			o.cyan(conversation.ConversationID), o.dim(model), slug, working); err != nil {
			return err
		}
	}
	return nil
}

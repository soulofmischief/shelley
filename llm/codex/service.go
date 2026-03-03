package codex

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"

	"shelley.exe.dev/llm"
)

const (
	// ChatGPTAPIURL is the base URL for the ChatGPT backend API when using OAuth
	ChatGPTAPIURL = "https://chatgpt.com/backend-api/codex"
	// DefaultMaxTokens is the default max output tokens
	DefaultMaxTokens = 16384
	// DefaultModel is the default Codex model
	DefaultModel = "gpt-5.3-codex"
)

// Service provides chat completions using the OpenAI Responses API
// with ChatGPT OAuth credentials.
type Service struct {
	HTTPC         *http.Client      // defaults to http.DefaultClient if nil
	AccessToken   string            // OAuth access token
	AccountID     string            // ChatGPT account ID for the header
	Model         string            // model name (e.g., "gpt-5.3-codex")
	MaxTokens     int               // defaults to DefaultMaxTokens if zero
	DumpLLM       bool              // whether to dump request/response for debugging
	ThinkingLevel llm.ThinkingLevel // thinking level (ThinkingLevelOff disables reasoning)
}

var _ llm.Service = (*Service)(nil)

// Responses API request/response types

type responsesRequest struct {
	Model        string               `json:"model"`
	Input        []responsesInputItem `json:"input"`
	Instructions string               `json:"instructions,omitempty"` // System prompt
	Tools        []responsesTool      `json:"tools,omitempty"`
	ToolChoice   any                  `json:"tool_choice,omitempty"`
	// Note: max_output_tokens is not supported by ChatGPT API
	Reasoning *responsesReasoning `json:"reasoning,omitempty"`
	Include   []string            `json:"include,omitempty"`
	Stream    bool                `json:"stream"` // Must be true for ChatGPT API
	Store     bool                `json:"store"`  // Must be false for ChatGPT API
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`  // "low", "medium", "high"
	Summary string `json:"summary,omitempty"` // "auto", "concise", "detailed"
}

type responsesInputItem struct {
	Type      string             `json:"type"`                // "message", "function_call", "function_call_output"
	Role      string             `json:"role,omitempty"`      // for messages: "user", "assistant"
	Content   []responsesContent `json:"content,omitempty"`   // for messages
	CallID    string             `json:"call_id,omitempty"`   // for function_call and function_call_output
	Name      string             `json:"name,omitempty"`      // for function_call
	Arguments string             `json:"arguments,omitempty"` // for function_call
	Output    string             `json:"output,omitempty"`    // for function_call_output
}

type responsesContent struct {
	Type string `json:"type"` // "input_text", "output_text"
	Text string `json:"text"`
}

type responsesTool struct {
	Type        string          `json:"type"` // "function"
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type responsesResponse struct {
	ID        string                `json:"id"`
	Object    string                `json:"object"` // "response"
	CreatedAt int64                 `json:"created_at"`
	Status    string                `json:"status"` // "completed", "incomplete", etc.
	Model     string                `json:"model"`
	Output    []responsesOutputItem `json:"output"`
	Usage     responsesUsage        `json:"usage"`
	Error     *responsesError       `json:"error"`
}

type responsesOutputItem struct {
	ID               string             `json:"id"`
	Type             string             `json:"type"`           // "message", "reasoning", "function_call"
	Role             string             `json:"role,omitempty"` // for messages: "assistant"
	Status           string             `json:"status,omitempty"`
	Content          []responsesContent `json:"content,omitempty"`   // for messages
	CallID           string             `json:"call_id,omitempty"`   // for function_call
	Name             string             `json:"name,omitempty"`      // for function_call
	Arguments        string             `json:"arguments,omitempty"` // for function_call
	Summary          reasoningSummaries `json:"summary,omitempty"`   // for reasoning
	ReasoningContent []string           `json:"-"`                   // for reasoning raw content (populated by SSE parser)
}

// reasoningSummaries handles both plain string arrays and tagged-union format
// from the Responses API: [{"type": "summary_text", "text": "..."}]
type reasoningSummaries []string

func (r *reasoningSummaries) UnmarshalJSON(data []byte) error {
	// Try plain string array first
	var plain []string
	if err := json.Unmarshal(data, &plain); err == nil {
		*r = plain
		return nil
	}
	// Try tagged-union format
	var tagged []sseReasoningSummary
	if err := json.Unmarshal(data, &tagged); err == nil {
		result := make([]string, 0, len(tagged))
		for _, s := range tagged {
			result = append(result, s.Text)
		}
		*r = result
		return nil
	}
	*r = nil
	return nil
}

type responsesUsage struct {
	InputTokens         int                           `json:"input_tokens"`
	InputTokensDetails  *responsesInputTokensDetails  `json:"input_tokens_details,omitempty"`
	OutputTokens        int                           `json:"output_tokens"`
	OutputTokensDetails *responsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
	TotalTokens         int                           `json:"total_tokens"`
}

type responsesInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type responsesOutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

type responsesError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Param   string `json:"param"`
	Code    string `json:"code"`
}

// SSE event types for streaming responses
type sseEvent struct {
	Type         string          `json:"type"`
	Delta        string          `json:"delta,omitempty"`
	Item         *sseOutputItem  `json:"item,omitempty"`
	Response     json.RawMessage `json:"response,omitempty"`
	SummaryIndex *int            `json:"summary_index,omitempty"`
	ContentIndex *int            `json:"content_index,omitempty"`
}

// sseOutputItem represents an item in SSE output_item.added/done events.
type sseOutputItem struct {
	Type             string                `json:"type"`
	ID               string                `json:"id,omitempty"`
	Role             string                `json:"role,omitempty"`
	CallID           string                `json:"call_id,omitempty"`
	Name             string                `json:"name,omitempty"`
	Arguments        string                `json:"arguments,omitempty"`
	Content          json.RawMessage       `json:"content,omitempty"`
	Summary          []sseReasoningSummary `json:"summary,omitempty"`
	EncryptedContent string                `json:"encrypted_content,omitempty"`
}

// sseReasoningSummary is the tagged-union format: {"type": "summary_text", "text": "..."}
type sseReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// sseReasoningContent is: {"type": "reasoning_text", "text": "..."}
type sseReasoningContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseSSEStreamWithCallbacks reads SSE events and calls onText for each text delta
// and onThinking for each reasoning summary delta.
// Returns the final accumulated response.
func parseSSEStreamWithCallbacks(body io.Reader, onText func(string), onThinking func(string)) (*responsesResponse, error) {
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var response responsesResponse
	var textContent strings.Builder
	pendingCalls := make(map[string]*responsesOutputItem)     // call_id -> item
	pendingReasoning := make(map[string]*responsesOutputItem) // id -> item
	var activeReasoningID string                              // tracks the current reasoning item for delta events
	var reasoningSummary strings.Builder                      // accumulates summary deltas
	var reasoningContent strings.Builder                      // accumulates raw content deltas

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		if jsonData == "[DONE]" {
			continue
		}

		var event sseEvent
		if err := json.Unmarshal([]byte(jsonData), &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_text.delta":
			textContent.WriteString(event.Delta)
			if onText != nil {
				onText(event.Delta)
			}

		case "response.output_item.added":
			if event.Item == nil {
				continue
			}
			switch event.Item.Type {
			case "function_call":
				if event.Item.CallID != "" {
					pendingCalls[event.Item.CallID] = &responsesOutputItem{
						Type:   "function_call",
						ID:     event.Item.ID,
						CallID: event.Item.CallID,
						Name:   event.Item.Name,
					}
				}
			case "reasoning":
				if event.Item.ID != "" {
					// Flush any previous reasoning item
					flushReasoningDeltas(activeReasoningID, pendingReasoning, &reasoningSummary, &reasoningContent)
					activeReasoningID = event.Item.ID
					var summaries []string
					for _, s := range event.Item.Summary {
						summaries = append(summaries, s.Text)
					}
					pendingReasoning[event.Item.ID] = &responsesOutputItem{
						Type:    "reasoning",
						ID:      event.Item.ID,
						Summary: summaries,
					}
				}
			}

		case "response.reasoning_summary_part.added":
			// A new summary section is starting; nothing to accumulate yet.

		case "response.reasoning_summary_text.delta":
			if event.Delta != "" {
				reasoningSummary.WriteString(event.Delta)
				if onThinking != nil {
					onThinking(event.Delta)
				}
			}

		case "response.reasoning_text.delta":
			if event.Delta != "" {
				reasoningContent.WriteString(event.Delta)
			}

		case "response.output_item.done":
			if event.Item == nil {
				continue
			}
			switch event.Item.Type {
			case "reasoning":
				// Finalized reasoning item — extract summary and content from the done payload.
				var summaries []string
				for _, s := range event.Item.Summary {
					if s.Text != "" {
						summaries = append(summaries, s.Text)
					}
				}
				var contentTexts []string
				if len(event.Item.Content) > 0 {
					var items []sseReasoningContent
					if err := json.Unmarshal(event.Item.Content, &items); err == nil {
						for _, c := range items {
							if c.Text != "" {
								contentTexts = append(contentTexts, c.Text)
							}
						}
					}
				}
				item := &responsesOutputItem{
					Type:    "reasoning",
					ID:      event.Item.ID,
					Summary: summaries,
				}
				if len(contentTexts) > 0 {
					item.ReasoningContent = contentTexts
				}
				pendingReasoning[event.Item.ID] = item
				// Reset delta accumulators for this item
				if activeReasoningID == event.Item.ID {
					reasoningSummary.Reset()
					reasoningContent.Reset()
				}
			case "function_call":
				if event.Item.CallID != "" {
					pendingCalls[event.Item.CallID] = &responsesOutputItem{
						Type:      "function_call",
						ID:        event.Item.ID,
						CallID:    event.Item.CallID,
						Name:      event.Item.Name,
						Arguments: event.Item.Arguments,
					}
				}
			}

		case "response.function_call_arguments.delta":
			if event.Item != nil && event.Item.CallID != "" {
				if call, ok := pendingCalls[event.Item.CallID]; ok {
					call.Arguments += event.Delta
				}
			}

		case "response.completed":
			var completedResp struct {
				Response responsesResponse `json:"response"`
			}
			if err := json.Unmarshal([]byte(jsonData), &completedResp); err == nil {
				response = completedResp.Response
			}

		case "response.failed":
			var failedResp struct {
				Response struct {
					Error *responsesError `json:"error"`
				} `json:"response"`
			}
			if err := json.Unmarshal([]byte(jsonData), &failedResp); err == nil && failedResp.Response.Error != nil {
				e := failedResp.Response.Error
				return nil, fmt.Errorf("API error (%s): %s", e.Code, e.Message)
			}
			return nil, fmt.Errorf("response.failed event received")

		case "response.incomplete":
			return nil, fmt.Errorf("incomplete response returned")
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading SSE stream: %w", err)
	}

	// Flush any remaining reasoning deltas
	flushReasoningDeltas(activeReasoningID, pendingReasoning, &reasoningSummary, &reasoningContent)

	if len(response.Output) == 0 {
		// Build response from accumulated state when response.completed didn't provide output
		if textContent.Len() > 0 {
			response.Output = append(response.Output, responsesOutputItem{
				Type:    "message",
				Role:    "assistant",
				Content: []responsesContent{{Type: "output_text", Text: textContent.String()}},
			})
		}
		for _, reasoning := range pendingReasoning {
			response.Output = append(response.Output, *reasoning)
		}
		for _, call := range pendingCalls {
			response.Output = append(response.Output, *call)
		}
	} else {
		// Merge delta-accumulated reasoning content into response.completed output.
		// The completed response has summaries from JSON but not raw content (json:"-").
		for i := range response.Output {
			if response.Output[i].Type != "reasoning" {
				continue
			}
			if pr, ok := pendingReasoning[response.Output[i].ID]; ok && len(pr.ReasoningContent) > 0 {
				response.Output[i].ReasoningContent = pr.ReasoningContent
			}
		}
	}

	return &response, nil
}

// flushReasoningDeltas merges accumulated summary/content deltas into the pending reasoning item.
func flushReasoningDeltas(id string, pending map[string]*responsesOutputItem, summary, content *strings.Builder) {
	if id == "" {
		return
	}
	item, ok := pending[id]
	if !ok {
		return
	}
	if summary.Len() > 0 {
		item.Summary = append(item.Summary, summary.String())
		summary.Reset()
	}
	if content.Len() > 0 {
		item.ReasoningContent = append(item.ReasoningContent, content.String())
		content.Reset()
	}
}

// fromLLMMessage converts llm.Message to Responses API input items
func fromLLMMessage(msg llm.Message) []responsesInputItem {
	var items []responsesInputItem

	// Separate tool results from regular content
	var regularContent []llm.Content
	var toolResults []llm.Content

	for _, c := range msg.Content {
		if c.Type == llm.ContentTypeToolResult {
			toolResults = append(toolResults, c)
		} else {
			regularContent = append(regularContent, c)
		}
	}

	// Process tool results first
	for _, tr := range toolResults {
		var texts []string
		for _, result := range tr.ToolResult {
			if strings.TrimSpace(result.Text) != "" {
				texts = append(texts, result.Text)
			}
		}
		toolResultContent := strings.Join(texts, "\n")

		if tr.ToolError {
			if toolResultContent != "" {
				toolResultContent = "error: " + toolResultContent
			} else {
				toolResultContent = "error: tool execution failed"
			}
		}

		items = append(items, responsesInputItem{
			Type:   "function_call_output",
			CallID: tr.ToolUseID,
			Output: cmp.Or(toolResultContent, " "),
		})
	}

	// Process regular content
	if len(regularContent) > 0 {
		var messageContent []responsesContent
		var functionCalls []responsesInputItem

		for _, c := range regularContent {
			switch c.Type {
			case llm.ContentTypeText:
				if c.Text != "" {
					contentType := "input_text"
					if msg.Role == llm.MessageRoleAssistant {
						contentType = "output_text"
					}
					messageContent = append(messageContent, responsesContent{
						Type: contentType,
						Text: c.Text,
					})
				}
			case llm.ContentTypeToolUse:
				args := string(c.ToolInput)
				if args == "" {
					args = "{}"
				}
				functionCalls = append(functionCalls, responsesInputItem{
					Type:      "function_call",
					CallID:    c.ID,
					Name:      c.ToolName,
					Arguments: args,
				})
			}
		}

		if len(messageContent) > 0 {
			role := "user"
			if msg.Role == llm.MessageRoleAssistant {
				role = "assistant"
			}
			items = append(items, responsesInputItem{
				Type:    "message",
				Role:    role,
				Content: messageContent,
			})
		}

		items = append(items, functionCalls...)
	}

	return items
}

// fromLLMTool converts llm.Tool to Responses API tool format
func fromLLMTool(t *llm.Tool) responsesTool {
	return responsesTool{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.InputSchema,
	}
}

// fromLLMSystem converts llm.SystemContent to Responses API input items
func fromLLMSystem(systemContent []llm.SystemContent) []responsesInputItem {
	if len(systemContent) == 0 {
		return nil
	}

	var systemText string
	for i, content := range systemContent {
		if i > 0 && systemText != "" && content.Text != "" {
			systemText += "\n"
		}
		systemText += content.Text
	}

	if systemText == "" {
		return nil
	}

	return []responsesInputItem{
		{
			Type: "message",
			Role: "user",
			Content: []responsesContent{
				{
					Type: "input_text",
					Text: systemText,
				},
			},
		},
	}
}

// fromLLMToolChoice converts llm.ToolChoice to Responses API format
func fromLLMToolChoice(tc *llm.ToolChoice) any {
	if tc == nil {
		return nil
	}
	if tc.Name != "" {
		return map[string]any{
			"type": "function",
			"name": tc.Name,
		}
	}
	switch tc.Type {
	case llm.ToolChoiceTypeAuto:
		return "auto"
	case llm.ToolChoiceTypeAny:
		return "required"
	case llm.ToolChoiceTypeNone:
		return "none"
	}
	return nil
}

// toLLMResponse converts Responses API response to llm.Response.
// fullThinking is optional streamed reasoning text to preserve even when provider summary is empty.
func (s *Service) toLLMResponse(resp *responsesResponse, headers http.Header, fullThinking string) *llm.Response {
	if len(resp.Output) == 0 {
		return &llm.Response{
			ID:    resp.ID,
			Model: resp.Model,
			Role:  llm.MessageRoleAssistant,
			Usage: s.toLLMUsage(resp.Usage, headers),
		}
	}

	var contents []llm.Content
	var stopReason llm.StopReason = llm.StopReasonStopSequence

	haveThinkingContent := false
	haveRedactedThinking := false
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Text != "" {
					contents = append(contents, llm.Content{
						Type: llm.ContentTypeText,
						Text: c.Text,
					})
				}
			}
		case "reasoning":
			summaryText := strings.Join(item.Summary, "\n")
			contentText := strings.Join(item.ReasoningContent, "\n")
			if summaryText != "" || contentText != "" {
				c := llm.Content{Type: llm.ContentTypeThinking}
				if contentText != "" {
					c.Thinking = contentText
					c.ThinkingSummary = summaryText
				} else {
					c.Thinking = summaryText
				}
				contents = append(contents, c)
				haveThinkingContent = true
			} else {
				contents = append(contents, llm.Content{Type: llm.ContentTypeRedactedThinking})
				haveRedactedThinking = true
			}
		case "function_call":
			contents = append(contents, llm.Content{
				ID:        item.CallID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  item.Name,
				ToolInput: json.RawMessage(item.Arguments),
			})
			stopReason = llm.StopReasonToolUse
		}
	}

	if fullThinking != "" {
		if !haveThinkingContent {
			contents = append([]llm.Content{{Type: llm.ContentTypeThinking, Thinking: fullThinking}}, contents...)
			haveThinkingContent = true
		}
		if haveRedactedThinking {
			filtered := contents[:0]
			for _, c := range contents {
				if c.Type == llm.ContentTypeRedactedThinking {
					continue
				}
				filtered = append(filtered, c)
			}
			contents = filtered
		}
	}

	if len(contents) == 0 {
		contents = append(contents, llm.Content{
			Type: llm.ContentTypeText,
			Text: "",
		})
	}

	return &llm.Response{
		ID:         resp.ID,
		Model:      resp.Model,
		Role:       llm.MessageRoleAssistant,
		Content:    contents,
		StopReason: stopReason,
		Usage:      s.toLLMUsage(resp.Usage, headers),
	}
}

// toLLMUsage converts Responses API usage to llm.Usage
func (s *Service) toLLMUsage(usage responsesUsage, headers http.Header) llm.Usage {
	in := uint64(usage.InputTokens)
	var inc uint64
	if usage.InputTokensDetails != nil {
		inc = uint64(usage.InputTokensDetails.CachedTokens)
	}
	out := uint64(usage.OutputTokens)
	reasoning := uint64(0)
	if usage.OutputTokensDetails != nil {
		reasoning = uint64(usage.OutputTokensDetails.ReasoningTokens)
	}
	u := llm.Usage{
		InputTokens:              in,
		CacheReadInputTokens:     inc,
		CacheCreationInputTokens: in,
		OutputTokens:             out,
		ReasoningTokens:          reasoning,
	}
	u.CostUSD = llm.CostUSDFromResponse(headers)
	return u
}

// TokenContextWindow returns the maximum token context window size for this service
func (s *Service) TokenContextWindow() int {
	switch s.Model {
	case "gpt-5.3-codex", "gpt-5.3-codex-thinking-low", "gpt-5.3-codex-thinking-medium", "gpt-5.3-codex-thinking-high":
		return 288000
	case "gpt-5.2-codex", "gpt-5.2-codex-thinking-low", "gpt-5.2-codex-thinking-medium", "gpt-5.2-codex-thinking-high":
		return 272000
	case "gpt-5.3", "gpt-5.2", "gpt-5.1":
		return 256000
	default:
		return 256000
	}
}

// MaxImageDimension returns the maximum allowed image dimension.
func (s *Service) MaxImageDimension() int {
	return 0 // No known limit
}

// UseSimplifiedPatch returns whether this service uses simplified patch format.
func (s *Service) UseSimplifiedPatch() bool {
	return false
}

// ConfigDetails returns configuration information for logging
func (s *Service) ConfigDetails() map[string]string {
	return map[string]string{
		"base_url":       ChatGPTAPIURL,
		"model_name":     s.Model,
		"full_url":       ChatGPTAPIURL + "/responses",
		"has_oauth":      fmt.Sprintf("%v", s.AccessToken != ""),
		"has_account_id": fmt.Sprintf("%v", s.AccountID != ""),
	}
}

// Do sends a request to OpenAI using the Responses API with ChatGPT OAuth.
func (s *Service) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	// Extract system prompt as instructions (required by ChatGPT API)
	var instructions string
	if len(ir.System) > 0 {
		var systemParts []string
		for _, sys := range ir.System {
			if sys.Text != "" {
				systemParts = append(systemParts, sys.Text)
			}
		}
		instructions = strings.Join(systemParts, "\n\n")
	}

	// Add regular messages as input
	var allInput []responsesInputItem
	for _, msg := range ir.Messages {
		items := fromLLMMessage(msg)
		allInput = append(allInput, items...)
	}

	// Convert tools
	var tools []responsesTool
	for _, t := range ir.Tools {
		tools = append(tools, fromLLMTool(t))
	}

	// Create the request
	req := responsesRequest{
		Model:        model,
		Input:        allInput,
		Instructions: instructions,
		Tools:        tools,
		Stream:       true,  // Required for ChatGPT API
		Store:        false, // Required for ChatGPT API
	}

	// Add reasoning if thinking is enabled
	if s.ThinkingLevel != llm.ThinkingLevelOff {
		effort := s.ThinkingLevel.ThinkingEffort()
		if effort != "" {
			req.Reasoning = &responsesReasoning{Effort: effort, Summary: "detailed"}
			req.Include = []string{"reasoning.encrypted_content"}
		}
	}

	// Add tool choice if specified
	if ir.ToolChoice != nil {
		req.ToolChoice = fromLLMToolChoice(ir.ToolChoice)
	}

	fullURL := ChatGPTAPIURL + "/responses"

	// Marshal the request
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Dump request if enabled
	if s.DumpLLM {
		if reqJSONPretty, err := json.MarshalIndent(req, "", "  "); err == nil {
			if err := llm.DumpToFile("request", fullURL, reqJSONPretty); err != nil {
				slog.WarnContext(ctx, "failed to dump codex request to file", "error", err)
			}
		}
	}

	// Retry mechanism
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}

	var errs error
	for attempts := 0; ; attempts++ {
		if attempts > 10 {
			return nil, fmt.Errorf("codex request failed after %d attempts (url=%s, model=%s): %w", attempts, fullURL, model, errs)
		}
		if attempts > 0 {
			sleep := backoff[min(attempts, len(backoff)-1)] + time.Duration(rand.Int64N(int64(time.Second)))
			slog.WarnContext(ctx, "codex request sleep before retry", "sleep", sleep, "attempts", attempts)
			time.Sleep(sleep)
		}

		// Create HTTP request
		httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(reqJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+s.AccessToken)
		if s.AccountID != "" {
			httpReq.Header.Set("ChatGPT-Account-ID", s.AccountID)
		}

		// Send request
		httpResp, err := httpc.Do(httpReq)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("attempt %d: %w", attempts+1, err))
			continue
		}

		// Handle non-200 responses (need to read body for error message)
		if httpResp.StatusCode != http.StatusOK {
			body, err := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to read error response body: %w", err)
			}

			var apiErr responsesError
			if jsonErr := json.Unmarshal(body, &struct {
				Error *responsesError `json:"error"`
			}{Error: &apiErr}); jsonErr == nil && apiErr.Message != "" {
				switch {
				case httpResp.StatusCode >= 500:
					slog.WarnContext(ctx, "codex_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model)
					errs = errors.Join(errs, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model, apiErr.Message))
					continue

				case httpResp.StatusCode == 429:
					slog.WarnContext(ctx, "codex_request_rate_limited", "error", apiErr.Message, "url", fullURL, "model", model)
					errs = errors.Join(errs, fmt.Errorf("status %d (rate limited, url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model, apiErr.Message))
					continue

				case httpResp.StatusCode == 401:
					// Auth error - don't retry, token might be expired
					return nil, fmt.Errorf("authentication failed (token may be expired): %s", apiErr.Message)

				case httpResp.StatusCode >= 400 && httpResp.StatusCode < 500:
					slog.WarnContext(ctx, "codex_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model)
					return nil, errors.Join(errs, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model, apiErr.Message))
				}
			}

			// Check for detail field (used by ChatGPT API)
			var detailErr struct {
				Detail string `json:"detail"`
			}
			if jsonErr := json.Unmarshal(body, &detailErr); jsonErr == nil && detailErr.Detail != "" {
				return nil, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model, detailErr.Detail)
			}

			slog.WarnContext(ctx, "codex_request_failed", "status_code", httpResp.StatusCode, "url", fullURL, "model", model, "body", string(body))
			return nil, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model, string(body))
		}

		// Parse SSE streaming response
		resp, err := parseSSEStreamWithCallbacks(httpResp.Body, nil, nil)
		httpResp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to parse SSE stream: %w", err)
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("response contains error: %s", resp.Error.Message)
		}

		// Dump response if enabled
		if s.DumpLLM {
			if respJSON, err := json.MarshalIndent(resp, "", "  "); err == nil {
				if err := llm.DumpToFile("response", "", respJSON); err != nil {
					slog.WarnContext(ctx, "failed to dump codex response to file", "error", err)
				}
			}
		}

		return s.toLLMResponse(resp, httpResp.Header, ""), nil
	}
}

// DoStream sends a request and streams text responses via the callback.
// Implements llm.StreamingService interface.
func (s *Service) DoStream(ctx context.Context, ir *llm.Request, onText func(string)) (*llm.Response, error) {
	return s.DoStreamWithThinking(ctx, ir, onText, nil)
}

// DoStreamWithThinking sends a request and streams both text and reasoning/thinking.
func (s *Service) DoStreamWithThinking(ctx context.Context, ir *llm.Request, onText func(string), onThinking func(string)) (*llm.Response, error) {
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	var thinkingStream strings.Builder
	streamThinking := func(delta string) {
		if delta == "" {
			return
		}
		thinkingStream.WriteString(delta)
		if onThinking != nil {
			onThinking(delta)
		}
	}

	// Extract system prompt as instructions (required by ChatGPT API)
	var instructions string
	if len(ir.System) > 0 {
		var systemParts []string
		for _, sys := range ir.System {
			if sys.Text != "" {
				systemParts = append(systemParts, sys.Text)
			}
		}
		instructions = strings.Join(systemParts, "\n\n")
	}

	// Add regular messages as input
	var allInput []responsesInputItem
	for _, msg := range ir.Messages {
		items := fromLLMMessage(msg)
		allInput = append(allInput, items...)
	}

	// Convert tools
	var tools []responsesTool
	for _, t := range ir.Tools {
		tools = append(tools, fromLLMTool(t))
	}

	// Create the request
	req := responsesRequest{
		Model:        model,
		Input:        allInput,
		Instructions: instructions,
		Tools:        tools,
		Stream:       true,
		Store:        false,
	}

	// Add reasoning if thinking is enabled
	if s.ThinkingLevel != llm.ThinkingLevelOff {
		effort := s.ThinkingLevel.ThinkingEffort()
		if effort != "" {
			req.Reasoning = &responsesReasoning{Effort: effort, Summary: "detailed"}
			req.Include = []string{"reasoning.encrypted_content"}
		}
	}

	// Add tool choice if specified
	if ir.ToolChoice != nil {
		req.ToolChoice = fromLLMToolChoice(ir.ToolChoice)
	}

	fullURL := ChatGPTAPIURL + "/responses"

	// Marshal the request
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(reqJSON))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Authorization", "Bearer "+s.AccessToken)
	if s.AccountID != "" {
		httpReq.Header.Set("ChatGPT-Account-ID", s.AccountID)
	}

	// Send request
	httpResp, err := httpc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer httpResp.Body.Close()

	// Handle non-200 responses
	if httpResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(httpResp.Body)
		return nil, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model, string(body))
	}

	// Parse SSE stream with callbacks
	resp, err := parseSSEStreamWithCallbacks(httpResp.Body, onText, streamThinking)
	if err != nil {
		return nil, fmt.Errorf("failed to parse SSE stream: %w", err)
	}

	if thinkingStream.Len() > 0 {
		hasThinking := false
		for i := range resp.Output {
			if resp.Output[i].Type != "reasoning" {
				continue
			}
			hasThinking = true
			if len(resp.Output[i].Summary) == 0 {
				resp.Output[i].Summary = []string{thinkingStream.String()}
			}
			break
		}
		if !hasThinking {
			resp.Output = append(resp.Output, responsesOutputItem{
				Type:    "reasoning",
				Summary: []string{thinkingStream.String()},
			})
		}
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("response contains error: %s", resp.Error.Message)
	}

	return s.toLLMResponse(resp, httpResp.Header, thinkingStream.String()), nil
}

// Verify Service implements streaming interfaces
var _ llm.StreamingService = (*Service)(nil)
var _ llm.ThinkingStreamingService = (*Service)(nil)

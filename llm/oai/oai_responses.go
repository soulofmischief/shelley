package oai

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

// ResponsesService provides chat completions using the OpenAI Responses API.
// This API is required for models like gpt-5.1-codex.
// Fields should not be altered concurrently with calling any method on ResponsesService.
type ResponsesService struct {
	HTTPC         *http.Client      // defaults to http.DefaultClient if nil
	APIKey        string            // optional, if not set will try to load from env var
	Model         Model             // defaults to DefaultModel if zero value
	ModelURL      string            // optional, overrides Model.URL
	MaxTokens     int               // defaults to DefaultMaxTokens if zero
	Org           string            // optional - organization ID
	DumpLLM       bool              // whether to dump request/response text to files for debugging; defaults to false
	ThinkingLevel llm.ThinkingLevel // thinking level (ThinkingLevelOff disables reasoning)
}

var _ llm.Service = (*ResponsesService)(nil)

// Responses API request/response types

type responsesRequest struct {
	Model           string               `json:"model"`
	Input           []responsesInputItem `json:"input"`
	Tools           []responsesTool      `json:"tools,omitempty"`
	ToolChoice      any                  `json:"tool_choice,omitempty"`
	MaxOutputTokens int                  `json:"max_output_tokens,omitempty"`
	Reasoning       *responsesReasoning  `json:"reasoning,omitempty"`
	Include         []string             `json:"include,omitempty"`
	Stream          bool                 `json:"stream"`
	Store           bool                 `json:"store"`
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
	ReasoningContent []string           `json:"-"`                   // populated by SSE parser
}

// reasoningSummaries handles both plain ["text"] and tagged [{"type":"summary_text","text":"..."}] formats.
type reasoningSummaries []string

func (r *reasoningSummaries) UnmarshalJSON(data []byte) error {
	var plain []string
	if err := json.Unmarshal(data, &plain); err == nil {
		*r = plain
		return nil
	}
	var tagged []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
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

// fromLLMMessageResponses converts llm.Message to Responses API input items
func fromLLMMessageResponses(msg llm.Message) []responsesInputItem {
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

	// Process tool results first - they need to come before the assistant message
	for _, tr := range toolResults {
		// Collect all text from content objects
		var texts []string
		for _, result := range tr.ToolResult {
			if strings.TrimSpace(result.Text) != "" {
				texts = append(texts, result.Text)
			}
		}
		toolResultContent := strings.Join(texts, "\n")

		// Add error prefix if needed
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
				// Tool use becomes a function_call in the input
				functionCalls = append(functionCalls, responsesInputItem{
					Type:      "function_call",
					CallID:    c.ID,
					Name:      c.ToolName,
					Arguments: string(c.ToolInput),
				})
			}
		}

		// Add message if it has content
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

		// Add function calls
		items = append(items, functionCalls...)
	}

	return items
}

// fromLLMToolResponses converts llm.Tool to Responses API tool format
func fromLLMToolResponses(t *llm.Tool) responsesTool {
	return responsesTool{
		Type:        "function",
		Name:        t.Name,
		Description: t.Description,
		Parameters:  t.InputSchema,
	}
}

// fromLLMSystemResponses converts llm.SystemContent to Responses API input items
func fromLLMSystemResponses(systemContent []llm.SystemContent) []responsesInputItem {
	if len(systemContent) == 0 {
		return nil
	}

	// Combine all system content into a single system message
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

// toLLMResponseFromResponses converts Responses API response to llm.Response
func (s *ResponsesService) toLLMResponseFromResponses(resp *responsesResponse, headers http.Header) *llm.Response {
	if len(resp.Output) == 0 {
		return &llm.Response{
			ID:    resp.ID,
			Model: resp.Model,
			Role:  llm.MessageRoleAssistant,
			Usage: s.toLLMUsageFromResponses(resp.Usage, headers),
		}
	}

	// Process the output items
	var contents []llm.Content
	var stopReason llm.StopReason = llm.StopReasonStopSequence

	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			// Convert message content
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
			} else {
				contents = append(contents, llm.Content{Type: llm.ContentTypeRedactedThinking})
			}
		case "function_call":
			// Convert function call to tool use
			contents = append(contents, llm.Content{
				ID:        item.CallID,
				Type:      llm.ContentTypeToolUse,
				ToolName:  item.Name,
				ToolInput: json.RawMessage(item.Arguments),
			})
			stopReason = llm.StopReasonToolUse
		}
	}

	// If no content, add empty text content
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
		Usage:      s.toLLMUsageFromResponses(resp.Usage, headers),
	}
}

// toLLMUsageFromResponses converts Responses API usage to llm.Usage
// toLLMUsageFromResponses converts Responses API usage to llm.Usage.
//
// OpenAI's Responses API reports input_tokens as the total input (including cached),
// with input_tokens_details.cached_tokens as the cached subset.
// Our Usage struct follows Anthropic's convention where InputTokens is the non-cached
// portion and TotalInputTokens() = InputTokens + CacheCreationInputTokens + CacheReadInputTokens.
// So we map: InputTokens = total - cached, CacheReadInputTokens = cached, CacheCreationInputTokens = 0.
func (s *ResponsesService) toLLMUsageFromResponses(usage responsesUsage, headers http.Header) llm.Usage {
	totalIn := uint64(usage.InputTokens)
	var cached uint64
	if usage.InputTokensDetails != nil {
		cached = uint64(usage.InputTokensDetails.CachedTokens)
	}
	out := uint64(usage.OutputTokens)
	var reasoning uint64
	if usage.OutputTokensDetails != nil {
		reasoning = uint64(usage.OutputTokensDetails.ReasoningTokens)
	}
	u := llm.Usage{
		InputTokens:          totalIn - cached,
		CacheReadInputTokens: cached,
		OutputTokens:         out,
		ReasoningTokens:      reasoning,
	}
	u.CostUSD = llm.CostUSDFromResponse(headers)
	return u
}

// TokenContextWindow returns the maximum token context window size for this service
func (s *ResponsesService) TokenContextWindow() int {
	model := cmp.Or(s.Model, DefaultModel)

	// Use the same context window logic as the regular service
	switch model.ModelName {
	case "gpt-5.3-codex":
		return 288000 // 288k for gpt-5.3-codex
	case "gpt-5.2-codex":
		return 272000 // 272k for gpt-5.2-codex
	case "gpt-5.1-codex":
		return 256000 // 256k for gpt-5.1-codex
	case "gpt-4.1-2025-04-14", "gpt-4.1-mini-2025-04-14", "gpt-4.1-nano-2025-04-14":
		return 200000
	case "gpt-4o-2024-08-06", "gpt-4o-mini-2024-07-18":
		return 128000
	default:
		return 128000
	}
}

// MaxImageDimension returns the maximum allowed image dimension.
// TODO: determine actual OpenAI image dimension limits
func (s *ResponsesService) MaxImageDimension() int {
	return 0 // No known limit
}

// Do sends a request to OpenAI using the Responses API.
// buildRequest constructs a responsesRequest from an llm.Request.
func (s *ResponsesService) buildRequest(ir *llm.Request, model Model) responsesRequest {
	var allInput []responsesInputItem
	if len(ir.System) > 0 {
		allInput = append(allInput, fromLLMSystemResponses(ir.System)...)
	}
	for _, msg := range ir.Messages {
		allInput = append(allInput, fromLLMMessageResponses(msg)...)
	}
	var tools []responsesTool
	for _, t := range ir.Tools {
		tools = append(tools, fromLLMToolResponses(t))
	}
	req := responsesRequest{
		Model:           model.ModelName,
		Input:           allInput,
		Tools:           tools,
		MaxOutputTokens: cmp.Or(s.MaxTokens, DefaultMaxTokens),
	}
	if s.ThinkingLevel != llm.ThinkingLevelOff {
		effort := s.ThinkingLevel.ThinkingEffort()
		if effort != "" {
			req.Reasoning = &responsesReasoning{Effort: effort, Summary: "detailed"}
			req.Include = []string{"reasoning.encrypted_content"}
		}
	}
	if ir.ToolChoice != nil {
		req.ToolChoice = fromLLMToolChoice(ir.ToolChoice)
	}
	return req
}

// Do sends a non-streaming request to OpenAI using the Responses API.
func (s *ResponsesService) Do(ctx context.Context, ir *llm.Request) (*llm.Response, error) {
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	req := s.buildRequest(ir, model)

	// Construct the full URL
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	fullURL := baseURL + "/responses"

	// Marshal the request
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Dump request if enabled
	if s.DumpLLM {
		if reqJSONPretty, err := json.MarshalIndent(req, "", "  "); err == nil {
			if err := llm.DumpToFile("request", fullURL, reqJSONPretty); err != nil {
				slog.WarnContext(ctx, "failed to dump responses request to file", "error", err)
			}
		}
	}

	// Retry mechanism
	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}

	// retry loop
	var errs error // accumulated errors across all attempts
	for attempts := 0; ; attempts++ {
		if attempts > 10 {
			return nil, fmt.Errorf("responses request failed after %d attempts (url=%s, model=%s): %w", attempts, fullURL, model.ModelName, errs)
		}
		if attempts > 0 {
			sleep := backoff[min(attempts, len(backoff)-1)] + time.Duration(rand.Int64N(int64(time.Second)))
			slog.WarnContext(ctx, "responses request sleep before retry", "sleep", sleep, "attempts", attempts)
			time.Sleep(sleep)
		}

		// Create HTTP request
		httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(reqJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}

		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+s.APIKey)
		if s.Org != "" {
			httpReq.Header.Set("OpenAI-Organization", s.Org)
		}

		// Send request
		httpResp, err := httpc.Do(httpReq)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("attempt %d: %w", attempts+1, err))
			continue
		}
		defer httpResp.Body.Close()

		// Read response body
		body, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}

		// Handle non-200 responses
		if httpResp.StatusCode != http.StatusOK {
			var apiErr responsesError
			if jsonErr := json.Unmarshal(body, &struct {
				Error *responsesError `json:"error"`
			}{Error: &apiErr}); jsonErr == nil && apiErr.Message != "" {
				// We have a structured error
				switch {
				case httpResp.StatusCode >= 500:
					// Server error, retry
					slog.WarnContext(ctx, "responses_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName)
					errs = errors.Join(errs, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
					continue

				case httpResp.StatusCode == 429:
					// Rate limited, retry
					slog.WarnContext(ctx, "responses_request_rate_limited", "error", apiErr.Message, "url", fullURL, "model", model.ModelName)
					errs = errors.Join(errs, fmt.Errorf("status %d (rate limited, url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
					continue

				case httpResp.StatusCode >= 400 && httpResp.StatusCode < 500:
					// Client error, probably unrecoverable
					slog.WarnContext(ctx, "responses_request_failed", "error", apiErr.Message, "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName)
					return nil, errors.Join(errs, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model.ModelName, apiErr.Message))
				}
			}

			// No structured error, use the raw body
			slog.WarnContext(ctx, "responses_request_failed", "status_code", httpResp.StatusCode, "url", fullURL, "model", model.ModelName, "body", string(body))
			return nil, fmt.Errorf("status %d (url=%s, model=%s): %s", httpResp.StatusCode, fullURL, model.ModelName, string(body))
		}

		// Parse successful response
		var resp responsesResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return nil, fmt.Errorf("failed to unmarshal response: %w", err)
		}

		// Check for errors in the response
		if resp.Error != nil {
			return nil, fmt.Errorf("response contains error: %s", resp.Error.Message)
		}

		// Dump response if enabled
		if s.DumpLLM {
			if respJSON, err := json.MarshalIndent(resp, "", "  "); err == nil {
				if err := llm.DumpToFile("response", "", respJSON); err != nil {
					slog.WarnContext(ctx, "failed to dump responses response to file", "error", err)
				}
			}
		}

		return s.toLLMResponseFromResponses(&resp, httpResp.Header), nil
	}
}

// Verify ResponsesService implements streaming interfaces
var _ llm.StreamingService = (*ResponsesService)(nil)
var _ llm.ThinkingStreamingService = (*ResponsesService)(nil)

// DoStream sends a streaming request.
func (s *ResponsesService) DoStream(ctx context.Context, ir *llm.Request, onText func(string)) (*llm.Response, error) {
	return s.DoStreamWithThinking(ctx, ir, onText, nil)
}

// DoStreamWithThinking sends a streaming request with reasoning callbacks.
func (s *ResponsesService) DoStreamWithThinking(ctx context.Context, ir *llm.Request, onText func(string), onThinking func(string)) (*llm.Response, error) {
	httpc := cmp.Or(s.HTTPC, http.DefaultClient)
	model := cmp.Or(s.Model, DefaultModel)

	req := s.buildRequest(ir, model)
	req.Stream = true

	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	fullURL := baseURL + "/responses"

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if s.DumpLLM {
		if reqJSONPretty, err := json.MarshalIndent(req, "", "  "); err == nil {
			llm.DumpToFile("request", fullURL, reqJSONPretty)
		}
	}

	backoff := []time.Duration{1 * time.Second, 2 * time.Second, 5 * time.Second, 10 * time.Second, 15 * time.Second}
	var errs error
	for attempts := 0; ; attempts++ {
		if attempts > 10 {
			return nil, fmt.Errorf("responses request failed after %d attempts: %w", attempts, errs)
		}
		if attempts > 0 {
			sleep := backoff[min(attempts, len(backoff)-1)] + time.Duration(rand.Int64N(int64(time.Second)))
			time.Sleep(sleep)
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(reqJSON))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "text/event-stream")
		httpReq.Header.Set("Authorization", "Bearer "+s.APIKey)
		if s.Org != "" {
			httpReq.Header.Set("OpenAI-Organization", s.Org)
		}

		httpResp, err := httpc.Do(httpReq)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("attempt %d: %w", attempts+1, err))
			continue
		}

		if httpResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(httpResp.Body)
			httpResp.Body.Close()
			if httpResp.StatusCode >= 500 || httpResp.StatusCode == 429 {
				errs = errors.Join(errs, fmt.Errorf("status %d: %s", httpResp.StatusCode, string(body)))
				continue
			}
			return nil, fmt.Errorf("status %d: %s", httpResp.StatusCode, string(body))
		}

		var thinkingStream strings.Builder
		streamThinking := onThinking
		if streamThinking == nil {
			streamThinking = func(s string) { thinkingStream.WriteString(s) }
		} else {
			orig := streamThinking
			streamThinking = func(s string) {
				thinkingStream.WriteString(s)
				orig(s)
			}
		}

		resp, err := parseSSEStream(httpResp.Body, onText, streamThinking)
		httpResp.Body.Close()
		if err != nil {
			return nil, err
		}

		if resp.Error != nil {
			return nil, fmt.Errorf("API error: %s", resp.Error.Message)
		}

		result := s.toLLMResponseFromResponses(resp, httpResp.Header)
		if thinkingStream.Len() > 0 {
			hasThinking := false
			for _, c := range result.Content {
				if c.Type == llm.ContentTypeThinking {
					hasThinking = true
					break
				}
			}
			if !hasThinking {
				result.Content = append([]llm.Content{{Type: llm.ContentTypeThinking, Thinking: thinkingStream.String()}}, result.Content...)
			}
			filtered := result.Content[:0]
			for _, c := range result.Content {
				if c.Type != llm.ContentTypeRedactedThinking {
					filtered = append(filtered, c)
				}
			}
			result.Content = filtered
		}
		return result, nil
	}
}

func (s *ResponsesService) UseSimplifiedPatch() bool {
	return s.Model.UseSimplifiedPatch
}

// ConfigDetails returns configuration information for logging
func (s *ResponsesService) ConfigDetails() map[string]string {
	model := cmp.Or(s.Model, DefaultModel)
	baseURL := cmp.Or(s.ModelURL, model.URL, OpenAIURL)
	return map[string]string{
		"base_url":        baseURL,
		"model_name":      model.ModelName,
		"full_url":        baseURL + "/responses",
		"api_key_env":     model.APIKeyEnv,
		"has_api_key_set": fmt.Sprintf("%v", s.APIKey != ""),
	}
}

// SSE event types for Responses API streaming
type sseEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
}

type sseItem struct {
	Type      string                `json:"type"`
	ID        string                `json:"id,omitempty"`
	CallID    string                `json:"call_id,omitempty"`
	Name      string                `json:"name,omitempty"`
	Arguments string                `json:"arguments,omitempty"`
	Summary   []sseReasoningSummary `json:"summary,omitempty"`
	Content   json.RawMessage       `json:"content,omitempty"`
}

type sseReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type sseReasoningContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// parseSSEStream parses Responses API SSE events.
func parseSSEStream(body io.Reader, onText func(string), onThinking func(string)) (*responsesResponse, error) {
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	var response responsesResponse
	var textContent strings.Builder
	pendingCalls := make(map[string]*responsesOutputItem)
	pendingReasoning := make(map[string]*responsesOutputItem)
	var activeReasoningID string
	var reasoningSummary, reasoningContent strings.Builder

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

		case "response.output_item.added", "response.output_item.done":
			if len(event.Item) == 0 {
				continue
			}
			var item sseItem
			if err := json.Unmarshal(event.Item, &item); err != nil {
				continue
			}
			switch item.Type {
			case "function_call":
				if item.CallID != "" {
					pendingCalls[item.CallID] = &responsesOutputItem{
						Type: "function_call", ID: item.ID,
						CallID: item.CallID, Name: item.Name, Arguments: item.Arguments,
					}
				}
			case "reasoning":
				if item.ID == "" {
					continue
				}
				if event.Type == "response.output_item.added" {
					flushReasoningDeltas(activeReasoningID, pendingReasoning, &reasoningSummary, &reasoningContent)
					activeReasoningID = item.ID
					var summaries []string
					for _, s := range item.Summary {
						summaries = append(summaries, s.Text)
					}
					pendingReasoning[item.ID] = &responsesOutputItem{Type: "reasoning", ID: item.ID, Summary: summaries}
				} else {
					// output_item.done: finalized
					var summaries []string
					for _, s := range item.Summary {
						if s.Text != "" {
							summaries = append(summaries, s.Text)
						}
					}
					oi := &responsesOutputItem{Type: "reasoning", ID: item.ID, Summary: summaries}
					if len(item.Content) > 0 {
						var items []sseReasoningContent
						if err := json.Unmarshal(item.Content, &items); err == nil {
							for _, c := range items {
								if c.Text != "" {
									oi.ReasoningContent = append(oi.ReasoningContent, c.Text)
								}
							}
						}
					}
					pendingReasoning[item.ID] = oi
					if activeReasoningID == item.ID {
						reasoningSummary.Reset()
						reasoningContent.Reset()
					}
				}
			}

		case "response.reasoning_summary_part.added":
			// section marker, no action needed

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

		case "response.function_call_arguments.delta":
			if len(event.Item) > 0 {
				var item sseItem
				if err := json.Unmarshal(event.Item, &item); err == nil && item.CallID != "" {
					if call, ok := pendingCalls[item.CallID]; ok {
						call.Arguments += event.Delta
					}
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

	flushReasoningDeltas(activeReasoningID, pendingReasoning, &reasoningSummary, &reasoningContent)

	if len(response.Output) == 0 {
		if textContent.Len() > 0 {
			response.Output = append(response.Output, responsesOutputItem{
				Type: "message", Role: "assistant",
				Content: []responsesContent{{Type: "output_text", Text: textContent.String()}},
			})
		}
		for _, r := range pendingReasoning {
			response.Output = append(response.Output, *r)
		}
		for _, c := range pendingCalls {
			response.Output = append(response.Output, *c)
		}
	} else {
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

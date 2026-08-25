package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type conversationStartedEvent struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	Slug           string `json:"slug,omitempty"`
}

func cmdChat(cc *clientConfig, args []string) error {
	fs := flag.NewFlagSet("client chat", flag.ContinueOnError)
	fs.SetOutput(cc.stderr)
	prompt := fs.String("p", "", "Message to send; use - to read stdin")
	conversationID := fs.String("c", "", "Conversation ID to continue")
	model := fs.String("model", "", "Model to use")
	cwd := fs.String("cwd", "", "Working directory for a new conversation")
	immediate := fs.Bool("immediate", false, "Return after starting the turn")
	ephemeral := fs.Bool("ephemeral", false, "Archive the conversation after the turn")
	disableNotifications := fs.Bool("disable-notifications", false, "Disable notifications for a new conversation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *immediate && *ephemeral {
		return fmt.Errorf("--immediate and --ephemeral cannot be used together")
	}
	if *disableNotifications && *conversationID != "" {
		return fmt.Errorf("--disable-notifications only applies to new conversations")
	}

	promptText, err := resolvePrompt(*prompt, fs.Args(), cc.input)
	if err != nil {
		return err
	}

	effectiveCWD := *cwd
	if effectiveCWD == "" && *conversationID == "" {
		effectiveCWD, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
	}

	body := map[string]any{"message": promptText}
	if *model != "" {
		body["model"] = *model
	}
	if effectiveCWD != "" {
		body["cwd"] = effectiveCWD
	}
	if *disableNotifications {
		body["conversation_options"] = map[string]any{"disable_notifications": true}
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		return err
	}
	endpoint := baseURL + "/api/conversations/new"
	if *conversationID != "" {
		endpoint = baseURL + "/api/conversation/" + *conversationID + "/chat"
	}
	request, err := cc.newRequest(http.MethodPost, endpoint, strings.NewReader(string(bodyBytes)))
	if err != nil {
		return fmt.Errorf("create chat request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return responseError("chat", response)
	}

	var responseBody struct {
		ConversationID string `json:"conversation_id"`
		Slug           string `json:"slug"`
	}
	if err := json.NewDecoder(response.Body).Decode(&responseBody); err != nil {
		return fmt.Errorf("decode chat response: %w", err)
	}
	if responseBody.ConversationID == "" {
		responseBody.ConversationID = *conversationID
	}
	if responseBody.ConversationID == "" {
		return fmt.Errorf("chat response did not include a conversation ID")
	}

	started := conversationStartedEvent{
		Type:           "conversation_started",
		ConversationID: responseBody.ConversationID,
		Slug:           responseBody.Slug,
	}
	if cc.output.jsonLines {
		if err := writeJSONLine(cc.output.writer, started); err != nil {
			return err
		}
	} else if *conversationID == "" {
		fmt.Fprintf(cc.stderr, "Conversation ID: %s\n", responseBody.ConversationID)
	}
	if *immediate {
		return nil
	}

	if err := streamConversation(cc, client, baseURL, responseBody.ConversationID, streamMode{
		onlyAfterLastUser: true,
		stopAtEndOfTurn:   true,
	}); err != nil {
		return err
	}
	if *ephemeral {
		return conversationAction(cc, client, baseURL, responseBody.ConversationID, "archive")
	}
	return nil
}

func resolvePrompt(flagPrompt string, args []string, stdin io.Reader) (string, error) {
	if flagPrompt != "" && len(args) > 0 {
		return "", fmt.Errorf("provide the message with either -p or positional arguments")
	}
	if flagPrompt == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		flagPrompt = string(data)
	}
	if flagPrompt == "" {
		flagPrompt = strings.Join(args, " ")
	}
	if strings.TrimSpace(flagPrompt) == "" {
		return "", fmt.Errorf("message required (-p PROMPT, -p -, or positional arguments)")
	}
	return flagPrompt, nil
}

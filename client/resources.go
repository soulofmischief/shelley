package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func cmdConversationAction(cc *clientConfig, args []string, action string) error {
	fs := flag.NewFlagSet("client "+action, flag.ContinueOnError)
	fs.SetOutput(cc.stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shelley client %s CONVERSATION_ID", action)
	}
	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		return err
	}
	return conversationAction(cc, client, baseURL, fs.Arg(0), action)
}

func conversationAction(cc *clientConfig, client *http.Client, baseURL, conversationID, action string) error {
	request, err := cc.newRequest(http.MethodPost, baseURL+"/api/conversation/"+conversationID+"/"+action, nil)
	if err != nil {
		return fmt.Errorf("create %s request: %w", action, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return responseError(action+" conversation", response)
	}
	if !cc.output.jsonLines {
		fmt.Fprintf(cc.stderr, "%s %s\n", strings.ToUpper(action[:1])+action[1:]+"d", conversationID)
	}
	return nil
}

func cmdModels(cc *clientConfig, args []string) error {
	fs := flag.NewFlagSet("client models", flag.ContinueOnError)
	fs.SetOutput(cc.stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("models does not accept positional arguments")
	}
	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		return err
	}
	request, err := cc.newRequest(http.MethodGet, baseURL+"/api/models", nil)
	if err != nil {
		return fmt.Errorf("create models request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError("list models", response)
	}
	var models []modelWire
	if err := json.NewDecoder(response.Body).Decode(&models); err != nil {
		return fmt.Errorf("decode models: %w", err)
	}
	for _, model := range models {
		if cc.output.jsonLines {
			if err := writeJSONLine(cc.output.writer, model); err != nil {
				return err
			}
			continue
		}
		status := cc.output.red("not ready")
		if model.Ready {
			status = cc.output.green("ready")
		}
		fmt.Fprintf(cc.output.writer, "%s  %s\n", cc.output.cyan(model.ID), status)
	}
	return nil
}

func responseError(operation string, response *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(response.Body, 8*1024))
	if err != nil {
		return fmt.Errorf("%s returned HTTP %d and its response could not be read: %w", operation, response.StatusCode, err)
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		return fmt.Errorf("%s returned HTTP %d", operation, response.StatusCode)
	}
	return fmt.Errorf("%s returned HTTP %d: %s", operation, response.StatusCode, message)
}

package client

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func cmdRead(cc *clientConfig, args []string) error {
	fs := flag.NewFlagSet("client read", flag.ContinueOnError)
	fs.SetOutput(cc.stderr)
	follow := fs.Bool("f", false, "Follow until the current agent turn finishes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: shelley client read [-f] CONVERSATION_ID")
	}

	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		return err
	}
	if *follow {
		return streamConversation(cc, client, baseURL, fs.Arg(0), streamMode{
			stopAtEndOfTurn: true,
			readMode:        true,
		})
	}
	return readSnapshot(cc, client, baseURL, fs.Arg(0))
}

func readSnapshot(cc *clientConfig, client *http.Client, baseURL, conversationID string) error {
	request, err := cc.newRequest(http.MethodGet, baseURL+"/api/conversation/"+conversationID, nil)
	if err != nil {
		return fmt.Errorf("create read request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError("read conversation", response)
	}

	var snapshot streamResponseWire
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		return fmt.Errorf("decode conversation: %w", err)
	}
	for _, message := range snapshot.Messages {
		if cc.output.jsonLines {
			if err := writeJSONLine(cc.output.writer, simplifyMessage(message)); err != nil {
				return err
			}
		} else {
			if err := cc.output.printReadMessage(message); err != nil {
				return err
			}
		}
	}
	return nil
}

func cmdList(cc *clientConfig, args []string) error {
	fs := flag.NewFlagSet("client list", flag.ContinueOnError)
	fs.SetOutput(cc.stderr)
	archived := fs.Bool("a", false, "List archived conversations")
	limit := fs.Int("limit", 20, "Maximum number of conversations")
	query := fs.String("q", "", "Search query")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("list does not accept positional arguments")
	}
	if *limit < 1 {
		return fmt.Errorf("--limit must be positive")
	}

	endpoint := "/api/conversations"
	if *archived {
		endpoint = "/api/conversations/archived"
	}
	values := url.Values{"limit": {fmt.Sprintf("%d", *limit)}}
	if *query != "" {
		values.Set("q", *query)
	}
	return fetchConversations(cc, endpoint+"?"+values.Encode())
}

func cmdSearch(cc *clientConfig, args []string) error {
	fs := flag.NewFlagSet("client search", flag.ContinueOnError)
	fs.SetOutput(cc.stderr)
	limit := fs.Int("limit", 20, "Maximum number of results")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: shelley client search [--limit N] QUERY")
	}
	if *limit < 1 {
		return fmt.Errorf("--limit must be positive")
	}
	values := url.Values{
		"q":              {strings.Join(fs.Args(), " ")},
		"search_content": {"true"},
		"limit":          {fmt.Sprintf("%d", *limit)},
	}
	return fetchConversations(cc, "/api/conversations?"+values.Encode())
}

func fetchConversations(cc *clientConfig, endpoint string) error {
	client, baseURL, err := cc.newHTTPClient()
	if err != nil {
		return err
	}
	request, err := cc.newRequest(http.MethodGet, baseURL+endpoint, nil)
	if err != nil {
		return fmt.Errorf("create conversations request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseError("list conversations", response)
	}

	var conversations []conversationWire
	if err := json.NewDecoder(response.Body).Decode(&conversations); err != nil {
		return fmt.Errorf("decode conversations: %w", err)
	}
	return cc.output.printConversations(conversations)
}

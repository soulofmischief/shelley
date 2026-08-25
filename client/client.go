// Package client implements the Shelley CLI client.
package client

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultSocketPath returns the default Unix socket path (~/.config/shelley/shelley.sock).
func DefaultSocketPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "shelley", "shelley.sock")
}

func defaultClientURL() string {
	return "unix://" + DefaultSocketPath()
}

func parseClientURL(rawURL string) (scheme, address string, err error) {
	if socketPath, ok := strings.CutPrefix(rawURL, "unix://"); ok {
		if socketPath == "" {
			return "", "", fmt.Errorf("unix:// URL must include a socket path")
		}
		return "unix", socketPath, nil
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return strings.SplitN(rawURL, "://", 2)[0], rawURL, nil
	}
	return "", "", fmt.Errorf("unsupported URL scheme: %s (use unix://, http://, or https://)", rawURL)
}

type multiFlag []string

func (f *multiFlag) String() string { return strings.Join(*f, ", ") }

func (f *multiFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

type clientConfig struct {
	serverURL string
	headers   map[string]string
	input     io.Reader
	output    outputConfig
	stderr    io.Writer
}

func (cc *clientConfig) newHTTPClient() (*http.Client, string, error) {
	scheme, address, err := parseClientURL(cc.serverURL)
	if err != nil {
		return nil, "", err
	}

	switch scheme {
	case "unix":
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", address)
			},
		}
		return &http.Client{Transport: transport}, "http://localhost", nil
	case "http", "https":
		return &http.Client{}, address, nil
	default:
		return nil, "", fmt.Errorf("unsupported scheme: %s", scheme)
	}
}

func (cc *clientConfig) newRequest(method, requestURL string, body io.Reader) (*http.Request, error) {
	request, err := http.NewRequest(method, requestURL, body)
	if err != nil {
		return nil, err
	}
	if method == http.MethodPost {
		request.Header.Set("X-Shelley-Request", "1")
	}
	for key, value := range cc.headers {
		request.Header.Set(key, value)
	}
	return request, nil
}

// Run is the entry point for "shelley client [args...]".
func Run(args []string) {
	if err := run(args, os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	fs.SetOutput(stderr)
	serverURL := fs.String("url", defaultClientURL(), "Server URL (unix:///path, http://host:port, https://host:port)")
	jsonLines := fs.Bool("json", false, "Output JSON lines for scripting")
	noColor := fs.Bool("no-color", false, "Disable colored output")
	var headers multiFlag
	fs.Var(&headers, "H", `Extra HTTP header ("Name: Value", repeatable)`)
	fs.Usage = func() { printUsage(stderr, fs) }
	if err := fs.Parse(args); err != nil {
		return err
	}

	parsedHeaders := make(map[string]string, len(headers))
	for _, header := range headers {
		parts := strings.SplitN(header, ":", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return fmt.Errorf("invalid header %q (expected \"Name: Value\")", header)
		}
		parsedHeaders[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}

	cc := &clientConfig{
		serverURL: *serverURL,
		headers:   parsedHeaders,
		input:     stdin,
		output: outputConfig{
			jsonLines: *jsonLines,
			color:     colorEnabled(*noColor, stdout),
			writer:    stdout,
		},
		stderr: stderr,
	}

	commandArgs := fs.Args()
	if len(commandArgs) == 0 {
		fs.Usage()
		return fmt.Errorf("command required")
	}

	switch commandArgs[0] {
	case "chat":
		return cmdChat(cc, commandArgs[1:])
	case "read":
		return cmdRead(cc, commandArgs[1:])
	case "list":
		return cmdList(cc, commandArgs[1:])
	case "search":
		return cmdSearch(cc, commandArgs[1:])
	case "tag":
		return cmdTag(cc, commandArgs[1:])
	case "tags":
		return cmdTags(cc, commandArgs[1:])
	case "archive":
		return cmdConversationAction(cc, commandArgs[1:], "archive")
	case "unarchive":
		return cmdConversationAction(cc, commandArgs[1:], "unarchive")
	case "delete":
		return cmdConversationAction(cc, commandArgs[1:], "delete")
	case "models":
		return cmdModels(cc, commandArgs[1:])
	case "help":
		printHelp(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command: %s", commandArgs[0])
	}
}

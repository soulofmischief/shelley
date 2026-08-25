package client

import (
	"flag"
	"fmt"
	"io"
)

func printUsage(writer io.Writer, fs *flag.FlagSet) {
	fmt.Fprintln(writer, "Shelley CLI client")
	fmt.Fprintln(writer, "\nUsage: shelley client [flags] <command> [args...]")
	fmt.Fprintln(writer, "\nFlags:")
	fs.PrintDefaults()
	fmt.Fprintln(writer, "\nCommands:")
	fmt.Fprintln(writer, "  chat       Send a message and stream the response")
	fmt.Fprintln(writer, "  read       Read conversation messages")
	fmt.Fprintln(writer, "  list       List conversations")
	fmt.Fprintln(writer, "  search     Search conversations")
	fmt.Fprintln(writer, "  tag        Show or change a conversation's tags")
	fmt.Fprintln(writer, "  tags       List tags already in use")
	fmt.Fprintln(writer, "  archive    Archive a conversation")
	fmt.Fprintln(writer, "  unarchive  Restore an archived conversation")
	fmt.Fprintln(writer, "  delete     Delete a conversation")
	fmt.Fprintln(writer, "  models     List available models")
	fmt.Fprintln(writer, "  help       Print detailed help")
}

func printHelp(writer io.Writer) {
	fmt.Fprintf(writer, `Shelley CLI client

Usage:
  shelley client [flags] <command> [args...]

Global flags:
  -url URL       Server URL (default: unix://%s)
  -json          Emit JSON lines for automation
  -no-color      Disable colored output
  -H HEADER      Add an HTTP header (repeatable)

Commands:
  chat [-p PROMPT] [-c ID] [-model MODEL] [-cwd DIR] [--immediate]
       [--ephemeral] [--disable-notifications] [PROMPT...]
      Send a message and stream the current response. Use -p - for stdin.
      --immediate returns after the turn starts. --ephemeral waits and archives.

  read [-f] CONVERSATION_ID
      Read the conversation. -f follows through the current turn.

  list [-a] [--limit N] [-q QUERY]
      List conversations. -a selects archived conversations.

  search [--limit N] QUERY
      Search conversation slugs and message content.

  tag [-rm|-set] CONVERSATION_ID [TAG...]
      Show, add, remove, replace, or clear a conversation's tags.

  tags [--limit N]
      List tags already in use across active and archived conversations.

  archive|unarchive|delete CONVERSATION_ID
      Change conversation lifecycle state.

  models
      List model readiness.

Examples:
  shelley client chat "what is 2+2?"
  echo "explain this" | shelley client chat -p -
  shelley client --json chat --ephemeral "summarize this repository"
`, DefaultSocketPath())
}

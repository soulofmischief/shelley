#!/bin/bash
set -euo pipefail

CONTENTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="$CONTENTS_DIR/MacOS/shelley-server"
DATA_DIR="$HOME/Library/Application Support/Shelley"

mkdir -p "$DATA_DIR"
PORT_FILE="$(mktemp -t shelley-port)"
trap 'rm -f "$PORT_FILE"' EXIT

"$BINARY" -db "$DATA_DIR/shelley.db" serve -port 0 -port-file "$PORT_FILE" &
SERVER_PID=$!
trap 'rm -f "$PORT_FILE"; kill "$SERVER_PID" 2>/dev/null || true' EXIT

for _ in $(seq 1 60); do
    if ! kill -0 "$SERVER_PID" 2>/dev/null; then
        wait "$SERVER_PID"
        exit $?
    fi
    if [ -s "$PORT_FILE" ]; then
        PORT="$(cat "$PORT_FILE")"
        open "http://localhost:$PORT"
        wait "$SERVER_PID"
        exit $?
    fi
    sleep 0.5
done

echo "Shelley did not report its listening port" >&2
exit 1

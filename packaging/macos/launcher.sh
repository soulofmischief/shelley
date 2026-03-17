#!/bin/bash
CONTENTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
BINARY="$CONTENTS_DIR/MacOS/shelley-server"

DATA_DIR="$HOME/Library/Application Support/Shelley"
mkdir -p "$DATA_DIR"

PORT_FILE=$(mktemp -t shelley-port)

"$BINARY" serve -db "$DATA_DIR/shelley.db" -port 0 -port-file "$PORT_FILE" &
SERVER_PID=$!

for i in $(seq 1 60); do
    if [ -s "$PORT_FILE" ]; then
        PORT=$(cat "$PORT_FILE")
        open "http://localhost:$PORT"
        break
    fi
    sleep 0.5
done

rm -f "$PORT_FILE"
wait $SERVER_PID

#!/bin/bash
set -euo pipefail

ARCH="${1:?Usage: build-macos-app.sh <arm64|amd64> <binary-path> [version] [output-dir]}"
BINARY="${2:?Usage: build-macos-app.sh <arm64|amd64> <binary-path> [version] [output-dir]}"
VERSION="${3:-0.0.0}"
OUTPUT_DIR="${4:-dist}"

case "$ARCH" in
    arm64|amd64) ;;
    *) echo "Unsupported architecture: $ARCH" >&2; exit 1 ;;
esac

for TOOL in rsvg-convert iconutil plutil lipo ditto; do
    if ! command -v "$TOOL" >/dev/null 2>&1; then
        echo "Required macOS app build tool is missing: $TOOL" >&2
        exit 1
    fi
done

if [ ! -f "$BINARY" ]; then
    echo "Shelley binary does not exist: $BINARY" >&2
    exit 1
fi

LIPO_ARCH="$ARCH"
if [ "$ARCH" = amd64 ]; then
    LIPO_ARCH=x86_64
fi
if ! lipo "$BINARY" -verify_arch "$LIPO_ARCH"; then
    echo "Shelley binary does not contain the requested $ARCH architecture: $BINARY" >&2
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PKG_DIR="$REPO_ROOT/packaging/macos"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

ICONSET="$WORK_DIR/AppIcon.iconset"
mkdir -p "$ICONSET"
for SIZE in 16 32 128 256 512; do
    rsvg-convert -w "$SIZE" -h "$SIZE" "$PKG_DIR/icon.svg" -o "$ICONSET/icon_${SIZE}x${SIZE}.png"
    DOUBLE=$((SIZE * 2))
    rsvg-convert -w "$DOUBLE" -h "$DOUBLE" "$PKG_DIR/icon.svg" -o "$ICONSET/icon_${SIZE}x${SIZE}@2x.png"
done
iconutil -c icns -o "$WORK_DIR/AppIcon.icns" "$ICONSET"

APP="$WORK_DIR/Shelley.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
cp "$WORK_DIR/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"
sed "s/__VERSION__/$VERSION/g" "$PKG_DIR/Info.plist" > "$APP/Contents/Info.plist"
plutil -lint "$APP/Contents/Info.plist" >/dev/null
cp "$PKG_DIR/launcher.sh" "$APP/Contents/MacOS/Shelley"
cp "$BINARY" "$APP/Contents/MacOS/shelley-server"
chmod +x "$APP/Contents/MacOS/Shelley" "$APP/Contents/MacOS/shelley-server"

mkdir -p "$OUTPUT_DIR"
rm -rf "$OUTPUT_DIR/Shelley.app"
ditto "$APP" "$OUTPUT_DIR/Shelley.app"
echo "Created $OUTPUT_DIR/Shelley.app"

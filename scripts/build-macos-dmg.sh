#!/bin/bash
set -euo pipefail

ARCH="${1:?Usage: build-macos-dmg.sh <arm64|amd64> <binary-path> [version]}"
BINARY="${2:?Usage: build-macos-dmg.sh <arm64|amd64> <binary-path> [version]}"
VERSION="${3:-0.0.0}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

"$SCRIPT_DIR/build-macos-app.sh" "$ARCH" "$BINARY" "$VERSION" "$WORK_DIR"
APP="$WORK_DIR/Shelley.app"
ZIP_NAME="Shelley_darwin_${ARCH}.zip"
DMG_NAME="Shelley_darwin_${ARCH}.dmg"

ditto -c -k --sequesterRsrc --keepParent "$APP" "$ZIP_NAME"

DMG_STAGING="$WORK_DIR/dmg-staging"
mkdir -p "$DMG_STAGING"
ditto "$APP" "$DMG_STAGING/Shelley.app"
ln -s /Applications "$DMG_STAGING/Applications"
hdiutil create -volname Shelley -srcfolder "$DMG_STAGING" -ov -format UDZO "$DMG_NAME"

echo "Created $ZIP_NAME"
echo "Created $DMG_NAME"

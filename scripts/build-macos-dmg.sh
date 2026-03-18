#!/bin/bash
set -euo pipefail

ARCH="${1:?Usage: build-macos-dmg.sh <arm64|amd64> <binary-path> [version]}"
BINARY="${2:?Usage: build-macos-dmg.sh <arm64|amd64> <binary-path> [version]}"
VERSION="${3:-0.0.0}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PKG_DIR="$REPO_ROOT/packaging/macos"

APP_NAME="Shelley"
BUNDLE="${APP_NAME}.app"
DMG_NAME="${APP_NAME}_darwin_${ARCH}.dmg"
ZIP_NAME="${APP_NAME}_darwin_${ARCH}.zip"

WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

# --- Build .icns from SVG ---------------------------------------------------

brew list librsvg &>/dev/null || brew install librsvg

ICONSET="$WORK_DIR/AppIcon.iconset"
mkdir -p "$ICONSET"

for SIZE in 16 32 128 256 512; do
    rsvg-convert -w "$SIZE" -h "$SIZE" "$PKG_DIR/icon.svg" -o "$ICONSET/icon_${SIZE}x${SIZE}.png"
    DOUBLE=$((SIZE * 2))
    rsvg-convert -w "$DOUBLE" -h "$DOUBLE" "$PKG_DIR/icon.svg" -o "$ICONSET/icon_${SIZE}x${SIZE}@2x.png"
done

iconutil -c icns -o "$WORK_DIR/AppIcon.icns" "$ICONSET"

# --- Assemble .app bundle ----------------------------------------------------

APP="$WORK_DIR/$BUNDLE"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

cp "$WORK_DIR/AppIcon.icns" "$APP/Contents/Resources/AppIcon.icns"

sed "s/__VERSION__/$VERSION/g" "$PKG_DIR/Info.plist" > "$APP/Contents/Info.plist"

cp "$PKG_DIR/launcher.sh" "$APP/Contents/MacOS/$APP_NAME"
chmod +x "$APP/Contents/MacOS/$APP_NAME"

cp "$BINARY" "$APP/Contents/MacOS/shelley-server"
chmod +x "$APP/Contents/MacOS/shelley-server"

# --- Create .app.zip ---------------------------------------------------------

ditto -c -k --sequesterRsrc "$APP" "$WORK_DIR/$ZIP_NAME"
cp "$WORK_DIR/$ZIP_NAME" .
echo "Created $ZIP_NAME"

# --- Create DMG ---------------------------------------------------------------

DMG_STAGING="$WORK_DIR/dmg-staging"
mkdir -p "$DMG_STAGING"
cp -a "$APP" "$DMG_STAGING/"
ln -s /Applications "$DMG_STAGING/Applications"

hdiutil create \
    -volname "$APP_NAME" \
    -srcfolder "$DMG_STAGING" \
    -ov \
    -format UDZO \
    "$WORK_DIR/$DMG_NAME"

cp "$WORK_DIR/$DMG_NAME" .
echo "Created $DMG_NAME"

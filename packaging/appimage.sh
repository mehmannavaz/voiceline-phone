#!/bin/bash
# Builds the Linux AppImage: AppDir + appimagetool (no FUSE needed).
set -euo pipefail
VERSION="${1:-v1.0.0}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIST="$ROOT/dist"
BIN="$DIST/voiceline-phone-linux-amd64"
[ -x "$BIN" ] || { echo "appimage.sh: build the linux binary first (make linux)"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
APPDIR="$WORK/VoicelinePhone.AppDir"
mkdir -p "$APPDIR/usr/bin" "$APPDIR/usr/share/metainfo"

cp "$BIN" "$APPDIR/usr/bin/voiceline-phone"
cp "$ROOT/packaging/voiceline-phone.desktop" "$APPDIR/"
cp "$ROOT/packaging/icon.png" "$APPDIR/voiceline-phone.png"
cp "$ROOT/packaging/AppRun" "$APPDIR/AppRun"
chmod +x "$APPDIR/AppRun" "$APPDIR/usr/bin/voiceline-phone"

cat > "$APPDIR/usr/share/metainfo/voiceline-phone.appdata.xml" <<XML
<?xml version="1.0" encoding="UTF-8"?>
<component type="desktop-application">
  <id>voiceline-phone</id>
  <name>Voiceline Phone</name>
  <summary>Encrypted call protocol client (VCP) for Voiceline lines</summary>
  <metadata_license>MIT</metadata_license>
  <project_license>MIT</project_license>
  <releases>
    <release version="${VERSION#v}" date="$(date +%F)"/>
  </releases>
</component>
XML

# appimagetool without FUSE:
TOOL="$WORK/appimagetool"
curl -sSL -o "$TOOL" https://github.com/AppImage/appimagetool/releases/download/continuous/appimagetool-x86_64.AppImage
chmod +x "$TOOL"
APPIMAGE_EXTRACT_AND_RUN=1 "$TOOL" "$APPDIR" "$DIST/voiceline-phone-x86_64.AppImage" --no-append \
  -u "ghactions" 2>/dev/null || APPIMAGE_EXTRACT_AND_RUN=1 "$TOOL" "$APPDIR" "$DIST/voiceline-phone-x86_64.AppImage"
echo "built $DIST/voiceline-phone-x86_64.AppImage"

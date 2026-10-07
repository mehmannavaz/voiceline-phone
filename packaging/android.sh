#!/bin/bash
# Builds the Android APK. Installs the Android SDK + NDK into
# ~/.android-sdk on demand (no root needed — everything lands in $HOME).
# Usage: packaging/android.sh <output-dir> [version]
set -euo pipefail

OUT="${1:-dist}"
VERSION="${2:-1.0.0}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"

SDK="${ANDROID_HOME:-$HOME/.android-sdk}"
NDK_VER="28.2.13676358"
JAVA_DIR="$(dirname "$(dirname "$(readlink -f "$(which java)")")")"

mkdir -p "$OUT"

# 1. command-line tools
if [ ! -x "$SDK/cmdline-tools/latest/bin/sdkmanager" ]; then
  echo "android.sh: installing command-line tools into $SDK"
  mkdir -p "$SDK/cmdline-tools"
  curl -sSL -o /tmp/cmdtools.zip \
    https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip
  unzip -q -o /tmp/cmdtools.zip -d "$SDK/cmdline-tools"
  rm -rf "$SDK/cmdline-tools/latest"
  mv "$SDK/cmdline-tools/cmdline-tools" "$SDK/cmdline-tools/latest"
  rm /tmp/cmdtools.zip
fi

export ANDROID_HOME="$SDK"
export JAVA_HOME="${JAVA_HOME:-$JAVA_DIR}"
export PATH="$SDK/cmdline-tools/latest/bin:$PATH"

# 2. platform + tools + NDK
if [ ! -d "$SDK/platforms/android-34" ] || [ ! -d "$SDK/build-tools/34.0.0" ]; then
  yes | sdkmanager --licenses > /dev/null 2>&1 || true
  sdkmanager "platform-tools" "platforms;android-34" "build-tools;34.0.0" > /dev/null
fi
if [ ! -d "$SDK/ndk/$NDK_VER" ]; then
  sdkmanager "ndk;$NDK_VER" > /dev/null
fi
export ANDROID_NDK_HOME="$SDK/ndk/$NDK_VER"

# 3. fyne CLI (v2.8.x ships the packager even with the deprecation notice)
if ! command -v fyne > /dev/null 2>&1; then
  go install fyne.io/fyne/v2/cmd/fyne@v2.8.1
fi
export PATH="$(go env GOPATH)/bin:$PATH"

echo "android.sh: packaging with fyne (SDK: $SDK, NDK: $NDK_VER)"
cd "$ROOT/cmd/phone"
fyne package -os android \
  -appID io.github.mehmannavaz.voicelinephone \
  -icon "$ROOT/packaging/icon.png" \
  -name phone \
  -release -appVersion "$VERSION" -appBuild 1

mkdir -p "$ROOT/$OUT"
mv phone.apk "$ROOT/$OUT/voiceline-phone-android.apk"
echo "android.sh: built $OUT/voiceline-phone-android.apk"

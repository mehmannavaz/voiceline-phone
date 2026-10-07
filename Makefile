# voiceline-phone — the VCP caller apps (website / APK / EXE / AppImage)
#
# Standard build:
#     make            # linux binaries into dist/
#     make windows    # voiceline-phone.exe (cross via zig)
#     make appimage   # Linux AppImage
#     make web        # website bundle (WASM + assets)
#     make android    # APK (needs ANDROID_HOME + NDK; see docs/BUILDING.md)
#     make dist       # everything + checksums

VERSION ?= v1.0.0
DIST := dist
GO     ?= go
ZIG    ?= /home/z/zig/zig

# Linux GUI builds need the X11/GL/ALSA development headers. The sandbox
# shim tree below carries them when the system packages are absent.
CGO_SHIM   := /home/z/cgo-shim
SHIM_CFLAGS  := -I$(CGO_SHIM)/root/usr/include
SHIM_LDFLAGS := -L$(CGO_SHIM)/root/usr/lib/x86_64-linux-gnu
export PKG_CONFIG_PATH ?= $(CGO_SHIM)/pkgconfig

# The sandbox shim provides headers the system lacks; harmless when the
# system already has them.
GUI_ENV := CGO_CFLAGS="$(SHIM_CFLAGS)" CGO_LDFLAGS="$(SHIM_LDFLAGS)"

LDFLAGS := -s -w

.PHONY: all clean test windows linux appimage web android dist vcpc webserve test-e2e

all: linux

test:
	$(GO) test ./internal/vcp/ -count=1 -race

test-e2e:
	bash scripts/e2e.sh
	bash scripts/e2e_web.sh

# ── Linux native ──────────────────────────────────────────
linux: $(DIST)/voiceline-phone-linux-amd64 $(DIST)/vcpc-linux-amd64 $(DIST)/webserve-linux-amd64

$(DIST)/voiceline-phone-linux-amd64: FORCE
	@mkdir -p $(DIST)
	$(GUI_ENV) CGO_ENABLED=1 $(GO) build -tags x11 -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/phone

$(DIST)/vcpc-linux-amd64: FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/vcpc

$(DIST)/webserve-linux-amd64: FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/webserve

# ── Windows ───────────────────────────────────────────────
windows: $(DIST)/voiceline-phone-windows-amd64.exe $(DIST)/vcpc-windows-amd64.exe $(DIST)/webserve-windows-amd64.exe

$(DIST)/voiceline-phone-windows-amd64.exe: FORCE
	@mkdir -p $(DIST)
	CC="$(ZIG) cc -target x86_64-windows-gnu" CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
		$(GO) build -trimpath -ldflags "$(LDFLAGS) -H=windowsgui -extldflags=-Wl,--subsystem,windows" -o $@ ./cmd/phone

$(DIST)/vcpc-windows-amd64.exe: FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/vcpc

$(DIST)/webserve-windows-amd64.exe: FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/webserve

# ── Website bundle ────────────────────────────────────────
web: $(DIST)/voiceline-phone-web.zip

$(DIST)/voiceline-phone-web.zip: web/app.js web/index.html web/style.css web/main.go FORCE
	GOOS=js GOARCH=wasm $(GO) build -trimpath -o web/phone.wasm ./web
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js
	cp packaging/icon.png web/icon.png
	@mkdir -p $(DIST)
	(cd web && zip -q -r ../$(DIST)/voiceline-phone-web.zip app.js index.html style.css manifest.webmanifest mic-worklet.js playback-worklet.js phone.wasm wasm_exec.js icon.png)

# Keep the embedded site in sync for webserve builds.
webserve-site:
	rm -rf cmd/webserve/site
	cp -r web cmd/webserve/site
	rm -f cmd/webserve/site/main.go

# ── AppImage ──────────────────────────────────────────────
appimage: $(DIST)/voiceline-phone-x86_64.AppImage

$(DIST)/voiceline-phone-x86_64.AppImage: $(DIST)/voiceline-phone-linux-amd64 packaging/icon.png packaging/AppRun packaging/voiceline-phone.desktop packaging/appimage.sh FORCE
	bash packaging/appimage.sh $(VERSION)

# ── Android APK ───────────────────────────────────────────
# Requires the Android SDK + NDK; packaging/android.sh installs them
# into ~/.android-sdk when ANDROID_HOME is unset.
android: $(DIST)/voiceline-phone-android.apk

$(DIST)/voiceline-phone-android.apk: FORCE
	bash packaging/android.sh $(DIST)

# ── everything ────────────────────────────────────────────
dist: linux windows web appimage android
	cd $(DIST) && sha256sum voiceline-phone-linux-amd64 vcpc-linux-amd64 webserve-linux-amd64 \
		voiceline-phone-windows-amd64.exe vcpc-windows-amd64.exe webserve-windows-amd64.exe \
		voiceline-phone-x86_64.AppImage voiceline-phone-web.zip voiceline-phone-android.apk > checksums.txt

FORCE:

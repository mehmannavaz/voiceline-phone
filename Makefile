# voiceline-phone — the VCP call client, website + CLI
#
# The website (web/, WASM) is the primary client; vcpc is the headless
# JSONL driver (also the E2E harness); webserve hosts the website.
#
#     make            # linux binaries into dist/
#     make windows    # windows binaries (pure Go cross-compile)
#     make web        # website bundle (WASM + assets)
#     make dist       # everything + checksums

VERSION ?= v1.1.0
DIST := dist
GO     ?= go

LDFLAGS := -s -w

.PHONY: all clean test windows linux web dist vcpc webserve test-e2e webserve-site

all: linux

test:
	$(GO) test ./internal/vcp/ -count=1 -race
	node scripts/worklet_test.mjs

test-e2e:
	bash scripts/e2e.sh
	bash scripts/e2e_web.sh

# ── Linux ─────────────────────────────────────────────────
linux: $(DIST)/vcpc-linux-amd64 $(DIST)/webserve-linux-amd64

$(DIST)/vcpc-linux-amd64: FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/vcpc

$(DIST)/webserve-linux-amd64: webserve-site FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/webserve

# ── Windows (pure Go — no cgo, no toolchain needed) ───────
windows: $(DIST)/vcpc-windows-amd64.exe $(DIST)/webserve-windows-amd64.exe

$(DIST)/vcpc-windows-amd64.exe: FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/vcpc

$(DIST)/webserve-windows-amd64.exe: webserve-site FORCE
	@mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/webserve

# ── Website bundle ────────────────────────────────────────
web: $(DIST)/voiceline-phone-web.zip

$(DIST)/voiceline-phone-web.zip: web/app.js web/index.html web/style.css web/main.go FORCE
	GOOS=js GOARCH=wasm $(GO) build -trimpath -o web/phone.wasm ./web
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" web/wasm_exec.js
	@mkdir -p $(DIST)
	(cd web && zip -q -r ../$(DIST)/voiceline-phone-web.zip app.js index.html style.css manifest.webmanifest mic-worklet.js playback-worklet.js phone.wasm wasm_exec.js icon.png)

# Keep the embedded site in sync for webserve builds.
webserve-site:
	rm -rf cmd/webserve/site
	cp -r web cmd/webserve/site
	rm -f cmd/webserve/site/main.go

# ── everything ────────────────────────────────────────────
dist: linux windows web
	cd $(DIST) && sha256sum vcpc-linux-amd64 webserve-linux-amd64 \
	        vcpc-windows-amd64.exe webserve-windows-amd64.exe \
	        voiceline-phone-web.zip > checksums.txt

FORCE:

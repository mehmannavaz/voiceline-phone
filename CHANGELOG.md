# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to semantic versioning.

## [1.0.0] - 2026-10-07

### Added — the caller apps, everywhere

- **One Go client, four form factors**: the Windows EXE (GUI subsystem,
  cross-compiled via zig), the Linux AppImage, the universal Android APK
  (arm/arm64/x86/x86_64, RECORD_AUDIO declared) and the website — the
  same `internal/vcp` package compiled to WebAssembly, with JavaScript
  doing only audio and DOM.
- **The full VCP v1.2 client**: PBKDF2 + AES-256-GCM encrypted session,
  call-out with per-call lifecycle events, multi-call concurrency (a line
  is never busy), DTMF both ways, blind transfer to outside numbers,
  group calls with live membership events — and **incoming calls**: the
  client subscribes, rings when someone calls the line, and
  accepts/rejects while the caller hears ringback (needs voiceline
  server ≥ 0.11.0).
- **Desktop softphone** (Fyne): login with remembered settings, live line
  header (registration, hook, active calls), keypad dialer, per-call
  cards with mute / keys / transfer / add-person / hang-up, incoming
  call banner with a real two-tone ringtone, 8 kHz miniaudio
  capture/playback with a saturating mixer and bounded jitter buffers.
- **Website** (`web/`): a polished single-page softphone — same
  protocol, AudioWorklet resampling 8 kHz ↔ device rate, installable as
  a PWA (manifest + icons), served by `webserve` (one static binary with
  correct wasm MIME) or any static file server.
- **`vcpc`, the headless CLI client**: dial, receive, tone/silence uplink,
  DTMF, conference, transfer, timed hang-up — every event as JSONL; the
  end-to-end test driver and an operator's Swiss army knife.
- **Verification**: golden PBKDF2/HMAC vectors (cross-checked against an
  independent Python implementation), frame-codec tamper/sequence tests,
  an in-process mini-server suite, and two live end-to-end suites that
  boot the real voiceline server + mockpbx SIP provider and prove
  outbound audio, incoming accept/reject, conference, transfer and the
  WASM website core — all against real SIP/RTP.

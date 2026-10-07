# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to semantic versioning.

## [1.1.0] - 2026-10-07

### Removed — lean by design

- **The Android APK, the Windows GUI EXE, the Linux AppImage and the Fyne
  desktop app are gone** (along with `internal/gui`, `internal/audioio`,
  `cmd/phone` and `packaging/`). The repo is now the website + the `vcpc`
  CLI + `webserve`, with a module graph of two direct dependencies
  (previously four + ~35 transitive, including the whole Fyne stack).
  The pure-Go `internal/vcp` core is unchanged and still builds anywhere.
  Reasons: no graphics stack is needed to run the client (the website is
  the GUI), the APK was a 112 MB artifact for a use case the website
  covers better, and the browser's audio stack beats hand-rolled native
  capture (see below).

### Changed — call quality first

- **Microphone path rebuilt** (`web/mic-worklet.js`): mono mix → 140 Hz
  high-pass → a 6th-order Butterworth anti-alias wall (3 cascaded biquads
  at 3.4 kHz) → drift-free fractional decimation to 8 kHz. The old
  nearest-sample/linear decimation aliased everything above 4 kHz into
  the voice band — measured as ~61 dB of would-be hiss now gone.
- **Playback path rebuilt** (`web/playback-worklet.js`): a 64 ms start
  watermark (was: played immediately → underrun chatter at call start),
  a 320 ms hard cap shedding the oldest audio back to 120 ms on bursts,
  a flat-deque jitter buffer with fractional interpolation, and a
  zero-lookahead soft limiter so boosting a quiet caller can never clip.
- **Volume is now safe and yours**: the far end plays at its natural
  level (1.0×) instead of the old fixed boost, with a 0–300% slider that
  persists — and the ringtone follows it.
- **Output device routing**: pick headphones/headset/speakers in the
  audio bar (AudioContext.setSinkId, feature-detected); the choice
  persists across sessions. No more defaulting to the blaring speakers.
- **Interactive latency**: the AudioContext requests the smallest buffer
  the device allows; worklet→main-thread stats are throttled to ~10/s
  (previously every quantum, which janked weak machines into glitches).
- **Mute emits zero frames** instead of stalling the stream — the 20 ms
  cadence, sequence numbers and the server pacer stay in sync; a fresh
  call leg flushes stale audio and re-primes the jitter buffer.
- **Live quality readout**: `jitter ms · dropped · gaps` in the audio bar.

### Added

- `scripts/worklet_test.mjs`: numerical verification of the shipped
  worklets (rate accuracy, alias suppression, burst shed, limiter
  ceiling, mute/flush semantics) — wired into `make test`.

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

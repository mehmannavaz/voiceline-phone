# Building the client

Everything here is pure Go — no cgo, no Android SDK, no system
dependencies beyond Go 1.27+ (and Node for the audio DSP tests and the
WASM e2e harness).

## Website (WASM) — the primary client

```sh
make web          # → dist/voiceline-phone-web.zip
go run ./cmd/webserve   # serve at :8090
```

The Go core (`internal/vcp`) is compiled with `GOOS=js GOARCH=wasm`
(see `web/main.go`) into `phone.wasm`; the browser side is `index.html`
+ `app.js` + two AudioWorklets that run the DSP on the real-time audio
thread:

- `mic-worklet.js` — mono mix, 140 Hz high-pass, 3× biquad anti-alias
  wall at 3.4 kHz, drift-free fractional decimation to 8 kHz, 20 ms
  frames; mute emits zeros so the cadence never breaks.
- `playback-worklet.js` — 64 ms start watermark, 320 ms hard cap with
  shed-to-120 ms on bursts, fractional linear resampling to the device
  rate, gain + zero-lookahead soft limiter, throttled stats.

Serve over HTTP on your LAN, or put it behind a TLS proxy and use
`wss://` URLs — the protocol's AES-GCM layer rides on top either way.

`webserve` embeds the site (run `make webserve-site` to re-sync the
embedded copy from `web/` — the release build does this automatically).

## CLI client (vcpc)

```sh
make linux        # dist/vcpc-linux-amd64 + webserve-linux-amd64
make windows      # dist/vcpc-windows-amd64.exe + webserve-windows-amd64.exe
```

Windows cross-compiles with plain `GOOS=windows` — there is no cgo
anywhere in the module, so no MinGW/zig is needed.

## Tests

```sh
make test         # internal/vcp: golden vectors, frame codec, mini-server
                  # + scripts/worklet_test.mjs: numerical DSP verification
                  #   (rate accuracy, alias suppression, burst shed,
                  #    limiter ceiling, mute/flush semantics)
make test-e2e     # scripts/e2e.sh + scripts/e2e_web.sh:
                  # boots the real voiceline + mockpbx and drives the
                  # CLI client and the WASM website core end to end
```

The live suites need the voiceline server ≥ v0.11.0 (VCP v1.2 incoming
calls) — they build it from `../voiceline` automatically.

## A note on scope

The v1.0.0 release also shipped a Fyne desktop app, an Android APK, a
Windows GUI EXE and an AppImage. They were removed in v1.1.0 to keep
this repo lean — the website covers the same protocol surface with
better audio (the browser's AEC/NS/AGC + the worklet DSP), and `vcpc`
covers automation. If a native app is ever needed again, `internal/vcp`
remains pure Go and builds anywhere.

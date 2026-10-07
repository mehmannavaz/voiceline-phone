# Voiceline Phone

The caller apps for the **Voiceline Call Protocol (VCP)** — the encrypted
custom protocol that lets you place and receive real phone calls through a
[Voiceline](https://github.com/mehmannavaz/voiceline) line registered at a
SIP provider — from a **website**, an **Android APK**, a **Windows EXE**
or a **Linux AppImage**. All four are the same Go client.

```
┌────────────────────┐   WebSocket (binary frames, AES-256-GCM)   ┌──────────┐
│  voiceline-phone   │ ─────────────────────────────────────────▶ │ voiceline│
│  web / apk / exe / │ ◀───────────────────────────────────────── │  server  │
│  appimage          │        protobuf envelopes + PCM audio      │  :8086   │
└────────────────────┘                                            └────┬─────┘
                                                                       │ SIP/RTP
                                                            ┌──────────▼─────────┐
                                                            │ your VoIP provider │
                                                            │ (3CX, Asterisk, …) │
                                                            └────────────────────┘
```

## What you get

| Artifact | What it is |
|---|---|
| `voiceline-phone-windows-amd64.exe` | one-file Windows softphone (GUI subsystem, no console) |
| `voiceline-phone-x86_64.AppImage` | one-file Linux AppImage (X11) |
| `voiceline-phone-android.apk` | universal Android APK (arm, arm64, x86, x86_64) |
| `voiceline-phone-web.zip` | the website — serve it from anywhere, zero install |
| `vcpc-*` | headless CLI client (ops + integration testing, JSONL events) |
| `webserve-*` | one-binary static server for the website |

Every form factor speaks the full protocol:

- **Call out** through the line — the callee sees the line's number.
- **Receive calls**: subscribe and the app *rings* when someone calls the
  line (VCP v1.2, server ≥ 0.11.0); accept or reject, with real ringback
  toward the caller while you decide.
- **Never busy**: calls are independent SIP dialogs — one line, many
  simultaneous calls (until the line's `max_concurrent`, if set).
- **DTMF** both ways, **blind transfer** to an outside number (you only
  type the number — the server assembles the SIP URI), **group calls**
  (add a person, everyone hears everyone).
- **Real audio**: microphone and speakers at 8 kHz PCM16, mixed and
  jitter-managed in every client.
- **Zero shared secrets on the wire**: per-connection random nonces,
  PBKDF2 key schedule, AES-256-GCM every frame, sequence enforcement.

## Quick start

1. Your voiceline server (≥ v0.11.0) needs the protocol endpoint on and a
   line with a **call password** (console → Lines → Call password, or
   `call_password:` on the line in config.yaml):

   ```yaml
   webcall:
     listen: ":8086"
   lines:
     - id: main
       server: sip.example.net:5060
       user: "17005554117"
       pass: s3cret
       call_password: <generate one in the console>
   ```

2. Start the app (EXE / AppImage / APK), or unzip the website and run
   `webserve`, then open `http://127.0.0.1:8090/`.

3. Fill in the server (`ws://your-server:8086/call`), line id and call
   password — and you have a phone. Check "Receive this line's calls here"
   to ring on incoming calls.

### The CLI client

```sh
vcpc -url ws://192.168.1.7:8086/call -line main -password SECRET \
     -dial +989221234567 -seconds 30 -tone        # call out, 30 s of tone

vcpc -url ws://192.168.1.7:8086/call -line main -password SECRET -receive
                                                 # ring on the line's calls

vcpc ... -dial 1002 -dtmf 12 -conf 1003 -transfer 1004   # the whole story
```

Every event prints as one JSON line — scripts love it.

## The protocol in one screen

VCP runs over a WebSocket at `ws://server:8086/call`, binary frames only
(full schema: `proto/webcall.proto`, served live at `/proto`):

```
HelloRequest{version,line,client_nonce}      → plaintext
HelloResponse{version,server_nonce}          → plaintext
master = PBKDF2(password, cn||sn, 60000, 96) → both sides
AuthConfirm{proof=HMAC(auth_key, …)}         → encrypted, seq 0
AuthResult{ok, line}                         → encrypted
[ 8-byte BE sequence | AES-256-GCM | tag ]   → every frame after that
```

Inside the encryption: `DialRequest`, `CallEvent`, `MediaStart`,
`AudioFrame` (PCM16 LE 8 kHz mono), `DtmfRequest/Event`,
`TransferRequest/Event`, `ConferenceInvite/Event`,
`InboundSubscription/Ack`, `IncomingCallEvent`, `AcceptRequest`,
`RejectRequest`, `Ping/Pong`, `Bye`. Sequence numbers strictly increase
per direction; any deviation drops the connection. Calls are identified
by `call_id` and are fully independent — the line is never "busy".

## Repository layout

```
proto/webcall.proto       the wire contract (vendored from voiceline)
internal/vcp/             the protocol client (crypto, frames, session,
                          calls, inbound) — pure Go, builds everywhere
internal/audioio/         microphone/speakers via miniaudio (malgo)
internal/gui/             the Fyne desktop softphone (login → phone)
cmd/phone/                the desktop+Android app entry point
cmd/vcpc/                 the headless CLI client
cmd/webserve/             static server for the website
web/                      the website: Go core → phone.wasm + JS audio
scripts/e2e.sh            end-to-end test vs a real voiceline + mockpbx
scripts/e2e_web.sh        end-to-end test of the WASM website core
```

## Building

```sh
make test          # protocol unit tests (race detector on)
make test-e2e      # boots voiceline + mockpbx and calls through VCP
make linux         # Linux binaries → dist/
make windows       # cross-compiles the EXE via zig
make appimage      # Linux AppImage
make web           # website zip (WASM)
make android       # APK (installs the Android SDK+NDK on demand)
make dist          # everything + checksums
```

Details — including the sandbox-friendly cgo header shim and the Android
microphone permission note — live in [docs/BUILDING.md](docs/BUILDING.md).

## Android note (microphone permission)

The APK declares `RECORD_AUDIO`. Android 6+ also wants the grant at
runtime, which a plain Go activity cannot prompt for — so the first run
starts without microphone (a note says so; everything else works). Grant
it once with adb and restart the app:

```sh
adb shell pm grant io.github.mehmannavaz.voicelinephone android.permission.RECORD_AUDIO
```

## Security notes

- The call password never travels in the clear; it only feeds the PBKDF2
  schedule, and the handshake proof is HMAC'd over both nonces.
- Every frame is AES-256-GCM sealed with a per-direction key and a
  strictly increasing sequence — replay, injection and tampering all end
  the session.
- The website should be served over plain HTTP on your LAN or behind a
  TLS reverse proxy (wss://) as with any WebRTC-less softphone; the
  protocol itself adds the confidentiality layer over the WebSocket.

## License

MIT — see [LICENSE](LICENSE).

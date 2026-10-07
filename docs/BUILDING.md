# Building every form factor

## Linux (native GUI)

The GUI is [Fyne](https://fyne.io) + [malgo](https://github.com/gen2brain/malgo)
(miniaudio), both cgo. You need the usual X11/GL/ALSA development headers:

```sh
sudo apt install -y libgl-dev libglx-dev libxxf86vm-dev libxrandr-dev \
    libxinerama-dev libxcursor-dev libxi-dev libxfixes-dev libasound2-dev
make linux
```

Build with the `x11` tag (`go build -tags x11 ./cmd/phone`) so GLFW skips
its Wayland backend; on Wayland desktops the app runs through XWayland.

### No root? The header shim

A user-level include/lib tree is enough for cgo:

```sh
mkdir -p ~/cgo-shim/pkgconfig && cd ~/cgo-shim
apt-get download libgl-dev libglx-dev libxxf86vm-dev libxrandr-dev \
    libxinerama-dev libxcursor-dev libxi-dev libxfixes-dev libasound2-dev
for d in *.deb; do dpkg -x "$d" root; done
```

Write `pkgconfig/gl.pc` and `pkgconfig/glfw3.pc` pointing at
`root/usr/include` / `root/usr/lib/x86_64-linux-gnu`, then:

```sh
export PKG_CONFIG_PATH=~/cgo-shim/pkgconfig
CGO_CFLAGS="-I$HOME/cgo-shim/root/usr/include" \
CGO_LDFLAGS="-L$HOME/cgo-shim/root/usr/lib/x86_64-linux-gnu" \
go build -tags x11 -o phone ./cmd/phone
```

(The repo's Makefile applies exactly this when `/home/z/cgo-shim` exists.)

## Windows EXE

Cross-compiled from Linux with [zig](https://ziglang.org) as the C
toolchain — no MinGW needed:

```sh
CC="zig cc -target x86_64-windows-gnu" CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  go build -trimpath \
  -ldflags "-s -w -H=windowsgui -extldflags=-Wl,--subsystem,windows" \
  -o voiceline-phone.exe ./cmd/phone
```

`-extldflags=-Wl,--subsystem,windows` is what actually marks the PE as a
GUI binary when cgo uses the external linker; `-H=windowsgui` alone does
not survive it.

## Android APK

`make android` (or `packaging/android.sh`) installs the SDK + NDK into
`~/.android-sdk` when `ANDROID_HOME` is unset, then runs the Fyne
packager:

```sh
fyne package -os android \
  -appID io.github.mehmannavaz.voicelinephone \
  -icon packaging/icon.png -name phone \
  -release -appVersion 1.0.0 -appBuild 1
```

`cmd/phone/AndroidManifest.xml` is picked up automatically — it declares
`INTERNET`, `RECORD_AUDIO` and `MODIFY_AUDIO_SETTINGS` and pins the
native library name (`android.app.lib_name` = `phone`, from the app
name). The result is a fat APK for arm, arm64, x86 and x86_64.

### The microphone permission on Android

Android 6+ grants dangerous permissions at *runtime*, and a plain Go
activity cannot raise the system dialog. The app therefore starts
gracefully without audio (a note says so) and works fully — including
audio — once you grant once:

```sh
adb shell pm grant io.github.mehmannavaz.voicelinephone android.permission.RECORD_AUDIO
```

…then restart the app. Playback and signalling always work.

## Website (WASM)

```sh
make web          # → dist/voiceline-phone-web.zip
go run ./cmd/webserve   # serve at :8090
```

The Go core is compiled with `GOOS=js GOARCH=wasm` (see `web/main.go`);
the browser side is `index.html` + `app.js` + two AudioWorklets that
resample 8 kHz PCM16 to the AudioContext rate and back. Serve over HTTP
on your LAN, or put it behind a TLS proxy and use `wss://` URLs — the
protocol's AES-GCM layer rides on top either way.

## AppImage

```sh
make appimage     # AppDir + appimagetool --appimage-extract-and-run
```

No FUSE needed; the resulting AppImage runs anywhere with X11.

## Tests

```sh
make test         # internal/vcp: golden vectors, frame codec, mini-server
make test-e2e     # scripts/e2e.sh + scripts/e2e_web.sh:
                  # boots the real voiceline + mockpbx and drives the
                  # CLI client and the WASM website core end to end
```

The live suites need the voiceline server ≥ v0.11.0 (VCP v1.2 incoming
calls) — they build it from `../voiceline` automatically.

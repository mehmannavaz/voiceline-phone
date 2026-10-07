#!/bin/bash
# Publishes the voiceline-phone v1.1.0 GitHub release with the lean asset
# set (web zip + vcpc + webserve + checksums) and removes the heavy native
# assets (APK / EXE / AppImage / desktop binary) from the v1.0.0 release.
set -euo pipefail
cd /home/z/my-project/voiceline-phone

PAT=$(cat /home/z/.pat)
AUTH="Authorization: Bearer $PAT"
API="https://api.github.com"
REPO="mehmannavaz/voiceline-phone"
TAG="v1.1.0"

git tag -f "$TAG" >/dev/null
git push -q origin "$TAG" 2>/dev/null || git push -q origin "refs/tags/$TAG"

cat > /tmp/relnotes-v11-phone.md <<'NOTES'
The Voiceline Call Protocol caller client — now **lean by design**: the
website (WASM) is the app, `vcpc` is the headless CLI.

## Downloads

| File | Platform |
|---|---|
| `voiceline-phone-web.zip` | The website — unzip and serve (or run `webserve`) |
| `vcpc-*` / `webserve-*` | Headless CLI client + static site server (linux & windows) |

The Android APK, Windows GUI EXE and AppImage from v1.0.0 are removed —
they are superseded by the website, which now has the better audio path.

## Call quality is the headline

- **Anti-alias wall on the microphone**: a 6th-order Butterworth at
  3.4 kHz before the 8 kHz leg — the old decimation folded everything
  above 4 kHz back into the voice band as hiss (~61 dB of noise, gone).
- **Adaptive jitter buffer**: 64 ms start watermark, 320 ms hard cap
  shedding the oldest audio back to 120 ms — latency can never grow
  into seconds.
- **Safe loudness by default**: the far end plays at its natural level
  with a 0–300% volume slider; a zero-lookahead soft limiter means a
  boosted caller can never clip or blast your ears.
- **Output routing**: pick headphones / headset / speakers in the audio
  bar (persists) — no more defaulting to the blaring speakers.
- **Interactive latency** everywhere: smallest device buffer, throttled
  stats, mute that keeps the 20 ms cadence, jitter/dropped/gaps
  readout in the UI.

Verified numerically (`scripts/worklet_test.mjs`: rate accuracy, alias
suppression, burst shed, limiter ceiling) and end to end against a live
voiceline server + mock SIP provider. Checksums in `checksums.txt`.
NOTES

# release
REL=$(curl -s -X POST "$API/repos/$REPO/releases" -H "$AUTH" -H "Content-Type: application/json" \
  -d "$(python3 -c "
import json
notes = open('/tmp/relnotes-v11-phone.md').read()
print(json.dumps({'tag_name':'$TAG','name':'voiceline-phone v1.1.0 — lean client, best-call-quality website','body':notes,'draft':False,'prerelease':False}))
")")
REL_ID=$(printf '%s' "$REL" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d.get('id',''))")
if [ -z "$REL_ID" ]; then
  echo "release create failed:"; printf '%s' "$REL" | head -c 400; exit 1
fi
echo "release id: $REL_ID"

upload() {
  f="$1"; mime="$2"
  name=$(basename "$f")
  URL="https://uploads.github.com/repos/$REPO/releases/$REL_ID/assets?name=$name"
  curl -s -X POST "$URL" -H "$AUTH" -H "Content-Type: $mime" --data-binary "@$f" \
    | python3 -c "import json,sys; d=json.load(sys.stdin); print('  asset:', d.get('name'), d.get('state', d.get('message','')))"
}

cd dist
upload voiceline-phone-web.zip application/zip
upload vcpc-linux-amd64 application/octet-stream
upload vcpc-windows-amd64.exe application/vnd.microsoft.portable-executable
upload webserve-linux-amd64 application/octet-stream
upload webserve-windows-amd64.exe application/vnd.microsoft.portable-executable
upload checksums.txt text/plain

# Remove the heavy native assets from the v1.0.0 release — superseded.
python3 - "$PAT" <<'PYEOF'
import json, sys, urllib.request

pat = sys.argv[1]
repo = "mehmannavaz/voiceline-phone"
auth = {"Authorization": f"Bearer {pat}", "Accept": "application/vnd.github+json"}

def api(path):
    return urllib.request.Request(f"https://api.github.com{path}", headers=auth)

req = api(f"/repos/{repo}/releases/tags/v1.0.0")
with urllib.request.urlopen(req) as r:
    rel = json.load(r)

kill = {"voiceline-phone-android.apk", "voiceline-phone-windows-amd64.exe",
        "voiceline-phone-x86_64.AppImage", "voiceline-phone-linux-amd64"}
for a in rel.get("assets", []):
    if a["name"] in kill:
        rq = urllib.request.Request(a["url"], method="DELETE", headers=auth)
        with urllib.request.urlopen(rq) as r:
            print(f"  deleted v1.0.0 asset: {a['name']} ({r.status})")
PYEOF

echo "RELEASE PUBLISHED: https://github.com/$REPO/releases/tag/$TAG"

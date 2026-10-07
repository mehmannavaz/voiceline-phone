#!/bin/bash
# Website (WASM) end-to-end test: boots mockpbx + the real voiceline
# server, then drives phone.wasm under Node — the exact Go core the
# browser runs — through a full call and an incoming call.
set -euo pipefail

DIR=/tmp/web-e2e
rm -rf "$DIR"; mkdir -p "$DIR"
cd /home/z/my-project/voiceline-phone

VL=/home/z/my-project/voiceline
GO=/home/z/go/bin/go
export PATH=/home/z/go/bin:/home/z/gopath/bin:$PATH GOPATH=/home/z/gopath

echo "== building =="
(cd "$VL" && $GO build -o "$DIR/voiceline" ./cmd/voiceline)
(cd "$VL" && $GO build -o "$DIR/mockpbx" ./cmd/mockpbx)

PW="e2e-CallPass-7Kmq2Ztv"

cat > "$DIR/config.yaml" <<CFG
server:
  listen: 127.0.0.1:15085
  tokens: [e2e-token]
sip:
  listen_addr: 127.0.0.1:15060
  local_ip: 127.0.0.1
webcall:
  listen: 127.0.0.1:15086
  inbound_ring_sec: 6
store:
  dir: $DIR/data
recordings:
  enabled: false
lines:
  - id: main
    server: 127.0.0.1:15090
    user: "1001"
    pass: s3cret
    call_password: $PW
    enabled: true
CFG

"$DIR/mockpbx" -sip 127.0.0.1:15090 -http 127.0.0.1:15099 \
  -engine 127.0.0.1:15060 -auth 1001:s3cret > "$DIR/mockpbx.log" 2>&1 &
PBX=$!
"$DIR/voiceline" -config "$DIR/config.yaml" serve > "$DIR/voiceline.log" 2>&1 &
SRV=$!
trap 'kill $PBX $SRV 2>/dev/null || true' EXIT
sleep 2

echo "== 1. website core: outbound call =="
VCP_URL=ws://127.0.0.1:15086/call VCP_LINE=main VCP_PASSWORD="$PW" \
  node scripts/webtest.mjs > "$DIR/web1.jsonl" 2>"$DIR/web1.err"
grep -q '"state":"answered"' "$DIR/web1.jsonl" && echo "  PASS: answered" || { echo "  FAIL: answered"; FAILED=1; }
grep -q '"state":"ended"' "$DIR/web1.jsonl" && echo "  PASS: ended" || { echo "  FAIL: ended"; FAILED=1; }
grep -q '=> PASS' "$DIR/web1.err" && echo "  PASS: summary" || { echo "  FAIL: summary"; cat "$DIR/web1.err" | tail -2; FAILED=1; }
curl -s -X POST http://127.0.0.1:15099/reset > /dev/null

echo "== 2. website core: incoming call accepted =="
node scripts/webtest.mjs > "$DIR/web2-prep.jsonl" 2>/dev/null || true &
sleep 0.1
pkill -f "web2-prep" 2>/dev/null || true
VCP_URL=ws://127.0.0.1:15086/call VCP_LINE=main VCP_PASSWORD="$PW" VCP_INCOMING=1 \
  node scripts/webtest.mjs > "$DIR/web2.jsonl" 2>"$DIR/web2.err" &
WEB=$!
sleep 2
curl -s -X POST 'http://127.0.0.1:15099/call?to=1001' > "$DIR/web2.call.json"
wait $WEB || true
grep -q '"type":"incoming"' "$DIR/web2.jsonl" && echo "  PASS: incoming offer" || { echo "  FAIL: incoming offer"; FAILED=1; }
grep -q '"state":"answered"' "$DIR/web2.jsonl" && echo "  PASS: accepted" || { echo "  FAIL: accepted"; FAILED=1; }
grep -q '=> PASS' "$DIR/web2.err" && echo "  PASS: summary" || { echo "  FAIL: summary"; tail -2 "$DIR/web2.err"; FAILED=1; }

echo "== 3. webserve serves the site =="
/tmp/webserve -addr 127.0.0.1:15091 &
WS=$!
trap 'kill $PBX $SRV $WS 2>/dev/null || true' EXIT
sleep 0.5
for path in / /app.js /phone.wasm /wasm_exec.js /manifest.webmanifest; do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:15091$path")
  [ "$CODE" = "200" ] && echo "  PASS: GET $path" || { echo "  FAIL: GET $path ($CODE)"; FAILED=1; }
done
CTYPE=$(curl -s -o /dev/null -w '%{content_type}' http://127.0.0.1:15091/phone.wasm)
echo "$CTYPE" | grep -q 'wasm' && echo "  PASS: wasm mime ($CTYPE)" || { echo "  FAIL: wasm mime ($CTYPE)"; FAILED=1; }

echo
if [ "${FAILED:-0}" = "0" ]; then
  echo "WEB E2E COMPLETE: ALL CHECKS PASSED"
else
  echo "WEB E2E COMPLETE: FAILURES PRESENT"
  exit 1
fi

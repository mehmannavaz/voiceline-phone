#!/bin/bash
# End-to-end test of the voiceline-phone VCP client against a REAL
# voiceline server (v0.11.0+, protocol v1.2) and the mockpbx SIP
# simulator:
#
#   vcpc (VCP client) <-ws:15086-> voiceline <-SIP-> mockpbx (fake provider)
#
# Covers: auth, line info, outbound call with two-way audio, DTMF,
# incoming call accepted, incoming rejected, conference, transfer.
set -euo pipefail

DIR=/tmp/vcpc-e2e
rm -rf "$DIR"; mkdir -p "$DIR"
cd /home/z/my-project/voiceline-phone

VL=/home/z/my-project/voiceline
GO=/home/z/go/bin/go
export PATH=/home/z/go/bin:/home/z/gopath/bin:$PATH GOPATH=/home/z/gopath

echo "== building =="
(cd "$VL" && $GO build -o "$DIR/voiceline" ./cmd/voiceline)
(cd "$VL" && $GO build -o "$DIR/mockpbx" ./cmd/mockpbx)
$GO build -o "$DIR/vcpc" ./cmd/vcpc

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

echo "== booting =="
"$DIR/mockpbx" -sip 127.0.0.1:15090 -http 127.0.0.1:15099 \
  -engine 127.0.0.1:15060 -auth 1001:s3cret > "$DIR/mockpbx.log" 2>&1 &
PBX=$!
"$DIR/voiceline" -config "$DIR/config.yaml" serve > "$DIR/voiceline.log" 2>&1 &
SRV=$!
trap 'kill $PBX $SRV 2>/dev/null || true' EXIT
sleep 2

pbxget() { curl -s "http://127.0.0.1:15099$1"; }
pbxpost() { curl -s -X POST "http://127.0.0.1:15099$1"; }

# Wait for the line to register at the mock PBX.
for i in $(seq 1 50); do
  N=$(pbxget /status | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["registrations"]))' 2>/dev/null || echo 0)
  [ "$N" -ge 1 ] && break
  sleep 0.2
done
echo "line registered at mockpbx: $([ "$N" -ge 1 ] && echo yes || echo NO)"

check() { # name, file, pattern
  if grep -q "$3" "$2"; then
    echo "  PASS: $1"
  else
    echo "  FAIL: $1 (missing: $3)"
    FAILED=1
  fi
}
FAILED=0

echo "== 1. outbound call with two-way audio =="
"$DIR/vcpc" -url ws://127.0.0.1:15086/call -line main -password "$PW" \
  -dial 15005554117 -seconds 4 -tone -display "E2E Caller" > "$DIR/out1.jsonl" 2>"$DIR/out1.err" || true
check "auth + line info" "$DIR/out1.jsonl" '"type":"line"'
check "dialing event"    "$DIR/out1.jsonl" '"state":"dialing"'
check "answered event"   "$DIR/out1.jsonl" '"state":"answered"'
check "ended event"      "$DIR/out1.jsonl" '"state":"ended"'
check "uplink audio"     "$DIR/out1.jsonl" '"uplink_frames":[1-9]'
check "downlink audio"   "$DIR/out1.jsonl" '"downlink_frames":[1-9]'
# The PBX leg saw RTP.
RTP=$(pbxget /status | python3 -c '
import json,sys
s=json.load(sys.stdin)
legs=s.get("legs") or {}
print(sum(l.get("rtp_packets",0) for l in legs.values() if l.get("answered")))')
echo "  pbx leg rtp packets: $RTP"
[ "$RTP" -gt 50 ] && echo "  PASS: pbx received RTP" || { echo "  FAIL: pbx received RTP"; FAILED=1; }
pbxpost /reset > /dev/null

echo "== 2. incoming call accepted =="
"$DIR/vcpc" -url ws://127.0.0.1:15086/call -line main -password "$PW" \
  -receive -seconds 3 -tone -wait 10s > "$DIR/inc1.jsonl" 2>"$DIR/inc1.err" &
CLI=$!
sleep 1.5
pbxpost '/call?to=1001' > "$DIR/inc1.call.json"
wait $CLI || true
check "incoming offer"    "$DIR/inc1.jsonl" '"type":"incoming"'
check "incoming accepted" "$DIR/inc1.jsonl" '"state":"answered"'
check "incoming audio"    "$DIR/inc1.jsonl" '"downlink_frames"'
check "incoming ended"    "$DIR/inc1.jsonl" '"state":"ended"'
pbxpost /reset > /dev/null

echo "== 3. incoming call rejected =="
"$DIR/vcpc" -url ws://127.0.0.1:15086/call -line main -password "$PW" \
  -receive -reject -wait 6s > "$DIR/inc2.jsonl" 2>"$DIR/inc2.err" &
CLI=$!
sleep 1.5
pbxpost '/call?to=1001' > "$DIR/inc2.call.json"
wait $CLI || true
check "incoming offer (reject)" "$DIR/inc2.jsonl" '"type":"incoming"'
check "rejected => ended"       "$DIR/inc2.jsonl" '"detail":"declined"'
pbxpost /reset > /dev/null

echo "== 4. conference: add a person to the call =="
"$DIR/vcpc" -url ws://127.0.0.1:15086/call -line main -password "$PW" \
  -dial 15005550001 -conf 15005550002 -seconds 6 -tone > "$DIR/conf1.jsonl" 2>"$DIR/conf1.err" || true
check "conference joined" "$DIR/conf1.jsonl" '"action":"joined"'
check "conference participants" "$DIR/conf1.jsonl" '"participants":3'
pbxpost /reset > /dev/null

echo "== 5. transfer to an outside number =="
"$DIR/vcpc" -url ws://127.0.0.1:15086/call -line main -password "$PW" \
  -dial 15005551001 -transfer 15005551002 -tone > "$DIR/tr1.jsonl" 2>"$DIR/tr1.err" || true
check "transfer dialing"  "$DIR/tr1.jsonl" '"type":"transfer"'
check "transfer answered" "$DIR/tr1.jsonl" '"state":"answered"'
check "client released"   "$DIR/tr1.jsonl" 'transferred'
pbxpost /reset > /dev/null

echo "== 6. DTMF toward the callee =="
"$DIR/vcpc" -url ws://127.0.0.1:15086/call -line main -password "$PW" \
  -dial 15005552001 -dtmf 12 -seconds 3 -tone > "$DIR/dtmf1.jsonl" 2>"$DIR/dtmf1.err" || true
check "dtmf call completed" "$DIR/dtmf1.jsonl" '"state":"ended"'
check "dtmf call answered"  "$DIR/dtmf1.jsonl" '"state":"answered"'
pbxpost /reset > /dev/null

echo "== server log sanity =="
grep -c 'webcall session opened' "$DIR/voiceline.log" || true
grep -c 'incoming call offered'  "$DIR/voiceline.log" || true

echo
if [ "$FAILED" = "0" ]; then
  echo "E2E COMPLETE: ALL CHECKS PASSED"
else
  echo "E2E COMPLETE: FAILURES PRESENT"
  exit 1
fi

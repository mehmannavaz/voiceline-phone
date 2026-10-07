// worklet_test.mjs — drives the real web worklets under a stubbed
// AudioWorkletGlobalScope and verifies the DSP numerically:
//
//   1. capture rate: 48 kHz in → exactly 8 kHz of 160-sample frames out
//   2. anti-alias wall: a 300 Hz tone passes; a 10 kHz tone is crushed
//      (without the filter it would alias to ~2 kHz hiss)
//   3. high-pass: DC is removed
//   4. mute = zero frames that still flow at 20 ms cadence
//   5. playback resample: 440 Hz @8 kHz → 440 Hz @48 kHz, no NaNs
//   6. jitter discipline: burst of seconds is shed — never more than
//      320 ms buffered (latency cannot grow into seconds)
//   7. soft limiter: |out| never exceeds 1 even at 4x gain
//   8. flush clears buffered audio
//
// Run: node scripts/worklet_test.mjs
import { readFileSync } from "node:fs";

let failures = 0;
function check(name, ok, detail) {
  if (ok) console.log(`  ok    ${name}${detail ? " — " + detail : ""}`);
  else { console.error(`  FAIL  ${name}${detail ? " — " + detail : ""}`); failures++; }
}

function makeScope(sampleRate) {
  const processors = {};
  return {
    sampleRate,
    registerProcessor: (name, ctor) => { processors[name] = ctor; },
    AudioWorkletProcessor: class {
      constructor() {
        this.port = {
          postMessage: (m) => outbox.push(m),
          onmessage: null,
        };
        const outbox = [];
        this.port.__outbox = outbox;
      }
    },
    __processors: processors,
  };
}

function loadWorklet(file, sampleRate) {
  const src = readFileSync(new URL("../web/" + file, import.meta.url), "utf8");
  const scope = makeScope(sampleRate);
  // The worklet source references `sampleRate`, `registerProcessor` and
  // `AudioWorkletProcessor` as free identifiers — bind them as function
  // parameters so the stub scope is used.
  const evald = new Function(
    "sampleRate", "registerProcessor", "AudioWorkletProcessor", src);
  evald(sampleRate, scope.registerProcessor, scope.AudioWorkletProcessor);
  return scope;
}

function newNode(scope, name) {
  const ctor = scope.__processors[name];
  if (!ctor) throw new Error("processor not registered: " + name);
  const node = new ctor();
  // The real browser bridges two distinct port objects; the stub shares
  // one object, so capture the worklet-side handler before the test
  // installs its own collector on .onmessage.
  node.__workletOnMessage = node.port.onmessage;
  return node;
}

function flushOut(node) {
  const msgs = node.port.__outbox.splice(0);
  for (const m of msgs) if (node.port.onmessage) node.port.onmessage({ data: m });
}
function deliver(node, m) {
  const h = node.__workletOnMessage;
  if (h) h({ data: m });
}

function sine(freq, rate, seconds, amp = 0.8) {
  const n = Math.round(rate * seconds);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i++) out[i] = amp * Math.sin((2 * Math.PI * freq * i) / rate);
  return out;
}

function rms(buf, from = 0, to = buf.length) {
  let s = 0, n = 0;
  for (let i = from; i < to; i++) { s += buf[i] * buf[i]; n++; }
  return Math.sqrt(s / Math.max(1, n));
}

// Goertzel tone amplitude, windowed (long recursions lose precision —
// measure in 8192-sample windows and take the strongest hit)
function goertzel(buf, f, rateHz) {
  const W = 8192;
  let best = 0;
  for (let o = 0; o + W <= buf.length; o += W) {
    const w = (2 * Math.PI * f) / rateHz, cw = Math.cos(w);
    let s0 = 0, s1 = 0, s2 = 0;
    for (let i = o; i < o + W; i++) { s0 = buf[i] + 2 * cw * s1 - s2; s2 = s1; s1 = s0; }
    const mag2 = s1 * s1 + s2 * s2 - 2 * cw * s1 * s2;
    const amp = Math.sqrt(Math.max(0, mag2)) / (W / 2);
    if (amp > best) best = amp;
  }
  return best;
}

function runMic(rate, signal) {
  const scope = loadWorklet("mic-worklet.js", rate);
  const mic = newNode(scope, "mic-processor");
  const frames = [];
  mic.port.onmessage = (e) => frames.push(e.data);
  for (let o = 0; o + 128 <= signal.length; o += 128) {
    mic.process([[signal.subarray(o, o + 128)]]);
    flushOut(mic);
  }
  const total = frames.reduce((a, f) => a + f.pcm.length, 0);
  const flat = new Float32Array(total);
  let p = 0;
  for (const f of frames) for (let i = 0; i < f.pcm.length; i++) flat[p++] = f.pcm[i] / 32768;
  return { frames, flat, mic };
}

// ── 1–4: capture worklet at 48 kHz ─────────────────────────
console.log("mic-worklet (capture) @ 48 kHz:");
{
  const rate = 48000, sec = 10;

  const low = sine(300, rate, sec, 0.5);
  const high = sine(10000, rate, sec, 0.5);
  const mix = new Float32Array(low.length);
  for (let i = 0; i < low.length; i++) mix[i] = low[i] + high[i];

  const { frames, flat } = runMic(rate, mix);
  const samples = flat.length;
  check("frame cadence: 160-sample 20 ms frames",
    frames.length > 0 && frames.every((f) => f.pcm.length === 160),
    `${frames.length} frames`);
  check("output rate is exactly 8 kHz",
    Math.abs(samples - 8000 * sec) <= 8000 * sec * 0.001,
    `${samples} samples vs ${8000 * sec}`);

  const clean = runMic(rate, low).flat;
  check("anti-alias wall crushes 10 kHz (would alias to 2 kHz)",
    rms(flat) < rms(clean) * 1.35,
    `mix ${rms(flat).toFixed(4)} vs clean-300 Hz ${rms(clean).toFixed(4)} rms`);
  const e300 = goertzel(flat, 300, 8000);
  const e2k = goertzel(flat, 2000, 8000);
  check("residual at 2 kHz is >=20 dB below the 300 Hz tone",
    e2k < e300 / 10,
    `2 kHz ${e2k.toExponential(2)} vs 300 Hz ${e300.toExponential(2)}`);

  const dc = runMic(rate, new Float32Array(rate * 2).fill(0.7)).flat;
  check("high-pass removes DC",
    rms(dc, Math.floor(dc.length / 2)) < 0.02,
    `tail rms ${rms(dc, Math.floor(dc.length / 2)).toFixed(4)}`);

  const m = runMic(rate, low);
  deliver(m.mic, { mute: true });
  const before = m.frames.length;
  for (let o = 0; o + 128 <= low.length; o += 128) {
    m.mic.process([[low.subarray(o, o + 128)]]);
    flushOut(m.mic);
  }
  const silent = m.frames.slice(before);
  // the first post-mute frame may carry <20 ms of pre-mute tail from
  // the accumulator — everything after must be pure zeros
  check("mute emits zero frames without stopping",
    silent.length > 1 && silent.slice(1).every((f) => f.pcm.every((v) => v === 0)),
    `${silent.length} frames (first carries the pre-mute tail)`);

  check("no NaN in captured output", flat.every((v) => Number.isFinite(v)));
}

// ── 5–8: playback worklet at 48 kHz ─────────────────────────
console.log("playback-worklet (playout) @ 48 kHz:");
{
  const rate = 48000;

  // 440 Hz @8 kHz fed with a realistic ~100 ms lead over the playout
  const scope = loadWorklet("playback-worklet.js", rate);
  const pb = newNode(scope, "playback-processor");
  const stats = [];
  pb.port.onmessage = (e) => { if (e.data.ms !== undefined) stats.push(e.data); };
  const sec = 6;
  const src = sine(440, 8000, sec, 0.5);
  const out = new Float32Array(rate * sec + 4096);
  let outN = 0, fed = 0, underran = false;
  const quanta = Math.floor((rate * sec) / 128);
  for (let q = 0; q < quanta; q++) {
    while (fed < outN / 6 + 800 && fed + 160 <= src.length) {
      deliver(pb, { pcm: src.slice(fed, fed + 160) });
      fed += 160;
    }
    const io = [[new Float32Array(128)]];
    pb.process([], io);
    flushOut(pb);
    out.set(io[0][0], outN);
    outN += 128;
    if (stats.length && stats[stats.length - 1].underruns > 0) underran = true;
  }
  const played = out.subarray(0, outN);
  check("no NaN in playout", played.every((v) => Number.isFinite(v)));
  check("playout carries energy", rms(played, Math.floor(outN / 2)) > 0.1,
    `tail rms ${rms(played, Math.floor(outN / 2)).toFixed(4)}`);
  const tail = played.subarray(Math.floor(outN / 2));
  const e440 = goertzel(tail, 440, rate);
  const eOther = Math.max(goertzel(tail, 330, rate), goertzel(tail, 550, rate), goertzel(tail, 880, rate));
  check("440 Hz stays 440 Hz after the 8k->48k resample",
    e440 > eOther * 5,
    `440 Hz ${e440.toExponential(2)} vs neighbors ${eOther.toExponential(2)}`);
  check("steady-state feeding has no underruns", !underran,
    stats.length ? `last: ${JSON.stringify(stats[stats.length - 1])}` : "");

  // burst discipline: 3 seconds queued at once must not exceed the cap
  const scope2 = loadWorklet("playback-worklet.js", rate);
  const pb2 = newNode(scope2, "playback-processor");
  const stats2 = [];
  pb2.port.onmessage = (e) => { if (e.data.ms !== undefined) stats2.push(e.data); };
  deliver(pb2, { pcm: new Float32Array(8000 * 3).fill(0.2) });
  let maxMs = 0;
  for (let q = 0; q < 100; q++) {
    pb2.process([], [[new Float32Array(128)]]);
    flushOut(pb2);
    if (stats2.length) maxMs = Math.max(maxMs, stats2[stats2.length - 1].ms);
  }
  check("burst of 3 s is shed — never more than 320 ms buffered",
    maxMs <= 320,
    `peak ${maxMs} ms, dropped ${stats2.length ? stats2[stats2.length - 1].dropped : 0}`);

  // limiter: 4x gain on a full-scale tone cannot exceed 1
  const scope3 = loadWorklet("playback-worklet.js", rate);
  const pb3 = newNode(scope3, "playback-processor");
  deliver(pb3, { gain: 4 });
  deliver(pb3, { pcm: sine(300, 8000, 2, 1.0) });
  let peak = 0;
  for (let q = 0; q < 200; q++) {
    const io = [[new Float32Array(128)]];
    pb3.process([], io);
    flushOut(pb3);
    for (const v of io[0][0]) { const a = Math.abs(v); if (a > peak) peak = a; }
  }
  check("soft limiter holds |out| <= 1 at 4x gain", peak <= 1.0001, `peak ${peak.toFixed(4)}`);

  deliver(pb3, { flush: true });
  const io = [[new Float32Array(128)]];
  pb3.process([], io);
  check("flush clears buffered audio", io[0][0].every((v) => v === 0));
}

console.log(failures === 0 ? "\nALL WORKLET DSP CHECKS PASSED" : `\n${failures} CHECKS FAILED`);
process.exit(failures === 0 ? 0 : 1);

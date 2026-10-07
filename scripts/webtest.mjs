// Drives the REAL phone.wasm module under Node against a live voiceline
// server: connect, dial, receive audio, hang up — proving the website's
// Go core over the real wire (the browser-only audio worklets are stubbed).
import fs from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";

const here = path.dirname(fileURLToPath(import.meta.url));
await import(path.join(here, "..", "web", "wasm_exec.js")); // defines global.Go

const url = process.env.VCP_URL || "ws://127.0.0.1:15086/call";
const line = process.env.VCP_LINE || "main";
const password = process.env.VCP_PASSWORD || "e2e-CallPass-7Kmq2Ztv";
const dialTo = process.env.VCP_DIAL || "15005554117";
const expectIncoming = process.env.VCP_INCOMING === "1";

const events = [];
const log = (t, d) => { events.push({ t, d }); console.log(JSON.stringify({ ts: new Date().toISOString(), type: t, data: d })); };

const callbacks = {
  onConnected: (u, l) => log("connected", { url: u, line: l }),
  onLine: (l) => log("line", l),
  onCall: (s) => log("call", s),
  onIncoming: (inc) => log("incoming", inc),
  onDtmf: (id, d, m) => log("dtmf", { id, d, m }),
  onTransfer: (p) => log("transfer", p),
  onConference: (p) => log("conference", p),
  onAudio: (id, arr) => { audioFrames++; },
  onBye: (r, c) => log("bye", { r, c }),
  onClosed: (e) => { log("closed", { error: e }); finish(); },
  onError: (e) => { log("error", { error: e }); process.exitCode = 1; finish(); },
};
let audioFrames = 0;
let done = false;
let timer = setTimeout(() => { console.error("webtest: timeout"); process.exit(1); }, 40000);

function finish() {
  if (done) return;
  done = true;
  clearTimeout(timer);
  setTimeout(() => process.exit(0), 300);
}

const go = new Go();
const wasm = fs.readFileSync(path.join(here, "..", "web", "phone.wasm"));
const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
go.run(instance);

globalThis.VCP.init(callbacks);
globalThis.VCP.connect(url, line, password, "Web E2E", expectIncoming);

// Wait for connection, then dial.
await new Promise((res) => {
  const iv = setInterval(() => {
    if (events.some((e) => e.t === "connected")) { clearInterval(iv); res(); }
  }, 50);
});

if (!expectIncoming) {
  globalThis.VCP.dial(dialTo);
  // Hang up after ~4 s of talk.
  setTimeout(() => globalThis.VCP.hangup(dialId()), 4000);
} else {
  // Incoming mode: accept whatever arrives.
  const iv = setInterval(() => {
    const inc = events.find((e) => e.t === "incoming");
    if (inc) {
      clearInterval(iv);
      globalThis.VCP.accept(inc.d.call_id);
      setTimeout(() => globalThis.VCP.hangup(inc.d.call_id), 4000);
    }
  }, 100);
}

function dialId() {
  const ev = events.find((e) => e.t === "call" && e.d.state === "dialing");
  return ev ? ev.d.id : "";
}

// Summary after the run.
setTimeout(() => {
  const states = events.filter((e) => e.t === "call").map((e) => e.d.state);
  const ok = states.includes("answered") && states.includes("ended") &&
    (!expectIncoming || events.some((e) => e.t === "incoming")) &&
    (expectIncoming || audioFrames >= 5);
  console.error(`webtest summary: states=[${states}] audioFrames=${audioFrames} => ${ok ? "PASS" : "FAIL"}`);
  if (!ok) process.exitCode = 1;
  finish();
}, 12000);

// voiceline-phone web client — UI + audio glue. The protocol itself is
// the Go vcp package compiled to phone.wasm (window.VCP).
"use strict";

const $ = (id) => document.getElementById(id);
const state = {
  connected: false,
  muted: false,
  calls: new Map(),      // id -> snapshot
  incoming: null,        // current incoming offer
  audioCtx: null,
  playNode: null,
  micNode: null,
  ringOsc: null,
  vcpReady: false,
  vol: 1.0,              // far-end volume 0..3 (1.0 = natural level)
  stats: { ms: 0, dropped: 0, underruns: 0 },
  framesIn: 0,
  framesOut: 0,
};

// ── persistence ──────────────────────────────────────────
const store = {
  load() {
    try { return JSON.parse(localStorage.getItem("voiceline-phone") || "{}"); } catch { return {}; }
  },
  save(o) { localStorage.setItem("voiceline-phone", JSON.stringify(o)); },
};

// ── boot ─────────────────────────────────────────────────
window.addEventListener("load", () => {
  const saved = store.load();
  $("server").value = saved.server || "";
  $("line").value = saved.line || "";
  $("display").value = saved.display || "";
  if (saved.keep) {
    $("keep").checked = true;
    $("password").value = saved.password || "";
  }
  $("receive").checked = saved.receive !== false;

  buildKeypad();
  bindEvents();
  restoreVolume();

  const go = new Go();
  WebAssembly.instantiateStreaming(fetch("phone.wasm"), go.importObject)
    .then((res) => {
      go.run(res.instance);
      state.vcpReady = true;
      window.VCP.init({
        onLine: cbLine,
        onCall: cbCall,
        onIncoming: cbIncoming,
        onDtmf: cbDtmf,
        onTransfer: cbTransfer,
        onConference: cbConference,
        onAudio: cbAudio,
        onBye: cbBye,
        onClosed: cbClosed,
        onError: cbError,
        onConnected: cbConnected,
      });
    })
    .catch((err) => loginFail("cannot load phone.wasm: " + err));
});

// ── login ────────────────────────────────────────────────
$("loginForm").addEventListener("submit", (ev) => {
  ev.preventDefault();
  if (!state.vcpReady) { loginFail("the protocol module is still loading…"); return; }
  const server = normServer($("server").value.trim());
  const line = $("line").value.trim();
  const password = $("password").value;
  if (!line || !password) { loginFail("line id and call password are required"); return; }
  if (!server) { loginFail("server is required"); return; }

  const keep = $("keep").checked;
  store.save({
    server, line,
    display: $("display").value.trim(),
    receive: $("receive").checked,
    keep,
    password: keep ? password : "",
  });

  $("connectBtn").disabled = true;
  $("connectBtn").textContent = "Connecting…";
  startAudio(); // user gesture: unlock the AudioContext now
  window.VCP.connect(server, line, password, $("display").value.trim(), $("receive").checked);
});

function normServer(s) {
  if (!s) return "";
  if (s.startsWith("ws://") || s.startsWith("wss://")) {
    return s.endsWith("/call") ? s : s + "/call";
  }
  return "ws://" + s + "/call";
}

function loginFail(msg) {
  $("connectBtn").disabled = false;
  $("connectBtn").textContent = "Connect";
  const st = $("loginStatus");
  st.hidden = false;
  st.textContent = msg;
}

function cbConnected(url, line) {
  state.connected = true;
  $("loginStatus").hidden = true;
  $("connectBtn").disabled = false;
  $("connectBtn").textContent = "Connect";
  $("login").hidden = true;
  $("phone").hidden = false;
  note("connected — dial a number, or wait for the line to ring");
}

function cbError(msg) { loginFail(msg); showLogin(); }

function showLogin() {
  state.connected = false;
  stopRing();
  state.calls.clear();
  renderCalls();
  $("phone").hidden = true;
  $("login").hidden = false;
}

function cbClosed(err) {
  if (err) loginFail(err);
  showLogin();
}

// ── header / actions ─────────────────────────────────────
function bindEvents() {
  $("disconnectBtn").addEventListener("click", () => window.VCP.disconnect());
  $("callBtn").addEventListener("click", () => {
    const n = $("number").value.trim();
    if (!n) { note("type a number first"); return; }
    window.VCP.dial(n);
  });
  $("muteBtn").addEventListener("click", () => {
    state.muted = !state.muted;
    // The worklet emits zero frames while muted, so the 20 ms cadence,
    // sequence numbers and the server pacer all stay in sync.
    if (state.micNode) state.micNode.port.postMessage({ mute: state.muted });
    $("muteBtn").textContent = state.muted ? "🎙 off" : "🎙 on";
    $("muteBtn").classList.toggle("on", !state.muted);
  });
  $("vol").addEventListener("input", () => setVolume(parseFloat($("vol").value), true));
  $("speaker").addEventListener("change", () => setSink($("speaker").value, true));
  $("hangAllBtn").addEventListener("click", () => {
    for (const [id, s] of state.calls) {
      if (isLive(s)) window.VCP.hangup(id);
    }
  });
  $("acceptBtn").addEventListener("click", () => {
    if (state.incoming) { stopRing(); window.VCP.accept(state.incoming.call_id); hideIncoming(); }
  });
  $("rejectBtn").addEventListener("click", () => {
    if (state.incoming) { stopRing(); window.VCP.reject(state.incoming.call_id); hideIncoming(); }
  });
  $("number").addEventListener("keydown", (ev) => {
    if (ev.key === "Enter") { ev.preventDefault(); $("callBtn").click(); }
  });
}

function buildKeypad() {
  const keys = [
    ["1", ""], ["2", "ABC"], ["3", "DEF"],
    ["4", "GHI"], ["5", "JKL"], ["6", "MNO"],
    ["7", "PQRS"], ["8", "TUV"], ["9", "WXYZ"],
    ["*", ""], ["0", "+"], ["#", ""],
  ];
  const pad = $("keypad");
  for (const [k, sub] of keys) {
    const b = document.createElement("button");
    b.innerHTML = k + (sub ? `<small>${sub}</small>` : "");
    b.addEventListener("click", () => { $("number").value += k; });
    pad.appendChild(b);
  }
}

// ── protocol callbacks ───────────────────────────────────
function cbLine(l) {
  $("lineNumber").textContent = (l.display_name ? l.display_name + " · " : "") + l.number;
  let stat = `line ${l.id} · ${l.reg_state} · ${hookLabel(l.hook)}`;
  stat += l.max_concurrent > 0
    ? ` · ${l.active_calls}/${l.max_concurrent} calls`
    : ` · ${l.active_calls} live`;
  $("lineStat").textContent = stat;
}

function hookLabel(h) {
  return h === "on_hook" ? "idle" : h === "off_hook" ? "call up" : h === "disconnected" ? "no registration" : h;
}

function cbCall(s) {
  const prev = state.calls.get(s.id);
  state.calls.set(s.id, s);
  if (s.state === "ended" || s.state === "failed") stopRing();
  // Entering the answered phase: flush any stale buffered audio and
  // re-prime the jitter buffer for a clean, low-latency start.
  if (s.state === "answered" && (!prev || prev.state !== "answered") && state.playNode) {
    state.playNode.port.postMessage({ flush: true });
  }
  renderCalls();
}

function cbIncoming(inc) {
  state.incoming = inc;
  const who = inc.from_display ? `${inc.from_display} (${inc.from})` : inc.from;
  $("incomingWho").textContent = "☎ " + who;
  $("incomingHint").textContent = `to ${inc.to} — ring window ${inc.ring_sec}s`;
  $("incomingBanner").hidden = false;
  startRing();
}

function hideIncoming() {
  state.incoming = null;
  $("incomingBanner").hidden = true;
  stopRing();
}

function cbDtmf(callID, digit, method) { note(`digit “${digit}” from the far end (${method})`); }

function cbTransfer(p) { note(`transfer ${p.state}${p.detail ? ": " + p.detail : ""}`); }

function cbConference(p) {
  note(p.action === "joined"
    ? `${p.participant} joined the call (${p.participants} in the room)`
    : `${p.participant} left the call (${p.participants} remain)`);
}

function cbBye(reason) { note(`server closed the session: ${reason}`); }

function cbAudio(callID, pcm) {
  if (state.playNode) {
    state.playNode.port.postMessage({ pcm });
    state.framesIn++;
  }
}

// ── calls rendering ──────────────────────────────────────
function isLive(s) {
  return ["dialing", "ringing", "answered", "incoming"].includes(s.state);
}

function titleOf(s) {
  const dir = s.direction === "in" ? "↙" : "↗";
  let who = s.direction === "in" ? (s.from_display || s.from || "") : s.to;
  if (!who && s.id) who = s.id.slice(0, 8);
  return `${dir} ${who}`;
}

function stateLabel(s) {
  switch (s.state) {
    case "incoming": return "incoming — ringing";
    case "dialing": return "dialing…";
    case "ringing": return "ringing…";
    case "answered": return s.media ? "connected — audio flowing" : "answered";
    case "ended": return "ended";
    case "failed": return "failed";
    default: return s.state;
  }
}

function fmtDur(ms) {
  const sec = Math.max(0, Math.floor(ms / 1000));
  return String(Math.floor(sec / 60)).padStart(2, "0") + ":" + String(sec % 60).padStart(2, "0");
}

function renderCalls() {
  const box = $("calls");
  box.innerHTML = "";
  const list = [...state.calls.values()];
  list.sort((a, b) => orderScore(a.state) - orderScore(b.state) || (a.id < b.id ? -1 : 1));
  if (list.length === 0) {
    box.innerHTML = '<div class="empty">no calls yet</div>';
    return;
  }
  for (const s of list) {
    const card = document.createElement("div");
    card.className = "call" + (isLive(s) ? " live" : "") + (s.state === "incoming" ? " incoming-card" : "");

    const head = document.createElement("div");
    head.className = "call-head";
    const who = document.createElement("div");
    who.className = "call-who";
    who.textContent = titleOf(s);
    const dur = document.createElement("div");
    dur.className = "call-dur";
    if (s.state === "answered") {
      dur.textContent = fmtDur(Date.now() - Date.parse(s.answered_at));
      dur.dataset.call = s.id;
    }
    head.append(who, dur);

    const st = document.createElement("div");
    st.className = "call-state" + (s.state === "failed" ? " failed" : "");
    st.textContent = stateLabel(s) + (s.detail && !["incoming"].includes(s.state) ? ` — ${s.detail}` : "");

    const actions = document.createElement("div");
    actions.className = "call-actions";
    addActions(actions, s);

    card.append(head, st, actions);
    box.appendChild(card);
  }
}

function addActions(box, s) {
  const btn = (label, cls, fn) => {
    const b = document.createElement("button");
    b.textContent = label;
    if (cls) b.className = cls;
    b.addEventListener("click", fn);
    return b;
  };
  switch (s.state) {
    case "incoming":
      box.append(
        btn("Accept", "primary", () => { stopRing(); window.VCP.accept(s.id); hideIncoming(); }),
        btn("Reject", "danger", () => { stopRing(); window.VCP.reject(s.id); hideIncoming(); }),
      );
      break;
    case "dialing":
    case "ringing":
      box.append(btn("Cancel", "danger", () => window.VCP.hangup(s.id)));
      break;
    case "answered":
      box.append(
        btn("Keys", "ghost", () => dtmfSheet(s.id)),
        btn("Transfer", "ghost", () => promptAction(`Transfer the caller to`, (to) => window.VCP.transfer(s.id, to))),
        btn("Add person", "ghost", () => promptAction(`Add a person to the call`, (to) => window.VCP.conference(s.id, to))),
        btn("Hang up", "danger", () => window.VCP.hangup(s.id)),
      );
      break;
    default:
      break;
  }
}

function promptAction(title, fn) {
  const to = window.prompt(title + "\n(number or SIP URI)");
  if (to && to.trim()) fn(to.trim());
}

function dtmfSheet(callID) {
  const sheet = document.createElement("div");
  sheet.className = "sheet";
  const inner = document.createElement("div");
  inner.className = "inner";
  const sent = document.createElement("div");
  sent.className = "note";
  sent.textContent = " ";
  const grid = document.createElement("div");
  grid.className = "keypad";
  for (const d of ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"]) {
    const b = document.createElement("button");
    b.textContent = d;
    b.addEventListener("click", () => {
      window.VCP.dtmf(callID, d);
      sent.textContent = (sent.textContent.trim() + d).slice(-12);
    });
    grid.appendChild(b);
  }
  const close = document.createElement("button");
  close.textContent = "Close";
  close.className = "primary big";
  close.addEventListener("click", () => sheet.remove());
  sheet.addEventListener("click", (ev) => { if (ev.target === sheet) sheet.remove(); });
  inner.append(sent, grid, close);
  sheet.appendChild(inner);
  document.body.appendChild(sheet);
}

// live durations
setInterval(() => {
  document.querySelectorAll(".call-dur[data-call]").forEach((el) => {
    const s = state.calls.get(el.dataset.call);
    if (s && s.state === "answered") {
      el.textContent = fmtDur(Date.now() - Date.parse(s.answered_at));
    }
  });
}, 1000);

// ── note ─────────────────────────────────────────────────
let noteTimer = null;
function note(msg) {
  const el = $("note");
  el.hidden = false;
  el.textContent = msg;
  clearTimeout(noteTimer);
  noteTimer = setTimeout(() => { el.hidden = true; }, 6000);
}

// ── audio ────────────────────────────────────────────────
async function startAudio() {
  if (state.audioCtx) return;
  try {
    // "interactive" = the smallest buffer the device allows: keeps the
    // end-to-end path at tens of milliseconds instead of hundreds.
    const ctx = new (window.AudioContext || window.webkitAudioContext)({ latencyHint: "interactive" });
    await ctx.resume();
    await ctx.audioWorklet.addModule("playback-worklet.js");
    await ctx.audioWorklet.addModule("mic-worklet.js");

    const playNode = new AudioWorkletNode(ctx, "playback-processor", {
      numberOfInputs: 0, numberOfOutputs: 1, outputChannelCount: [1],
    });
    playNode.connect(ctx.destination);
    playNode.port.postMessage({ gain: state.vol });
    playNode.port.onmessage = (e) => {
      if (e.data && e.data.ms !== undefined) state.stats = e.data;
    };

    let micNode = null;
    try {
      // The browser's own AEC / noise suppression / AGC run on this
      // track; the worklet adds the anti-alias wall for the 8 kHz leg.
      let stream;
      try {
        stream = await navigator.mediaDevices.getUserMedia({
          audio: {
            echoCancellation: true,
            noiseSuppression: true,
            autoGainControl: true,
            channelCount: 1,
          },
        });
      } catch {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      }
      const src = ctx.createMediaStreamSource(stream);
      micNode = new AudioWorkletNode(ctx, "mic-processor", {
        numberOfInputs: 1, numberOfOutputs: 0,
      });
      micNode.port.onmessage = (e) => {
        if (e.data && e.data.pcm) {
          // Mute is applied inside the worklet (zero frames), so this
          // always forwards — cadence and sequence numbers stay honest.
          window.VCP.mic(e.data.pcm);
          state.framesOut++;
        }
      };
      src.connect(micNode);
    } catch (err) {
      note("microphone unavailable — you can still hear the call: " + err.message);
    }

    state.audioCtx = ctx;
    state.playNode = playNode;
    state.micNode = micNode;
    if (state.muted && micNode) micNode.port.postMessage({ mute: true });
    await refreshSpeakers(); // labels exist once mic permission is granted
    await restoreSink();     // route to the saved headphones/handset
  } catch (err) {
    note("audio failed to start: " + err.message);
  }
}

// ── volume: far-end loudness, natural level by default ───
function setVolume(v, save) {
  if (!isFinite(v)) v = 1;
  state.vol = Math.max(0, Math.min(3, v));
  $("vol").value = String(state.vol);
  $("volLabel").textContent = Math.round(state.vol * 100) + "%";
  if (state.playNode) state.playNode.port.postMessage({ gain: state.vol });
  if (save) { try { localStorage.setItem("vlphone.vol", String(state.vol)); } catch {} }
}
function restoreVolume() {
  let v = 1.0;
  try { const s = parseFloat(localStorage.getItem("vlphone.vol")); if (s >= 0 && s <= 3) v = s; } catch {}
  setVolume(v, false);
}

// ── output device: route the call to headphones, not the blaring speakers
async function refreshSpeakers() {
  const sel = $("speaker");
  const ctx = state.audioCtx;
  if (!sel || !ctx) return;
  if (!navigator.mediaDevices || !navigator.mediaDevices.enumerateDevices ||
      typeof ctx.setSinkId !== "function") {
    sel.closest(".audio-bar").hidden = true;
    return;
  }
  let devs = [];
  try { devs = (await navigator.mediaDevices.enumerateDevices()).filter(d => d.kind === "audiooutput"); } catch {}
  const saved = sel.value || "";
  sel.innerHTML = "";
  const def = document.createElement("option");
  def.value = "";
  def.textContent = "🔊 default output";
  sel.appendChild(def);
  for (const d of devs) {
    const o = document.createElement("option");
    o.value = d.deviceId;
    o.textContent = "🎧 " + (d.label || "output " + sel.length);
    sel.appendChild(o);
  }
  sel.value = saved && [...sel.options].some(o => o.value === saved) ? saved : "";
}

async function setSink(id, save) {
  const ctx = state.audioCtx;
  if (!ctx || typeof ctx.setSinkId !== "function") return;
  try {
    await ctx.setSinkId(id);
    if (save) { try { localStorage.setItem("vlphone.sink", id); } catch {} }
  } catch (err) {
    note("could not switch the audio output: " + err.message);
  }
}
async function restoreSink() {
  const ctx = state.audioCtx;
  if (!ctx || typeof ctx.setSinkId !== "function") return;
  let id = "";
  try { id = localStorage.getItem("vlphone.sink") || ""; } catch {}
  if (id) {
    await setSink(id, false);
    const sel = $("speaker");
    if (sel && [...sel.options].some(o => o.value === id)) sel.value = id;
  }
}
if (navigator.mediaDevices && navigator.mediaDevices.addEventListener) {
  navigator.mediaDevices.addEventListener("devicechange", () => { refreshSpeakers(); });
}

// live connection-quality readout
setInterval(() => {
  const el = $("audioStat");
  if (!el) return;
  const s = state.stats;
  el.textContent = `jitter ${s.ms} ms · dropped ${s.dropped} · gaps ${s.underruns}`;
}, 500);

// ── ringtone (two classic tones, oscillator-based) ───────
function startRing() {
  stopRing();
  if (!state.audioCtx) return;
  const ctx = state.audioCtx;
  const ringGain = 0.12 * Math.min(1, state.vol); // follows the volume slider
  const gain = ctx.createGain();
  gain.gain.value = ringGain;
  gain.connect(ctx.destination);
  const o1 = ctx.createOscillator();
  o1.frequency.value = 440;
  const o2 = ctx.createOscillator();
  o2.frequency.value = 480;
  o1.connect(gain); o2.connect(gain);

  const t = ctx.currentTime;
  gain.gain.setValueAtTime(ringGain, t);
  gain.gain.setValueAtTime(ringGain, t + 1.0);
  gain.gain.setValueAtTime(0.0, t + 1.02);
  gain.gain.setValueAtTime(0.0, t + 2.98);
  gain.gain.setValueAtTime(ringGain, t + 3.0);
  gain.gain.setValueAtTime(ringGain, t + 4.0);
  gain.gain.setValueAtTime(0.0, t + 4.02);

  o1.start(); o2.start();
  state.ringOsc = { o1, o2, gain };
  state.ringTimer = setInterval(() => {
    if (!state.ringOsc) return;
    const t2 = ctx.currentTime;
    const g = state.ringOsc.gain.gain;
    g.setValueAtTime(ringGain, t2);
    g.setValueAtTime(ringGain, t2 + 1.0);
    g.setValueAtTime(0.0, t2 + 1.02);
    g.setValueAtTime(0.0, t2 + 2.98);
    g.setValueAtTime(ringGain, t2 + 3.0);
    g.setValueAtTime(ringGain, t2 + 4.0);
    g.setValueAtTime(0.0, t2 + 4.02);
  }, 6000);
}

function stopRing() {
  if (state.ringTimer) { clearInterval(state.ringTimer); state.ringTimer = null; }
  if (state.ringOsc) {
    try {
      state.ringOsc.o1.stop();
      state.ringOsc.o2.stop();
      state.ringOsc.gain.disconnect();
    } catch {}
    state.ringOsc = null;
  }
}

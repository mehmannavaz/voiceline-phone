// mic-worklet — broadcast-grade microphone capture.
//
// Pipeline (all inside the audio thread, zero main-thread involvement):
//   1. stereo → mono average
//   2. one-pole high-pass @ 140 Hz   — kills rumble / DC / handling noise
//   3. 3× biquad low-pass @ 3.4 kHz  — 6th-order Butterworth anti-alias
//      wall: without it every frequency above 4 kHz aliases straight back
//      into the voice band as hiss ("noisy" calls)
//   4. drift-free fractional decimation to 8 kHz (linear interpolation)
//   5. 160-sample (20 ms) Int16 frames → main thread
//
// Muting emits ZERO frames instead of stopping the stream, so the 20 ms
// cadence, sequence numbers and server-side pacer all stay in sync.
class MicProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.pos = 0;        // fractional input position for decimation
    this.acc = [];       // accumulated 8 kHz samples (Float32 numbers)
    this.muted = false;
    this.frameNo = 0;

    // one-pole high-pass state
    this.hpPrevIn = 0;
    this.hpPrevOut = 0;
    const rcHp = 1 / (2 * Math.PI * 140);
    this.hpA = rcHp / (rcHp + 1 / sampleRate);

    // 3 cascaded RBJ low-pass biquads = 6th-order Butterworth @ 3.4 kHz
    this.lps = [0.5176, 0.7071, 1.9319].map((q) => makeLPF(3400, q));
    this.f = new Float32Array(0); // filtered scratch for this block

    this.port.onmessage = (e) => {
      if (e.data && e.data.mute !== undefined) this.muted = !!e.data.mute;
    };

    function makeLPF(fc, q) {
      const w0 = (2 * Math.PI * fc) / sampleRate;
      const alpha = Math.sin(w0) / (2 * q);
      const cw = Math.cos(w0);
      const a0 = 1 + alpha;
      return {
        b0: (1 - cw) / 2 / a0, b1: (1 - cw) / a0, b2: (1 - cw) / 2 / a0,
        a1: (-2 * cw) / a0, a2: (1 - alpha) / a0,
        x1: 0, x2: 0, y1: 0, y2: 0,
      };
    }
  }

  process(inputs) {
    const input = inputs[0];
    if (!input || !input[0] || input[0].length === 0) return true;
    const chs = input;
    const n = chs[0].length;

    // 1. mono mix into the scratch buffer
    if (this.f.length !== n) this.f = new Float32Array(n);
    const f = this.f;
    const nc = chs.length;
    if (nc === 1) {
      for (let i = 0; i < n; i++) f[i] = chs[0][i];
    } else {
      for (let i = 0; i < n; i++) {
        let s = 0;
        for (let c = 0; c < nc; c++) s += chs[c][i];
        f[i] = s / nc;
      }
    }

    // 2. high-pass
    const aH = this.hpA;
    let pIn = this.hpPrevIn, pOut = this.hpPrevOut;
    for (let i = 0; i < n; i++) {
      const x = f[i];
      pOut = aH * (pOut + x - pIn);
      pIn = x;
      f[i] = pOut;
    }
    this.hpPrevIn = pIn;
    this.hpPrevOut = pOut;

    // 3. anti-alias cascade
    for (const lp of this.lps) {
      let { x1, x2, y1, y2 } = lp;
      const { b0, b1, b2, a1, a2 } = lp;
      for (let i = 0; i < n; i++) {
        const x = f[i];
        const y = b0 * x + b1 * x1 + b2 * x2 - a1 * y1 - a2 * y2;
        x2 = x1; x1 = x; y2 = y1; y1 = y;
        f[i] = y;
      }
      lp.x1 = x1; lp.x2 = x2; lp.y1 = y1; lp.y2 = y2;
    }

    // 4. fractional decimation to 8 kHz (position is continuous across
    // blocks, so there is no drift and no clicks at quantum boundaries)
    const step = sampleRate / 8000;
    let peak = 0;
    while (this.pos <= n - 2) {
      const i0 = this.pos | 0;
      const frac = this.pos - i0;
      const a = f[i0], b = f[i0 + 1];
      let v = a + (b - a) * frac;
      if (v > 1) v = 1; else if (v < -1) v = -1;
      if (v > peak) peak = v; else if (-v > peak) peak = -v;
      this.acc.push(this.muted ? 0 : v);
      this.pos += step;
    }
    this.pos -= n;
    if (this.pos < 0) this.pos = 0;

    // 5. emit 160-sample frames
    while (this.acc.length >= 160) {
      const pcm16 = new Int16Array(160);
      const frame = this.acc;
      let over = false;
      for (let i = 0; i < 160; i++) {
        let s = frame[i];
        if (s > 1) { s = 1; over = true; } else if (s < -1) { s = -1; over = true; }
        pcm16[i] = s < 0 ? s * 0x8000 : s * 0x7fff;
      }
      if (over) for (let i = 0; i < 160; i++) frame[i] = pcm16[i] / 32768;
      this.acc = frame.slice(160);
      this.frameNo++;
      this.port.postMessage(
        { pcm: pcm16, peak: this.frameNo % 3 === 0 ? peak : 0 },
        [pcm16.buffer],
      );
      peak = 0;
    }
    return true;
  }
}

registerProcessor("mic-processor", MicProcessor);

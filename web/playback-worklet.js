// playback-worklet — far-end playout with phone-grade jitter discipline.
//
//   · playout waits for a 64 ms start watermark, then runs continuously
//   · hard cap 320 ms: bursts shed the OLDEST audio back to 120 ms, so
//     latency can never grow into seconds (freshness > completeness for
//     live voice)
//   · underruns play brief silence and resume mid-stream — never a reset
//   · gain stage ends in a smooth soft limiter (linear below 0.85,
//     saturating toward ±1 above): boosting a quiet caller can never
//     clip or blast the speakers
//   · stats are throttled (~10/s) so the audio thread never floods the
//     main thread with messages
//
// Audio path: 8 kHz Int16 in → fractional linear resample to the context
// rate → gain → soft limit → out. The buffer is a flat deque (compact on
// push); the fractional read always interpolates between the first two
// valid samples, so there are no wrap edge cases and no clicks.
class PlaybackProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buf = new Float32Array(4096);
    this.start = 0;   // index of first valid sample
    this.len = 0;     // valid samples (buf[start .. start+len))
    this.frac = 0;    // fractional read position between buf[start] and buf[start+1]
    this.primed = false;
    this.gain = 1.0;
    this.dropped = 0;
    this.underruns = 0;
    this.statTick = 0;

    this.START = 512; // 64 ms before playout begins
    this.CAP = 2560;  // 320 ms hard ceiling
    this.SHED = 960;  // shed bursts back to 120 ms

    this.port.onmessage = (e) => {
      const d = e.data;
      if (d === null || typeof d !== "object") return;
      if (d.gain !== undefined) { this.gain = Math.max(0, Math.min(4, d.gain)); return; }
      if (d.flush) { this.start = 0; this.len = 0; this.frac = 0; this.primed = false; return; }
      if (d.pcm) this.push(d.pcm);
    };
  }

  push(pcm) {
    const n = pcm.length;
    // make room: compact, then grow only if the buffer is genuinely small
    if (this.start + this.len + n > this.buf.length) {
      this.buf.copyWithin(0, this.start, this.start + this.len);
      this.start = 0;
      if (this.len + n > this.buf.length) {
        const size = Math.max(4096, 1 << (32 - Math.clz32(this.len + n)));
        const next = new Float32Array(size);
        next.set(this.buf.subarray(0, this.len));
        this.buf = next;
      }
    }
    for (let i = 0; i < n; i++) this.buf[this.start + this.len + i] = pcm[i];
    this.len += n;

    if (this.len > this.CAP) {
      // Burst recovery: drop the OLDEST audio back to the shed level.
      const drop = Math.max(0, Math.min(this.len - this.SHED, this.len - 2));
      this.start += drop;
      this.len -= drop;
      this.dropped += drop;
    }
    if (!this.primed && this.len >= this.START) this.primed = true;
  }

  soft(x) {
    // linear below T, smoothly saturating to ±1 above — continuous,
    // zero lookahead, cannot clip
    const T = 0.85;
    const a = x < 0 ? -x : x;
    if (a <= T) return x;
    const over = (a - T) / (1 - T);
    const lim = T + (1 - T) * Math.tanh(over);
    return x < 0 ? -lim : lim;
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    const n = out.length;
    const ratio = 8000 / sampleRate;
    const g = this.gain;

    let i = 0;
    if (this.primed) {
      while (i < n) {
        if (this.len < 2) {
          // underrun: silence the rest of this quantum, resume when
          // audio arrives (playout never resets)
          for (; i < n; i++) out[i] = 0;
          this.underruns++;
          break;
        }
        const a = this.buf[this.start];
        const b = this.buf[this.start + 1];
        const s = (a + (b - a) * this.frac) * g;
        out[i] = this.soft(s);
        i++;
        this.frac += ratio;
        while (this.frac >= 1) {
          this.frac -= 1;
          this.start++;
          this.len--;
        }
      }
    }
    for (; i < n; i++) out[i] = 0;

    if (++this.statTick % 24 === 0) { // ~64 ms at 48 kHz
      this.port.postMessage({
        ms: Math.round(this.len / 8),
        dropped: this.dropped,
        underruns: this.underruns,
      });
    }
    return true;
  }
}

registerProcessor("playback-processor", PlaybackProcessor);

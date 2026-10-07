// playback-worklet: pulls 8 kHz PCM (as Float32 messages) from a jitter
// buffer and resamples to the AudioContext rate with linear interpolation.
class PlaybackProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.buf = [];          // queued Float32Array chunks (8 kHz)
    this.bufLen = 0;        // samples buffered
    this.maxBuf = 8000 * 0.25; // 250 ms cap — shed oldest beyond this
    this.pos = 0;           // fractional play position across chunks
    this.port.onmessage = (e) => {
      if (e.data && e.data.pcm) {
        this.buf.push(e.data.pcm);
        this.bufLen += e.data.pcm.length;
        while (this.bufLen > this.maxBuf && this.buf.length > 1) {
          this.bufLen -= this.buf.shift().length;
        }
      }
    };
  }

  process(_inputs, outputs) {
    const out = outputs[0][0];
    const step = sampleRate / 8000; // context Hz per phone Hz
    for (let i = 0; i < out.length; i++) {
      if (this.bufLen < 2) {
        out[i] = 0;
        continue;
      }
      // find the chunk holding this.pos
      let p = this.pos;
      let idx = 0;
      while (idx < this.buf.length - 1 && p >= this.buf[idx].length) {
        p -= this.buf[idx].length;
        idx++;
      }
      const chunk = this.buf[idx];
      const i0 = Math.floor(p);
      const frac = p - i0;
      const a = i0 < chunk.length ? chunk[i0] : 0;
      const b = i0 + 1 < chunk.length ? chunk[i0 + 1] : (this.buf[idx + 1] ? this.buf[idx + 1][0] : 0);
      out[i] = a + (b - a) * frac;

      this.pos += step;
      // retire fully consumed chunks
      while (this.buf.length > 0 && this.pos >= this.buf[0].length) {
        this.pos -= this.buf[0].length;
        this.bufLen -= this.buf[0].length;
        this.buf.shift();
      }
    }
    return true;
  }
}

registerProcessor('playback-processor', PlaybackProcessor);

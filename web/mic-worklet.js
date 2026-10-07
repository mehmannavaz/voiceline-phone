// mic-worklet: captures the microphone at the AudioContext rate, converts
// to mono, downsamples to 8 kHz and posts 160-sample (20 ms) Int16Array
// chunks to the main thread.
class MicProcessor extends AudioWorkletProcessor {
  constructor() {
    super();
    this.acc = [];          // accumulated 8 kHz samples
    this.pos = 0;           // fractional source position for downsampling
  }

  process(inputs) {
    const input = inputs[0];
    if (!input || !input[0]) {
      return true;
    }
    // average channels to mono
    const chs = input;
    const n = chs[0].length;
    const mono = new Float32Array(n);
    for (let c = 0; c < chs.length; c++) {
      const data = chs[c];
      for (let i = 0; i < n; i++) {
        mono[i] += data[i] / chs.length;
      }
    }

    // linear-interpolating downsample to 8 kHz
    const step = sampleRate / 8000;
    while (this.pos < n - 1) {
      const i0 = Math.floor(this.pos);
      const frac = this.pos - i0;
      const v = mono[i0] + (mono[i0 + 1] - mono[i0]) * frac;
      this.acc.push(Math.max(-1, Math.min(1, v)));
      this.pos += step;
      if (this.acc.length >= 160) {
        const frame = this.acc.splice(0, 160);
        const pcm16 = new Int16Array(160);
        for (let i = 0; i < 160; i++) {
          pcm16[i] = Math.round(frame[i] * 32000);
        }
        this.port.postMessage({ pcm: pcm16 }, []);
      }
    }
    this.pos -= n;
    return true;
  }
}

registerProcessor('mic-processor', MicProcessor);

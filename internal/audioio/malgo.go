// Package audioio drives the microphone and speakers through miniaudio
// (malgo): capture is delivered to the caller as PCM16 8 kHz mono, and
// playback mixes every live call's downlink with an optional ringtone.
package audioio

import (
	"encoding/binary"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/gen2brain/malgo"
)

const (
	sampleRate = 8000
	channels   = 1
	// bufSamples bounds one call's downlink buffer (400 ms): beyond that
	// the oldest audio is dropped so latency never snowballs.
	bufSamples = 3200
	// ringOnMS/ringOffMS shape the incoming-call ringtone.
	ringOnMS  = 1000
	ringOffMS = 2000
)

// Engine is the app's audio path.
type Engine struct {
	ctx     *malgo.AllocatedContext
	playDev *malgo.Device
	capDev  *malgo.Device

	mu      sync.Mutex
	sources map[string][]int16 // per-call downlink jitter buffers
	ringing bool
	ringAt  time.Time
	muted   bool
	onMic   func(pcm []int16)

	closed bool
}

// Start brings up both devices; onMic receives microphone chunks (about
// 20 ms each). It may be nil when only playback is wanted.
func Start(onMic func(pcm []int16)) (*Engine, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("audio: init: %w", err)
	}
	e := &Engine{ctx: ctx, sources: map[string][]int16{}, onMic: onMic}

	// Playback: the mixer fills each 20 ms period.
	pcfg := malgo.DefaultDeviceConfig(malgo.Playback)
	pcfg.SampleRate = sampleRate
	pcfg.PeriodSizeInMilliseconds = 20
	pcfg.Playback.Format = malgo.FormatS16
	pcfg.Playback.Channels = channels
	playDev, err := malgo.InitDevice(ctx.Context, pcfg, malgo.DeviceCallbacks{
		Data: func(out, _ []byte, frames uint32) {
			e.fill(out, int(frames))
		},
	})
	if err != nil {
		_ = ctx.Uninit()
		return nil, fmt.Errorf("audio: speakers: %w", err)
	}
	e.playDev = playDev

	// Capture: forward mic chunks when someone listens.
	ccfg := malgo.DefaultDeviceConfig(malgo.Capture)
	ccfg.SampleRate = sampleRate
	ccfg.PeriodSizeInMilliseconds = 20
	ccfg.Capture.Format = malgo.FormatS16
	ccfg.Capture.Channels = channels
	capDev, err := malgo.InitDevice(ctx.Context, ccfg, malgo.DeviceCallbacks{
		Data: func(_, in []byte, frames uint32) {
			e.mic(in, int(frames))
		},
	})
	if err != nil {
		playDev.Uninit()
		_ = ctx.Uninit()
		return nil, fmt.Errorf("audio: microphone: %w", err)
	}
	e.capDev = capDev

	if err := playDev.Start(); err != nil {
		e.Close()
		return nil, fmt.Errorf("audio: speakers start: %w", err)
	}
	if err := capDev.Start(); err != nil {
		e.Close()
		return nil, fmt.Errorf("audio: microphone start: %w", err)
	}
	return e, nil
}

// fill renders one playback period: the sum of every call's downlink
// plus the ringtone when a call rings, hard-clipped.
func (e *Engine) fill(out []byte, frames int) {
	n := frames * channels
	if n*2 > len(out) {
		n = len(out) / 2
	}
	mix := make([]int16, n)

	e.mu.Lock()
	for id, buf := range e.sources {
		take := n
		if take > len(buf) {
			take = len(buf)
		}
		for i := 0; i < take; i++ {
			mix[i] += buf[i]
		}
		if take == len(buf) {
			delete(e.sources, id)
		} else {
			e.sources[id] = buf[take:]
		}
	}
	if e.ringing {
		elapsed := time.Since(e.ringAt).Milliseconds()
		cycle := int64(ringOnMS + ringOffMS)
		if pos := elapsed % cycle; pos < ringOnMS {
			for i := 0; i < n; i++ {
				t := float64(e.ringAt.UnixNano())/1e9 + float64(pos)/1e3 + float64(i)/sampleRate
				v := 0.35 * (sin(2*pi*440*t) + sin(2*pi*480*t))
				mix[i] += int16(clamp(v * 32000 / 2))
			}
		}
	}
	e.mu.Unlock()

	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(mix[i]))
	}
}

// mic delivers captured audio to the listener unless muted.
func (e *Engine) mic(in []byte, frames int) {
	e.mu.Lock()
	muted := e.muted
	cb := e.onMic
	e.mu.Unlock()
	if muted || cb == nil {
		return
	}
	cnt := frames * channels
	if cnt*2 > len(in) {
		cnt = len(in) / 2
	}
	pcm := make([]int16, cnt)
	for i := 0; i < cnt; i++ {
		pcm[i] = int16(binary.LittleEndian.Uint16(in[i*2:]))
	}
	cb(pcm)
}

// Play queues downlink audio for one call.
func (e *Engine) Play(callID string, pcm []int16) {
	if len(pcm) == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	buf := append(e.sources[callID], pcm...)
	if len(buf) > bufSamples {
		buf = buf[len(buf)-bufSamples:]
	}
	e.sources[callID] = buf
}

// EndCall drops one call's queued audio.
func (e *Engine) EndCall(callID string) {
	e.mu.Lock()
	delete(e.sources, callID)
	e.mu.Unlock()
}

// SetRing starts or stops the ringtone.
func (e *Engine) SetRing(on bool) {
	e.mu.Lock()
	if on && !e.ringing {
		e.ringAt = time.Now()
	}
	e.ringing = on
	e.mu.Unlock()
}

// SetMuted silences the microphone path.
func (e *Engine) SetMuted(muted bool) {
	e.mu.Lock()
	e.muted = muted
	e.mu.Unlock()
}

// Muted reports the microphone state.
func (e *Engine) Muted() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.muted
}

// Close tears the devices down.
func (e *Engine) Close() {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.closed = true
	e.mu.Unlock()
	if e.capDev != nil {
		e.capDev.Uninit()
	}
	if e.playDev != nil {
		e.playDev.Uninit()
	}
	if e.ctx != nil {
		_ = e.ctx.Uninit()
	}
}

const pi = 3.141592653589793

func sin(x float64) float64 { return math.Sin(x) }

func clamp(v float64) float64 {
	if v > 32000 {
		return 32000
	}
	if v < -32000 {
		return -32000
	}
	return v
}

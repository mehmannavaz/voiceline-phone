// Command vcpc is a headless Voiceline Call Protocol client: it
// authenticates against a server, places or receives calls, streams
// audio, sends DTMF, transfers and conferences — and prints every
// protocol event as one JSON line. It is both an operator's Swiss army
// knife and the end-to-end test driver for the phone apps.
//
// Examples:
//
//	vcpc -url ws://127.0.0.1:8086/call -line main -password SECRET \
//	     -dial 1002 -seconds 10 -tone
//
//	vcpc -url ws://127.0.0.1:8086/call -line main -password SECRET -receive
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mehmannavaz/voiceline-phone/internal/vcp"
)

type event struct {
	TS   string `json:"ts"`
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

var (
	mu  sync.Mutex
	out = json.NewEncoder(os.Stdout)
)

func emit(typ string, data any) {
	mu.Lock()
	defer mu.Unlock()
	_ = out.Encode(event{TS: time.Now().Format("15:04:05.000"), Type: typ, Data: data})
}

func main() {
	var (
		url      = flag.String("url", "ws://127.0.0.1:8086/call", "protocol endpoint")
		line     = flag.String("line", "", "line id")
		password = flag.String("password", "", "line call password")
		display  = flag.String("display", "CLI", "display name presented to callees")

		receive = flag.Bool("receive", false, "subscribe for incoming calls")
		reject  = flag.Bool("reject", false, "auto-reject incoming calls (default: accept)")

		dialTo   = flag.String("dial", "", "dial this number after login")
		seconds  = flag.Float64("seconds", 0, "hang up after this many seconds of talk")
		tone     = flag.Bool("tone", false, "send a 440 Hz tone uplink instead of silence")
		dtmf     = flag.String("dtmf", "", "digits to send once the call is answered")
		transfer = flag.String("transfer", "", "transfer the answered call to this number (3 s after answer)")
		conf     = flag.String("conf", "", "conference this number into the answered call (1.5 s after answer)")

		wait = flag.Duration("wait", 0, "stay connected for at least this long even with no call")
	)
	flag.Parse()

	if *line == "" || *password == "" {
		fmt.Fprintln(os.Stderr, "vcpc: -line and -password are required")
		flag.Usage()
		os.Exit(2)
	}

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	answered := make(chan string, 8)
	finished := make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(finished) }) }

	client := vcp.NewClient(vcp.Options{
		URL: *url, LineID: *line, Password: *password,
		DisplayName: *display, UserAgent: "vcpc/1.0",
		AcceptInbound: *receive, Log: log,
	})

	var audioMu sync.Mutex
	uplink, downlink := int64(0), int64(0)
	active := map[string]bool{}

	client.OnLine = func(l vcp.LineInfo) { emit("line", l) }
	client.OnCallEvent = func(c *vcp.Call) {
		s := c.Snapshot()
		emit("call", s)
		if s.State == vcp.StateAnswered {
			select {
			case answered <- s.ID:
			default:
			}
		}
		if s.State == vcp.StateEnded || s.State == vcp.StateFailed {
			audioMu.Lock()
			was := active[s.ID]
			delete(active, s.ID)
			up, down := uplink, downlink
			audioMu.Unlock()
			if was {
				emit("audio_stats", map[string]any{
					"call_id": hashless(s.ID), "uplink_frames": up, "downlink_frames": down,
				})
			}
			if s.Direction == vcp.CallOut && *dialTo != "" {
				finish()
			}
		}
	}
	client.OnIncoming = func(inc vcp.Incoming) {
		emit("incoming", inc)
		if *reject {
			_ = client.Reject(inc.CallID, "busy")
			return
		}
		_ = client.Accept(inc.CallID)
	}
	client.OnDtmf = func(callID, digit, method string) {
		emit("dtmf", map[string]string{"call_id": callID, "digit": digit, "method": method})
	}
	client.OnTransfer = func(p vcp.TransferProgress) { emit("transfer", p) }
	client.OnConference = func(p vcp.ConferenceProgress) { emit("conference", p) }
	client.OnAudio = func(callID string, pcm []int16) {
		audioMu.Lock()
		downlink++
		active[callID] = true
		audioMu.Unlock()
	}
	client.OnBye = func(reason string, code uint32) {
		emit("bye", map[string]any{"reason": reason, "code": code})
	}
	client.OnClosed = func(err error) {
		emit("closed", map[string]any{"error": errStr(err)})
		finish()
	}

	if err := client.Connect(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "vcpc: connect: %v\n", err)
		os.Exit(3)
	}
	defer client.Close()
	emit("connected", map[string]any{"url": *url, "line": *line, "receiving": *receive})

	if *dialTo != "" {
		if err := client.Dial(*dialTo, *display, 0); err != nil {
			fmt.Fprintf(os.Stderr, "vcpc: dial: %v\n", err)
			os.Exit(4)
		}
	}

	// Microphone path: a gentle tone or comfort silence, paced at 20 ms.
	mic := make([]int16, vcp.FrameSamples)
	if *tone {
		for i := range mic {
			mic[i] = int16(9000 * math.Sin(2*math.Pi*float64(i)/float64(vcp.FrameSamples)))
		}
	}
	go func() {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-finished:
				return
			case <-t.C:
			}
			audioMu.Lock()
			ids := make([]string, 0, len(active))
			for id := range active {
				ids = append(ids, id)
			}
			audioMu.Unlock()
			for _, id := range ids {
				_ = client.SendAudio(id, mic)
				audioMu.Lock()
				uplink++
				audioMu.Unlock()
			}
		}
	}()

	// Post-answer script: DTMF, conference, transfer, timed hangup.
	// One goroutine owns the "answered" signal so the actions cannot race
	// each other for it.
	go func() {
		var id string
		select {
		case id = <-answered:
		case <-time.After(30 * time.Second):
			return
		case <-finished:
			return
		}
		if *dtmf != "" {
			time.Sleep(500 * time.Millisecond)
			for _, d := range *dtmf {
				_ = client.SendDTMF(id, string(d))
				time.Sleep(300 * time.Millisecond)
			}
		}
		if *conf != "" {
			time.Sleep(1500 * time.Millisecond)
			if err := client.Conference(id, *conf, ""); err != nil {
				emit("error", map[string]string{"where": "conference", "error": err.Error()})
			}
		}
		if *transfer != "" {
			time.Sleep(3000 * time.Millisecond)
			if err := client.Transfer(id, *transfer); err != nil {
				emit("error", map[string]string{"where": "transfer", "error": err.Error()})
			}
		}
		if *seconds > 0 {
			time.Sleep(time.Duration(*seconds * float64(time.Second)))
			for _, c := range client.Calls() {
				s := c.Snapshot()
				if s.Live() {
					_ = client.Hangup(s.ID)
				}
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	overall := 10 * time.Minute
	if *wait > 0 {
		overall = *wait
	}
	select {
	case <-finished:
	case <-sig:
		emit("closed", map[string]any{"error": "interrupt"})
	case <-ctx.Done():
	case <-time.After(overall):
		emit("closed", map[string]any{"error": "wait elapsed"})
	}
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// hashless shortens a call id for logs.
func hashless(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

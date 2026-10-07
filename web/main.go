//go:build js && wasm

// Command web builds the voiceline-phone website client: the ENTIRE
// protocol client — WebSocket transport, PBKDF2 key schedule, AES-GCM
// frames, call state machine — is the Go vcp package compiled to
// WebAssembly; the JavaScript side only renders the UI and moves PCM
// between the browser's audio devices and the Go module.
//
// Build:  GOOS=js GOARCH=wasm go build -o web/phone.wasm ./web
// Serve:  webserve (this repo) or any static file server.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"syscall/js"
	"time"

	"github.com/mehmannavaz/voiceline-phone/internal/vcp"
)

var (
	client    *vcp.Client
	callbacks js.Value
	micOn     bool
)

func main() {
	g := js.Global()
	if !g.Truthy() {
		return
	}
	vcpObj := g.Get("Object").New()

	vcpObj.Set("init", js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Truthy() {
			callbacks = args[0]
		}
		return nil
	}))

	vcpObj.Set("connect", js.FuncOf(func(this js.Value, args []js.Value) any {
		url, line, password, display := args[0].String(), args[1].String(), args[2].String(), args[3].String()
		receive := len(args) > 4 && args[4].Bool()
		go connect(url, line, password, display, receive)
		return nil
	}))

	vcpObj.Set("disconnect", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			go client.Close()
		}
		return nil
	}))

	vcpObj.Set("dial", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.Dial(args[0].String(), "", 0)
		}
		return nil
	}))

	vcpObj.Set("hangup", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.Hangup(args[0].String())
		}
		return nil
	}))

	vcpObj.Set("accept", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.Accept(args[0].String())
		}
		return nil
	}))

	vcpObj.Set("reject", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.Reject(args[0].String(), "declined")
		}
		return nil
	}))

	vcpObj.Set("dtmf", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.SendDTMF(args[0].String(), args[1].String())
		}
		return nil
	}))

	vcpObj.Set("transfer", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.Transfer(args[0].String(), args[1].String())
		}
		return nil
	}))

	vcpObj.Set("conference", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.Conference(args[0].String(), args[1].String(), "")
		}
		return nil
	}))

	vcpObj.Set("subscribe", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client != nil {
			_ = client.SubscribeInbound(args[0].Bool())
		}
		return nil
	}))

	// mic forwards one 20 ms Int16 PCM chunk from the browser's
	// microphone into every connected call.
	vcpObj.Set("mic", js.FuncOf(func(this js.Value, args []js.Value) any {
		if client == nil || !micOn {
			return nil
		}
		arr := args[0]
		n := arr.Length()
		if n == 0 {
			return nil
		}
		pcm := make([]int16, n)
		for i := 0; i < n; i++ {
			pcm[i] = int16(arr.Index(i).Int())
		}
		for _, call := range client.Calls() {
			if s := call.Snapshot(); s.Connected() {
				_ = client.SendAudio(s.ID, pcm)
			}
		}
		return nil
	}))

	vcpObj.Set("setMicOn", js.FuncOf(func(this js.Value, args []js.Value) any {
		micOn = len(args) > 0 && args[0].Bool()
		return nil
	}))

	g.Set("VCP", vcpObj)

	// Keep the module alive: block forever.
	select {}
}

// connect performs the login and installs the wire callbacks.
func connect(url, line, password, display string, receive bool) {
	c := vcp.NewClient(vcp.Options{
		URL: url, LineID: line, Password: password,
		DisplayName: display, UserAgent: "voiceline-phone-web/1.0",
		AcceptInbound: receive,
		Log:           slog.New(slog.DiscardHandler),
	})
	wire(c)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		emit("onError", err.Error())
		return
	}
	client = c
	emit("onConnected", url, line)
}

// wire bridges every vcp callback into the JS callbacks object.
func wire(c *vcp.Client) {
	c.OnLine = func(l vcp.LineInfo) { emitJSON("onLine", l) }
	c.OnCallEvent = func(call *vcp.Call) { emitJSON("onCall", call.Snapshot()) }
	c.OnIncoming = func(inc vcp.Incoming) { emitJSON("onIncoming", inc) }
	c.OnDtmf = func(callID, digit, method string) {
		emit("onDtmf", callID, digit, method)
	}
	c.OnTransfer = func(p vcp.TransferProgress) { emitJSON("onTransfer", p) }
	c.OnConference = func(p vcp.ConferenceProgress) { emitJSON("onConference", p) }
	c.OnBye = func(reason string, code uint32) { emit("onBye", reason, code) }
	c.OnClosed = func(err error) {
		msg := ""
		if err != nil {
			msg = err.Error()
		}
		client = nil
		emit("onClosed", msg)
	}
	c.OnAudio = func(callID string, pcm []int16) {
		// Convert to Float32 and hand to the JS audio path.
		arr := js.Global().Get("Float32Array").New(len(pcm))
		for i, v := range pcm {
			arr.SetIndex(i, float32(v)/32768)
		}
		call("onAudio", callID, arr)
	}
}

// emit invokes callbacks.<name>(...args).
func emit(name string, args ...any) {
	if !callbacks.Truthy() {
		return
	}
	_ = callbacks.Call(name, args...)
}

// call invokes callbacks.<name> with prebuilt values.
func call(name string, args ...any) {
	if !callbacks.Truthy() {
		return
	}
	_ = callbacks.Call(name, args...)
}

// emitJSON marshals one struct as a JS object.
func emitJSON(name string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	obj := js.Global().Get("JSON").Call("parse", string(raw))
	call(name, obj)
}

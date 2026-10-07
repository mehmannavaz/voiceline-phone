//go:build js && wasm

package vcp

import (
	"context"
	"fmt"
	"syscall/js"
)

// jsConn bridges the browser's WebSocket to the protocol's conn interface,
// so the exact same vcp package runs unchanged as a WASM website: the
// handshake, the PBKDF2 schedule, the AES-GCM frames and the call state
// machine are plain Go; only the socket is JavaScript's.
type jsConn struct {
	ws     js.Value
	ready  chan error
	msgs   chan []byte
	closed chan string
	// kept references so the runtime does not GC the callbacks
	onOpen   js.Func
	onMsg    js.Func
	onErr    js.Func
	onClose  js.Func
	didClose bool
}

// dialTransport opens a browser WebSocket.
func dialTransport(ctx context.Context, url string) (conn, error) {
	wsType := js.Global().Get("WebSocket")
	if !wsType.Truthy() {
		return nil, fmt.Errorf("vcp: no WebSocket in this environment")
	}
	c := &jsConn{
		ws:     wsType.New(url),
		ready:  make(chan error, 1),
		msgs:   make(chan []byte, 512),
		closed: make(chan string, 1),
	}
	c.ws.Set("binaryType", "arraybuffer")

	c.onOpen = js.FuncOf(func(this js.Value, args []js.Value) any {
		c.ready <- nil
		return nil
	})
	c.onMsg = js.FuncOf(func(this js.Value, args []js.Value) any {
		buf := args[0].Get("data")
		if buf.InstanceOf(js.Global().Get("ArrayBuffer")) {
			src := js.Global().Get("Uint8Array").New(buf)
			out := make([]byte, src.Length())
			js.CopyBytesToGo(out, src)
			select {
			case c.msgs <- out:
			default:
				// The protocol's read loop is starved: the server drops
				// audio before it drops the session; keep the newest by
				// shedding the oldest.
				select {
				case <-c.msgs:
				default:
				}
				select {
				case c.msgs <- out:
				default:
				}
			}
		}
		return nil
	})
	c.onErr = js.FuncOf(func(this js.Value, args []js.Value) any {
		select {
		case c.ready <- fmt.Errorf("vcp: websocket error"):
		default:
		}
		return nil
	})
	c.onClose = js.FuncOf(func(this js.Value, args []js.Value) any {
		reason := "closed"
		if len(args) > 0 {
			reason = args[0].Get("reason").String()
		}
		select {
		case c.closed <- reason:
		default:
		}
		select {
		case c.ready <- fmt.Errorf("vcp: closed before open"):
		default:
		}
		return nil
	})
	c.ws.Call("addEventListener", "open", c.onOpen)
	c.ws.Call("addEventListener", "message", c.onMsg)
	c.ws.Call("addEventListener", "error", c.onErr)
	c.ws.Call("addEventListener", "close", c.onClose)

	select {
	case err := <-c.ready:
		if err != nil {
			return nil, err
		}
		return c, nil
	case <-ctx.Done():
		c.release()
		return nil, ctx.Err()
	}
}

func (c *jsConn) read(ctx context.Context) ([]byte, error) {
	select {
	case data := <-c.msgs:
		return data, nil
	case reason := <-c.closed:
		return nil, fmt.Errorf("vcp: websocket closed: %s", reason)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *jsConn) write(ctx context.Context, data []byte) error {
	dst := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(dst, data)
	c.ws.Call("send", dst)
	return nil
}

func (c *jsConn) close(code uint32, reason string) error {
	if c.didClose {
		return nil
	}
	c.didClose = true
	c.ws.Call("close", js.ValueOf(int(code)), js.ValueOf(reason))
	c.release()
	return nil
}

// release drops the JS callback references.
func (c *jsConn) release() {
	c.onOpen.Release()
	c.onMsg.Release()
	c.onErr.Release()
	c.onClose.Release()
}

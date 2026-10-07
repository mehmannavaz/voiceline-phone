package vcp

import "context"

// conn is the minimal binary WebSocket surface the protocol needs. Native
// builds get it from coder/websocket; the browser (WASM) build bridges the
// JavaScript WebSocket via syscall/js (see transport_js.go).
type conn interface {
	// read returns the next binary message. Text frames are a protocol
	// violation upstream; implementations surface them as errors.
	read(ctx context.Context) (data []byte, err error)
	// write sends one binary message.
	write(ctx context.Context, data []byte) error
	// close terminates the connection.
	close(code uint32, reason string) error
}

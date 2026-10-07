//go:build !js || !wasm

package vcp

import (
	"context"
	"fmt"

	"github.com/coder/websocket"
)

// wsConn adapts coder/websocket to the protocol's conn interface.
type wsConn struct {
	c *websocket.Conn
}

// dialNative opens the WebSocket endpoint.
func dialTransport(ctx context.Context, url string) (conn, error) {
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		// The line's call password IS the authentication; cross-origin
		// clients (file:// pages, APKs, EXEs) are expected.
		HTTPHeader: nil,
	})
	if err != nil {
		return nil, fmt.Errorf("vcp: dial %s: %w", url, err)
	}
	c.SetReadLimit(512 * 1024)
	return &wsConn{c: c}, nil
}

func (w *wsConn) read(ctx context.Context) ([]byte, error) {
	typ, data, err := w.c.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ == websocket.MessageText {
		return nil, fmt.Errorf("vcp: text frame on a binary protocol")
	}
	return data, nil
}

func (w *wsConn) write(ctx context.Context, data []byte) error {
	return w.c.Write(ctx, websocket.MessageBinary, data)
}

func (w *wsConn) close(code uint32, reason string) error {
	return w.c.Close(websocket.StatusCode(code), reason)
}

package vcp

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/mehmannavaz/voiceline-phone/internal/vcp/pb"
)

// Options configures one client session.
type Options struct {
	// URL of the protocol endpoint, e.g. ws://host:8086/call
	URL string
	// LineID is the Voiceline line to call through.
	LineID string
	// Password is the line's call password.
	Password string
	// DisplayName shown to callees when the provider supports it.
	DisplayName string
	// UserAgent identifies the app ("voiceline-phone/1.0 (windows)").
	UserAgent string
	// AcceptInbound subscribes for incoming calls right after login.
	AcceptInbound bool
	// PBKDF2Iterations for the key schedule (0 = 60000, the server default).
	PBKDF2Iterations int

	HandshakeTimeout time.Duration // default 15s
	WriteTimeout     time.Duration // default 10s
	KeepalivePeriod  time.Duration // default 25s

	Log *slog.Logger
}

func (o Options) withDefaults() Options {
	if o.UserAgent == "" {
		o.UserAgent = "voiceline-phone/1.0"
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 15 * time.Second
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.KeepalivePeriod <= 0 {
		o.KeepalivePeriod = 25 * time.Second
	}
	if o.PBKDF2Iterations <= 0 {
		o.PBKDF2Iterations = DefaultPBKDF2Iterations
	}
	return o
}

// LineInfo is the snapshot of the bound line.
type LineInfo struct {
	ID            string `json:"id"`
	Number        string `json:"number"`
	DisplayName   string `json:"display_name"`
	RegState      string `json:"reg_state"`
	Registered    bool   `json:"registered"`
	Hook          string `json:"hook"`
	ActiveCalls   int    `json:"active_calls"`
	MaxConcurrent int    `json:"max_concurrent"`
}

// Incoming is one offered incoming call.
type Incoming struct {
	CallID      string `json:"call_id"`
	From        string `json:"from"`
	FromDisplay string `json:"from_display"`
	To          string `json:"to"`
	RingSec     int    `json:"ring_sec"`
}

// TransferProgress reports a transfer's dialing/answered/failed phases.
type TransferProgress struct {
	CallID string `json:"call_id"`
	State  string `json:"state"` // dialing | answered | failed
	Detail string `json:"detail"`
}

// ConferenceProgress reports group-call membership changes.
type ConferenceProgress struct {
	CallID       string `json:"call_id"`
	Participant  string `json:"participant"`
	Action       string `json:"action"` // joined | left
	Participants int    `json:"participants"`
}

// Client is one authenticated VCP session bound to one line. Set the
// On* callbacks before Connect; they fire from the read loop goroutine.
type Client struct {
	opts Options
	log  *slog.Logger

	cn     conn
	cCodec *frameCodec // client -> server
	sCodec *frameCodec // server -> client

	sendMu  sync.Mutex
	sendSeq uint64
	recvSeq uint64

	closeOnce sync.Once
	done      chan struct{}
	loopyOnce sync.Once

	mu      sync.Mutex
	calls   map[string]*Call
	line    LineInfo
	inbound bool

	// Callbacks — all optional.
	OnLine       func(LineInfo)
	OnCallEvent  func(c *Call)
	OnIncoming   func(inc Incoming)
	OnDtmf       func(callID, digit, method string)
	OnTransfer   func(p TransferProgress)
	OnConference func(p ConferenceProgress)
	OnAudio      func(callID string, pcm []int16)
	OnMediaStart func(callID string)
	OnBye        func(reason string, code uint32)
	OnClosed     func(err error)
}

// NewClient builds a client; call Connect to bring it up.
func NewClient(opts Options) *Client {
	opts = opts.withDefaults()
	log := opts.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Client{
		opts:  opts,
		log:   log,
		done:  make(chan struct{}),
		calls: map[string]*Call{},
	}
}

// Connect performs the WebSocket dial, the VCP handshake and the
// authenticated session setup, then runs the read loop in the
// background. It returns once the session is live.
func (c *Client) Connect(ctx context.Context) error {
	hsCtx, cancel := context.WithTimeout(ctx, c.opts.HandshakeTimeout)
	defer cancel()

	cn, err := dialTransport(hsCtx, c.opts.URL)
	if err != nil {
		return err
	}
	c.cn = cn

	// 1. Plaintext HelloRequest.
	clientNonce := randomNonce()
	hello, err := proto.Marshal(&pb.HelloRequest{
		Version: ProtocolVersion, LineId: c.opts.LineID, ClientNonce: clientNonce,
	})
	if err != nil {
		_ = cn.close(1002, "marshal")
		return fmt.Errorf("vcp: hello marshal: %w", err)
	}
	if err := cn.write(hsCtx, hello); err != nil {
		_ = cn.close(1011, "hello")
		return fmt.Errorf("vcp: hello write: %w", err)
	}

	// 2. HelloResponse (plaintext).
	data, err := cn.read(hsCtx)
	if err != nil {
		_ = cn.close(1011, "handshake")
		return fmt.Errorf("vcp: hello read: %w", err)
	}
	var resp pb.HelloResponse
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &resp); err != nil {
		_ = cn.close(1002, "bad hello response")
		return fmt.Errorf("vcp: hello parse: %w", err)
	}
	if resp.Error != "" {
		_ = cn.close(1000, "rejected")
		return fmt.Errorf("vcp: handshake rejected: %s", resp.Error)
	}
	if resp.Version != ProtocolVersion || len(resp.ServerNonce) != NonceSize {
		_ = cn.close(1002, "bad hello response")
		return fmt.Errorf("vcp: bad HelloResponse (version=%d nonce=%d bytes)", resp.Version, len(resp.ServerNonce))
	}

	// 3. Key schedule + proof.
	k, err := deriveKeys(c.opts.Password, clientNonce, resp.ServerNonce, c.opts.PBKDF2Iterations)
	if err != nil {
		_ = cn.close(1011, "keys")
		return err
	}
	proof := clientProof(k.auth, c.opts.LineID, clientNonce, resp.ServerNonce)
	c.cCodec, err = newFrameCodec(k.clientWrite)
	if err != nil {
		_ = cn.close(1011, "codec")
		return err
	}
	c.sCodec, err = newFrameCodec(k.serverWrite)
	if err != nil {
		_ = cn.close(1011, "codec")
		return err
	}

	// 4. Encrypted AuthConfirm at sequence 0.
	confirm, err := proto.Marshal(&pb.Envelope{Payload: &pb.Envelope_AuthConfirm{AuthConfirm: &pb.AuthConfirm{
		Proof: proof, DisplayName: c.opts.DisplayName, UserAgent: c.opts.UserAgent,
	}}})
	if err != nil {
		_ = cn.close(1011, "marshal")
		return err
	}
	if err := cn.write(hsCtx, c.cCodec.seal(0, confirm)); err != nil {
		_ = cn.close(1011, "auth")
		return fmt.Errorf("vcp: auth write: %w", err)
	}
	c.sendSeq = 1

	// 5. AuthResult — sealed under the server's keys; a dropped
	// connection or an undecryptable frame IS the wrong-password
	// rejection.
	data, err = cn.read(hsCtx)
	if err != nil {
		_ = cn.close(1011, "auth read")
		return fmt.Errorf("vcp: authentication failed (wrong call password?): %w", err)
	}
	plain, err := c.sCodec.open(data, 0)
	if err != nil {
		_ = cn.close(1011, "auth")
		return fmt.Errorf("vcp: authentication failed (wrong call password?)")
	}
	c.recvSeq = 1
	var env pb.Envelope
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(plain, &env); err != nil {
		_ = cn.close(1002, "bad auth result")
		return fmt.Errorf("vcp: auth parse: %w", err)
	}
	res := env.GetAuthResult()
	if res == nil {
		_ = cn.close(1002, "bad auth result")
		return fmt.Errorf("vcp: expected AuthResult, got %T", env.Payload)
	}
	if !res.Ok {
		_ = cn.close(1000, "auth failed")
		return fmt.Errorf("vcp: authentication failed: %s", res.Error)
	}
	c.line = lineInfoOf(res.Line)

	go c.readLoop()
	go c.keepalive()

	if c.OnLine != nil {
		c.OnLine(c.line)
	}
	c.log.Info("vcp session up",
		"line", c.line.ID, "number", c.line.Number, "hook", c.line.Hook, "url", c.opts.URL)

	if c.opts.AcceptInbound {
		if err := c.SubscribeInbound(true); err != nil {
			return err
		}
	}
	return nil
}

// readLoop decrypts and dispatches every server frame until the session
// ends.
func (c *Client) readLoop() {
	err := func() error {
		for {
			data, err := c.cn.read(context.Background())
			if err != nil {
				return err
			}
			want := c.recvSeq
			plain, err := c.sCodec.open(data, want)
			if err != nil {
				return fmt.Errorf("frame %d: %w", want, err)
			}
			c.recvSeq = want + 1
			var env pb.Envelope
			if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(plain, &env); err != nil {
				return fmt.Errorf("envelope parse: %w", err)
			}
			c.dispatch(&env)
		}
	}()
	c.shutdown(fmt.Errorf("session ended: %w", err))
}

// dispatch routes one decrypted server message.
func (c *Client) dispatch(env *pb.Envelope) {
	switch p := env.Payload.(type) {
	case *pb.Envelope_CallEvent:
		c.applyCallEvent(p.CallEvent)

	case *pb.Envelope_MediaStart:
		if ms := p.MediaStart; ms != nil {
			if call := c.callByID(ms.CallId); call != nil {
				call.setMedia(true)
			}
			if c.OnMediaStart != nil {
				c.OnMediaStart(ms.CallId)
			}
		}

	case *pb.Envelope_Audio:
		a := p.Audio
		if a == nil || len(a.Pcm) == 0 || len(a.Pcm)%2 != 0 {
			return
		}
		if c.OnAudio == nil {
			return
		}
		pcm := make([]int16, len(a.Pcm)/2)
		for i := range pcm {
			pcm[i] = int16(binary.LittleEndian.Uint16(a.Pcm[i*2:]))
		}
		c.OnAudio(a.CallId, pcm)

	case *pb.Envelope_DtmfEvent:
		if d := p.DtmfEvent; d != nil && c.OnDtmf != nil {
			c.OnDtmf(d.CallId, d.Digit, d.Method)
		}

	case *pb.Envelope_TransferEv:
		if t := p.TransferEv; t != nil && c.OnTransfer != nil {
			c.OnTransfer(TransferProgress{CallID: t.CallId, State: t.State, Detail: t.Detail})
		}

	case *pb.Envelope_ConfEvent:
		if e := p.ConfEvent; e != nil && c.OnConference != nil {
			c.OnConference(ConferenceProgress{
				CallID: e.CallId, Participant: e.Participant,
				Action: e.Action, Participants: int(e.Participants),
			})
		}

	case *pb.Envelope_Incoming:
		if inc := p.Incoming; inc != nil {
			call := &Call{
				ID: inc.CallId, Direction: CallIn,
				From: inc.From, FromDisplay: inc.FromDisplay, To: inc.To,
				State: StateIncoming, Detail: "incoming",
			}
			c.mu.Lock()
			c.calls[call.ID] = call
			c.mu.Unlock()
			if c.OnIncoming != nil {
				c.OnIncoming(Incoming{
					CallID: inc.CallId, From: inc.From, FromDisplay: inc.FromDisplay,
					To: inc.To, RingSec: int(inc.RingSec),
				})
			}
		}

	case *pb.Envelope_InboundAck:
		if ack := p.InboundAck; ack != nil {
			c.mu.Lock()
			c.inbound = ack.Want
			c.mu.Unlock()
		}

	case *pb.Envelope_Ping:
		if ping := p.Ping; ping != nil {
			_ = c.send(&pb.Envelope{Payload: &pb.Envelope_Pong{Pong: &pb.Pong{Nonce: ping.Nonce}}})
		}

	case *pb.Envelope_Bye:
		if b := p.Bye; b != nil {
			c.log.Info("vcp server bye", "reason", b.Reason, "code", b.Code)
			if c.OnBye != nil {
				c.OnBye(b.Reason, b.Code)
			}
		}

	default:
		// Unknown members are ignored for forward compatibility.
	}
}

// applyCallEvent updates the call registry and fires OnCallEvent.
func (c *Client) applyCallEvent(ev *pb.CallEvent) {
	if ev == nil {
		return
	}
	c.mu.Lock()
	call := c.calls[ev.CallId]
	if call == nil {
		call = &Call{ID: ev.CallId, Direction: CallOut, To: ev.Detail}
		c.calls[ev.CallId] = call
	}
	call.mu.Lock()
	call.State = ev.State
	call.Detail = ev.Detail
	if ev.State == StateAnswered && call.AnsweredAt.IsZero() {
		call.AnsweredAt = time.Now()
	}
	if ev.State == StateEnded || ev.State == StateFailed {
		call.EndedAt = time.Now()
		call.mediaOn = false // call.mu already held; setMedia would re-lock
	}
	call.mu.Unlock()
	c.mu.Unlock()
	if c.OnCallEvent != nil {
		c.OnCallEvent(call)
	}
}

// keepalive exchanges app-level pings so the server sees liveness even
// when no audio flows (its WS pings also count).
func (c *Client) keepalive() {
	t := time.NewTicker(c.opts.KeepalivePeriod)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
			if err := c.send(&pb.Envelope{Payload: &pb.Envelope_Ping{Ping: &pb.Ping{
				Nonce: uint64(time.Now().UnixNano()),
			}}}); err != nil {
				return
			}
		}
	}
}

// send seals and writes one envelope, tearing the session down when the
// write fails.
func (c *Client) send(env *pb.Envelope) error {
	err := c.sendRaw(env)
	if err != nil {
		c.shutdown(fmt.Errorf("write failed: %w", err))
	}
	return err
}

// sendRaw seals and writes one envelope with no teardown side effects —
// Close uses it because its own teardown is already in flight (calling
// send there would re-enter the shutdown path and deadlock the Once).
func (c *Client) sendRaw(env *pb.Envelope) error {
	raw, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.opts.WriteTimeout)
	defer cancel()

	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	seq := c.sendSeq
	c.sendSeq++
	return c.cn.write(ctx, c.cCodec.seal(seq, raw))
}

// shutdown ends the session exactly once and reports it.
func (c *Client) shutdown(err error) {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.cn.close(1000, "client shutdown")

		// Calls die with the session (the server hangs them up); tell the
		// UI in no uncertain terms.
		c.mu.Lock()
		calls := make([]*Call, 0, len(c.calls))
		for _, call := range c.calls {
			call.mu.Lock()
			if call.State != StateEnded && call.State != StateFailed {
				call.State = StateEnded
				call.Detail = "connection lost"
				call.EndedAt = time.Now()
				calls = append(calls, call)
			}
			call.mu.Unlock()
		}
		c.calls = map[string]*Call{}
		c.mu.Unlock()
		for _, call := range calls {
			if c.OnCallEvent != nil {
				c.OnCallEvent(call)
			}
		}
		c.log.Info("vcp session closed", "error", err.Error())
		if c.OnClosed != nil {
			c.OnClosed(err)
		}
	})
}

// Close sends a graceful Bye and tears the session down.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		_ = c.sendRaw(&pb.Envelope{Payload: &pb.Envelope_Bye{Bye: &pb.Bye{Reason: "client closing"}}})
		close(c.done)
		_ = c.cn.close(1000, "client closing")
		c.mu.Lock()
		c.calls = map[string]*Call{}
		c.mu.Unlock()
		if c.OnClosed != nil {
			c.OnClosed(nil)
		}
	})
}

// Done exposes session termination.
func (c *Client) Done() <-chan struct{} { return c.done }

// Line snapshots the bound line info.
func (c *Client) Line() LineInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.line
}

// InboundSubscribed reports whether this client receives incoming calls.
func (c *Client) InboundSubscribed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inbound
}

// Calls lists the known calls (live and finished).
func (c *Client) Calls() []*Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Call, 0, len(c.calls))
	for _, call := range c.calls {
		out = append(out, call)
	}
	return out
}

// callByID finds one call.
func (c *Client) callByID(id string) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[id]
}

// ---- actions ----

// Dial places an outbound call through the line. Progress arrives as
// CallEvents on the first "dialing" event (which carries the call id).
func (c *Client) Dial(to, display string, timeoutSec uint32) error {
	if to == "" {
		return fmt.Errorf("vcp: destination is empty")
	}
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Dial{Dial: &pb.DialRequest{
		To: to, DisplayName: display, TimeoutSec: timeoutSec,
	}}})
}

// Hangup ends one call; the session and its other calls survive.
func (c *Client) Hangup(callID string) error {
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Hangup{Hangup: &pb.HangupRequest{CallId: callID}}})
}

// SendDTMF sends one digit toward the far end (RFC 4733 out-of-band).
func (c *Client) SendDTMF(callID, digit string) error {
	if digit == "" {
		return fmt.Errorf("vcp: empty digit")
	}
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Dtmf{Dtmf: &pb.DtmfRequest{
		CallId: callID, Digit: digit,
	}}})
}

// SendAudio uplinks one chunk of microphone audio (PCM16 LE 8 kHz mono;
// any length, 160 samples = 20 ms is the natural ptime).
func (c *Client) SendAudio(callID string, pcm []int16) error {
	if len(pcm) == 0 {
		return nil
	}
	call := c.callByID(callID)
	if call == nil {
		return fmt.Errorf("vcp: unknown call %s", callID)
	}
	buf := make([]byte, len(pcm)*2)
	for i, v := range pcm {
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(v))
	}
	call.mu.Lock()
	call.seq++
	call.ts += uint32(len(pcm))
	seq, ts := call.seq, call.ts
	call.mu.Unlock()
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Audio{Audio: &pb.AudioFrame{
		CallId: callID, Seq: seq, Timestamp: ts, Pcm: buf,
	}}})
}

// Transfer connects the far party of an answered call to an outside
// number, then releases this client.
func (c *Client) Transfer(callID, to string) error {
	if to == "" {
		return fmt.Errorf("vcp: transfer destination is empty")
	}
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Transfer{Transfer: &pb.TransferRequest{
		CallId: callID, To: to,
	}}})
}

// Conference dials another person into the live call.
func (c *Client) Conference(callID, to, display string) error {
	if to == "" {
		return fmt.Errorf("vcp: conference destination is empty")
	}
	return c.send(&pb.Envelope{Payload: &pb.Envelope_ConfInvite{ConfInvite: &pb.ConferenceInvite{
		CallId: callID, To: to, DisplayName: display,
	}}})
}

// SubscribeInbound toggles delivery of incoming calls to this client.
func (c *Client) SubscribeInbound(want bool) error {
	return c.send(&pb.Envelope{Payload: &pb.Envelope_InboundSub{InboundSub: &pb.InboundSubscription{
		Want: want,
	}}})
}

// Accept picks up an offered incoming call.
func (c *Client) Accept(callID string) error {
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Accept{Accept: &pb.AcceptRequest{CallId: callID}}})
}

// Reject declines an offered incoming call.
func (c *Client) Reject(callID, reason string) error {
	return c.send(&pb.Envelope{Payload: &pb.Envelope_Reject{Reject: &pb.RejectRequest{
		CallId: callID, Reason: reason,
	}}})
}

// lineInfoOf converts the wire snapshot.
func lineInfoOf(l *pb.LineInfo) LineInfo {
	if l == nil {
		return LineInfo{}
	}
	return LineInfo{
		ID: l.Id, Number: l.Number, DisplayName: l.DisplayName,
		RegState: l.RegState, Registered: l.Registered, Hook: l.Hook,
		ActiveCalls: int(l.ActiveCalls), MaxConcurrent: int(l.MaxConcurrent),
	}
}

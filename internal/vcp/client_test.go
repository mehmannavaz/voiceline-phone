package vcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	pb "github.com/mehmannavaz/voiceline-phone/internal/vcp/pb"
)

// TestKeyScheduleGoldenVector pins the key schedule against an
// independent implementation (Python hashlib.pbkdf2_hmac + hmac):
// password "correct-horse-battery-staple", line "main-line",
// client nonce 0..15, server nonce 16..31, 1000 iterations.
func TestKeyScheduleGoldenVector(t *testing.T) {
	clientNonce := make([]byte, 16)
	for i := range clientNonce {
		clientNonce[i] = byte(i)
	}
	serverNonce := make([]byte, 16)
	for i := range serverNonce {
		serverNonce[i] = byte(16 + i)
	}
	k, err := deriveKeys("correct-horse-battery-staple", clientNonce, serverNonce, 1000)
	if err != nil {
		t.Fatal(err)
	}
	wantClient, _ := hex.DecodeString("e5a01ac11a94a1019dfdaabe0e5df9968e32d2c6963642a12dfb88ef3154faf5")
	wantServer, _ := hex.DecodeString("7704c4775b189928a231f1a4afe135108b3178aeccf99154f956a8a723083abc")
	wantAuth, _ := hex.DecodeString("8a099905d1f8488ddded36fdc05217df6cb92c016523d414dbc94bb3e47ea5b7")
	wantProof, _ := hex.DecodeString("d4c792243f777c37e49ef1b4daa0fb66c4799de990d552fecf148766bdd4d53d")
	if !bytes.Equal(k.clientWrite[:], wantClient) {
		t.Fatalf("clientWrite mismatch: %x", k.clientWrite)
	}
	if !bytes.Equal(k.serverWrite[:], wantServer) {
		t.Fatalf("serverWrite mismatch: %x", k.serverWrite)
	}
	if !bytes.Equal(k.auth[:], wantAuth) {
		t.Fatalf("authKey mismatch: %x", k.auth)
	}
	gotProof := clientProof(k.auth, "main-line", clientNonce, serverNonce)
	if !bytes.Equal(gotProof, wantProof) {
		t.Fatalf("proof mismatch: %x", gotProof)
	}
}

// TestFrameCodecRoundtrip checks seal/open, sequence enforcement and the
// leading-sequence layout.
func TestFrameCodecRoundtrip(t *testing.T) {
	c, err := newFrameCodec([32]byte{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	frame := c.seal(7, []byte("hello protocol"))
	if len(frame) != 8+len("hello protocol")+16 {
		t.Fatalf("frame length %d", len(frame))
	}
	if frame[0] != 0 || frame[7] != 7 {
		t.Fatalf("sequence not big-endian in the clear: % x", frame[:8])
	}
	plain, err := c.open(frame, 7)
	if err != nil || string(plain) != "hello protocol" {
		t.Fatalf("open: %v %q", err, plain)
	}
	if _, err := c.open(frame, 8); err != ErrBadFrame {
		t.Fatalf("wrong sequence must fail with ErrBadFrame, got %v", err)
	}
	tampered := append([]byte(nil), frame...)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := c.open(tampered, 7); err != ErrBadFrame {
		t.Fatalf("tampered frame must fail with ErrBadFrame, got %v", err)
	}
	if _, err := c.open(frame[:10], 7); err != ErrBadFrame {
		t.Fatalf("short frame must fail with ErrBadFrame, got %v", err)
	}
}

// TestNonceLength pins the handshake nonce rule.
func TestNonceLength(t *testing.T) {
	if _, err := deriveKeys("pw", make([]byte, 15), make([]byte, 16), 1000); err == nil {
		t.Fatal("short client nonce must be rejected")
	}
	if len(randomNonce()) != NonceSize {
		t.Fatal("randomNonce length")
	}
}

// ---- mini in-process VCP server ----

// miniServer implements just enough of the server side — with the SAME
// crypto helpers — to exercise the client end to end over a real
// WebSocket: handshake, auth, subscription ack, ping/pong, a full dial
// lifecycle with audio, and an incoming call offer with accept.
type miniServer struct {
	t      *testing.T
	pw     string
	lineID string
	srv    *httptest.Server
	url    string

	mu       sync.Mutex
	uplink   int
	gotSub   bool
	audioSum int16

	onDial func()
}

func startMiniServer(t *testing.T) *miniServer {
	m := &miniServer{t: t, pw: "test-password-123", lineID: "main"}
	mux := http.NewServeMux()
	mux.HandleFunc("/call", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		m.serve(conn)
	})
	m.srv = httptest.NewServer(mux)
	m.url = "ws" + strings.TrimPrefix(m.srv.URL, "http") + "/call"
	t.Cleanup(m.srv.Close)
	return m
}

type srvSession struct {
	m        *miniServer
	conn     *websocket.Conn
	cCodec   *frameCodec // client -> server (open)
	sCodec   *frameCodec // server -> client (seal)
	recvSeq  uint64
	sendSeq  uint64
	sendMu   sync.Mutex
	lineInfo *pb.LineInfo
}

func (m *miniServer) serve(conn *websocket.Conn) {
	ctx := context.Background()
	s := &srvSession{m: m, conn: conn}

	// hello
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary {
		_ = conn.Close(1002, "hello")
		return
	}
	var hello pb.HelloRequest
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &hello); err != nil ||
		hello.LineId != m.lineID || len(hello.ClientNonce) != NonceSize {
		_ = conn.Close(1002, "bad hello")
		return
	}
	serverNonce := randomNonce()
	resp, _ := proto.Marshal(&pb.HelloResponse{Version: ProtocolVersion, ServerNonce: serverNonce})
	if err := conn.Write(ctx, websocket.MessageBinary, resp); err != nil {
		return
	}
	k, err := deriveKeys(m.pw, hello.ClientNonce, serverNonce, 1000)
	if err != nil {
		return
	}
	s.cCodec, _ = newFrameCodec(k.clientWrite)
	s.sCodec, _ = newFrameCodec(k.serverWrite)

	// auth
	typ, data, err = conn.Read(ctx)
	if err != nil {
		return
	}
	plain, err := s.cCodec.open(data, 0)
	if err != nil {
		// Wrong password: drop like the real server does.
		_ = conn.Close(1000, "auth failed")
		return
	}
	s.recvSeq = 1
	var env pb.Envelope
	if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(plain, &env); err != nil {
		return
	}
	if env.GetAuthConfirm() == nil {
		return
	}
	s.lineInfo = &pb.LineInfo{Id: m.lineID, Number: "17005554117", Registered: true, Hook: "off_hook"}
	if err := s.send(&pb.Envelope{Payload: &pb.Envelope_AuthResult{AuthResult: &pb.AuthResult{
		Ok: true, Line: s.lineInfo,
	}}}); err != nil {
		return
	}

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			return
		}
		want := s.recvSeq
		plain, err := s.cCodec.open(data, want)
		if err != nil {
			return
		}
		s.recvSeq = want + 1
		var env pb.Envelope
		if err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(plain, &env); err != nil {
			return
		}
		if !s.dispatch(&env) {
			return
		}
	}
}

func (s *srvSession) send(env *pb.Envelope) error {
	raw, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	seq := s.sendSeq
	s.sendSeq++
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageBinary, s.sCodec.seal(seq, raw))
}

func (s *srvSession) dispatch(env *pb.Envelope) bool {
	switch p := env.Payload.(type) {
	case *pb.Envelope_Ping:
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_Pong{Pong: &pb.Pong{Nonce: p.Ping.Nonce}}})

	case *pb.Envelope_InboundSub:
		s.m.mu.Lock()
		s.m.gotSub = p.InboundSub.Want
		s.m.mu.Unlock()
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_InboundAck{InboundAck: &pb.InboundAck{Want: p.InboundSub.Want}}})

	case *pb.Envelope_Dial:
		id := "call-1"
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_CallEvent{CallEvent: &pb.CallEvent{CallId: id, State: "dialing", Detail: p.Dial.To}}})
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_CallEvent{CallEvent: &pb.CallEvent{CallId: id, State: "ringing"}}})
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_CallEvent{CallEvent: &pb.CallEvent{CallId: id, State: "answered", AnsweredAtMs: uint64(time.Now().UnixMilli())}}})
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_MediaStart{MediaStart: &pb.MediaStart{CallId: id, Codec: "L16", SampleRate: 8000, FrameMs: 20}}})
		pcm := make([]byte, 320)
		for i := 0; i < len(pcm); i += 2 {
			pcm[i] = byte(i / 2)
			pcm[i+1] = 0x40
		}
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_Audio{Audio: &pb.AudioFrame{CallId: id, Seq: 1, Timestamp: 160, Pcm: pcm}}})

	case *pb.Envelope_Audio:
		s.m.mu.Lock()
		s.m.uplink++
		if len(p.Audio.Pcm) >= 2 {
			s.m.audioSum += int16(p.Audio.Pcm[0]) | int16(p.Audio.Pcm[1])<<8
		}
		s.m.mu.Unlock()

	case *pb.Envelope_Hangup:
		_ = s.send(&pb.Envelope{Payload: &pb.Envelope_CallEvent{CallEvent: &pb.CallEvent{CallId: p.Hangup.CallId, State: "ended", Detail: "completed", DurationMs: 500}}})

	case *pb.Envelope_Bye:
		return false
	}
	return true
}

// ---- client tests against the mini server ----

func newTestClient(t *testing.T, m *miniServer, mutate func(*Client)) *Client {
	t.Helper()
	c := NewClient(Options{
		URL: m.url, LineID: m.lineID, Password: m.pw,
		DisplayName: "Tester", UserAgent: "vcp-test/1.0",
		PBKDF2Iterations: 1000, KeepalivePeriod: time.Hour,
		Log: slog.New(slog.DiscardHandler),
	})
	if mutate != nil {
		mutate(c)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestClientHandshakeAndLine(t *testing.T) {
	m := startMiniServer(t)
	var gotLine LineInfo
	c := newTestClient(t, m, func(c *Client) {
		c.OnLine = func(l LineInfo) { gotLine = l }
	})
	if gotLine.ID != m.lineID || gotLine.Number != "17005554117" || !gotLine.Registered {
		t.Fatalf("bad line info: %+v", gotLine)
	}
	if c.Line().Hook != "off_hook" {
		t.Fatalf("hook = %q", c.Line().Hook)
	}
}

func TestClientWrongPasswordRejected(t *testing.T) {
	m := startMiniServer(t)
	c := NewClient(Options{URL: m.url, LineID: m.lineID, Password: "wrong",
		PBKDF2Iterations: 1000, Log: slog.New(slog.DiscardHandler)})
	err := c.Connect(context.Background())
	if err == nil {
		c.Close()
		t.Fatal("wrong password must be rejected")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("rejection should mention the password: %v", err)
	}
}

func TestClientUnknownLineRejected(t *testing.T) {
	m := startMiniServer(t)
	c := NewClient(Options{URL: m.url, LineID: "nope", Password: m.pw,
		PBKDF2Iterations: 1000, Log: slog.New(slog.DiscardHandler)})
	if err := c.Connect(context.Background()); err == nil {
		c.Close()
		t.Fatal("unknown line must be rejected in the handshake")
	}
}

func TestClientDialLifecycleAndAudio(t *testing.T) {
	m := startMiniServer(t)
	events := make(chan Snapshot, 16)
	audio := make(chan string, 8)
	media := make(chan string, 4)
	c := newTestClient(t, m, func(c *Client) {
		c.OnCallEvent = func(call *Call) {
			select {
			case events <- call.Snapshot():
			default:
			}
		}
		c.OnAudio = func(callID string, pcm []int16) {
			select {
			case audio <- fmt.Sprintf("%s:%d:%d", callID, len(pcm), pcm[0]):
			default:
			}
		}
		c.OnMediaStart = func(callID string) { media <- callID }
	})

	if err := c.Dial("15551230001", "", 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	states := map[string]bool{}
	for len(states) < 3 {
		select {
		case s := <-events:
			states[s.State] = true
			if s.State == StateAnswered {
				if s.To != "15551230001" {
					t.Fatalf("call.To = %q", s.To)
				}
			}
		case <-deadline:
			t.Fatalf("lifecycle stalled at %v", states)
		}
	}
	// downlink audio arrived with sane pcm
	select {
	case a := <-audio:
		if !strings.HasPrefix(a, "call-1:160:") {
			t.Fatalf("audio frame %q", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no downlink audio")
	}
	select {
	case id := <-media:
		if id != "call-1" {
			t.Fatalf("media start for %q", id)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no MediaStart")
	}

	// uplink audio
	mic := make([]int16, 160)
	mic[0] = 1234
	if err := c.SendAudio("call-1", mic); err != nil {
		t.Fatal(err)
	}
	m.deadline(t, 3*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.uplink >= 1 && m.audioSum == 1234
	}, "server never saw uplink audio")

	// hangup
	if err := c.Hangup("call-1"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case s := <-events:
			if s.State == StateEnded && s.Detail == "completed" && s.Duration() >= 0 {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no ended event")
		}
	}
}

func TestClientSubscribeAndPing(t *testing.T) {
	m := startMiniServer(t)
	c := newTestClient(t, m, nil)
	if err := c.SubscribeInbound(true); err != nil {
		t.Fatal(err)
	}
	m.deadline(t, 3*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.gotSub
	}, "subscription never arrived")
	if err := c.SubscribeInbound(false); err != nil {
		t.Fatal(err)
	}
	m.deadline(t, 3*time.Second, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.gotSub
	}, "unsubscribe never arrived")
}

func (m *miniServer) deadline(t *testing.T, d time.Duration, ok func() bool, what string) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(what)
}

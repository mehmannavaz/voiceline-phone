// Package vcp implements the client side of the Voiceline Call Protocol
// (VCP): the encrypted custom protocol that caller applications — this
// repo's desktop app, the website and the APK — use to place and receive
// calls through a Voiceline line registered against a SIP provider.
//
// The wire contract lives in proto/webcall.proto (vendored from the
// voiceline server, the single source of truth; also served live at
// http://<server>:8086/proto).
//
// Transport: WebSocket (ws://host:8086/call), binary messages only.
//
//  1. PLAINTEXT handshake:
//     client -> HelloRequest { version, line_id, client_nonce }
//     server -> HelloResponse { version, server_nonce }  (error set => close)
//
//  2. KEY SCHEDULE (both sides):
//     master = PBKDF2-HMAC-SHA256(password,
//     salt = client_nonce || server_nonce, 60000, 96)
//     client_write_key = master[0:32]   // client -> server AES-256-GCM
//     server_write_key = master[32:64]  // server -> client AES-256-GCM
//     auth_key         = master[64:96]  // handshake proof
//
//  3. AUTH (first encrypted frame each direction, sequence 0):
//     client -> Envelope{ AuthConfirm{ proof = HMAC-SHA256(auth_key,
//     "VCP/1 client|" || line_id || "|" ||
//     client_nonce || "|" || server_nonce) } }
//     server -> Envelope{ AuthResult{ ok, error, line } }
//
//  4. SESSION: every binary message after the handshake is
//     [ 8-byte big-endian sequence ][ AES-256-GCM ciphertext ][ tag ]
//     with nonce = 00000000 || sequence, strictly +1 per direction.
//
// Audio is PCM16 little-endian, 8000 Hz, mono, 20 ms frames.
package vcp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Protocol constants — shared by every VCP implementation.
const (
	// ProtocolVersion is the VCP version this client speaks.
	ProtocolVersion uint32 = 1
	// NonceSize is the length of both handshake nonces.
	NonceSize = 16
	// KeyLen is the AES-256 key length.
	KeyLen = 32
	// MasterLen is the PBKDF2 output: two write keys plus the auth key.
	MasterLen = 3 * KeyLen
	// DefaultPBKDF2Iterations matches the server default.
	DefaultPBKDF2Iterations = 60000
	// proofLabel binds the auth proof to the protocol and direction.
	proofLabel = "VCP/1 client|"
	// frameSeqLen is the leading big-endian sequence on every frame.
	frameSeqLen = 8
	// GCMTagLen is the appended authentication tag.
	GCMTagLen = 16

	// SampleRate is the negotiated audio rate (L16).
	SampleRate = 8000
	// FrameMS is the ptime of one AudioFrame.
	FrameMS = 20
	// FrameSamples is the sample count of one 20 ms frame at 8 kHz.
	FrameSamples = 160
)

// ErrBadFrame reports a frame that is truncated, tampered, or whose
// sequence deviates — the session must be dropped.
var ErrBadFrame = errors.New("vcp: bad encrypted frame")

// keys is the session key material derived from the line's call password
// and the two handshake nonces.
type keys struct {
	clientWrite [KeyLen]byte // client -> server
	serverWrite [KeyLen]byte // server -> client
	auth        [KeyLen]byte // handshake proof
}

// deriveKeys expands the password into the three session keys. Both sides
// run exactly this: salt = client_nonce || server_nonce.
func deriveKeys(password string, clientNonce, serverNonce []byte, iterations int) (*keys, error) {
	if len(clientNonce) != NonceSize || len(serverNonce) != NonceSize {
		return nil, fmt.Errorf("vcp: nonces must be %d bytes (got %d/%d)",
			NonceSize, len(clientNonce), len(serverNonce))
	}
	if iterations < 1000 {
		iterations = DefaultPBKDF2Iterations
	}
	salt := make([]byte, 0, 2*NonceSize)
	salt = append(salt, clientNonce...)
	salt = append(salt, serverNonce...)
	master, err := pbkdf2.Key(sha256.New, password, salt, iterations, MasterLen)
	if err != nil {
		return nil, fmt.Errorf("vcp: pbkdf2: %w", err)
	}
	k := &keys{}
	copy(k.clientWrite[:], master[0:KeyLen])
	copy(k.serverWrite[:], master[KeyLen:2*KeyLen])
	copy(k.auth[:], master[2*KeyLen:3*KeyLen])
	return k, nil
}

// clientProof computes the HMAC proving the client derived the same auth
// key — i.e. that it knows the line's call password.
func clientProof(authKey [KeyLen]byte, lineID string, clientNonce, serverNonce []byte) []byte {
	h := hmac.New(sha256.New, authKey[:])
	h.Write([]byte(proofLabel))
	h.Write([]byte(lineID))
	h.Write([]byte("|"))
	h.Write(clientNonce)
	h.Write([]byte("|"))
	h.Write(serverNonce)
	return h.Sum(nil)
}

// frameCodec seals and opens one encrypted frame:
//
//	[ 8-byte big-endian sequence ][ AES-256-GCM ciphertext || 16-byte tag ]
//
// The GCM nonce is four zero bytes followed by the 8-byte sequence: with
// one key per direction and a strictly increasing sequence a nonce never
// repeats within a session.
type frameCodec struct {
	aead cipher.AEAD
}

func newFrameCodec(key [KeyLen]byte) (*frameCodec, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("vcp: aes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("vcp: gcm: %w", err)
	}
	return &frameCodec{aead: aead}, nil
}

func gcmNonce(seq uint64, out [12]byte) []byte {
	out[0], out[1], out[2], out[3] = 0, 0, 0, 0
	binary.BigEndian.PutUint64(out[4:], seq)
	return out[:]
}

// seal encrypts plaintext under the given sequence number.
func (c *frameCodec) seal(seq uint64, plaintext []byte) []byte {
	var nonce [12]byte
	out := make([]byte, frameSeqLen, frameSeqLen+len(plaintext)+GCMTagLen)
	binary.BigEndian.PutUint64(out, seq)
	out = c.aead.Seal(out, gcmNonce(seq, nonce), plaintext, nil)
	return out
}

// open decrypts a frame and enforces the expected sequence.
func (c *frameCodec) open(frame []byte, wantSeq uint64) ([]byte, error) {
	if len(frame) < frameSeqLen+GCMTagLen {
		return nil, ErrBadFrame
	}
	if binary.BigEndian.Uint64(frame[:frameSeqLen]) != wantSeq {
		return nil, ErrBadFrame
	}
	var nonce [12]byte
	plain, err := c.aead.Open(nil, gcmNonce(wantSeq, nonce), frame[frameSeqLen:], nil)
	if err != nil {
		return nil, ErrBadFrame
	}
	return plain, nil
}

// randomNonce returns the 16 fresh random bytes the client contributes.
func randomNonce() []byte {
	b := make([]byte, NonceSize)
	if _, err := rand.Read(b); err != nil {
		panic("vcp: entropy source failed: " + err.Error())
	}
	return b
}

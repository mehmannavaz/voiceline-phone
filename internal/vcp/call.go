package vcp

import (
	"sync"
	"time"
)

// Call states, as reported by CallEvent.state.
const (
	StateDialing  = "dialing"
	StateRinging  = "ringing"
	StateAnswered = "answered"
	StateEnded    = "ended"
	StateFailed   = "failed"
	StateIncoming = "incoming" // client-side only: offered, not yet decided
)

// Call directions.
const (
	CallOut = "out"
	CallIn  = "in"
)

// Call is one call on the session — outbound dialed from here, or
// inbound offered while subscribed. All fields are guarded; snapshot via
// Snapshot for UI lists.
type Call struct {
	ID string

	mu sync.Mutex

	Direction   string // CallOut | CallIn
	To          string // dialed destination (out) or the line's number (in)
	From        string // caller identity for inbound offers
	FromDisplay string
	State       string
	Detail      string
	AnsweredAt  time.Time
	EndedAt     time.Time

	mediaOn bool
	seq     uint32 // uplink frame counter
	ts      uint32 // uplink sample timestamp
}

// Snapshot is a stable copy for UI rendering.
type Snapshot struct {
	ID          string    `json:"id"`
	Direction   string    `json:"direction"`
	To          string    `json:"to"`
	From        string    `json:"from"`
	FromDisplay string    `json:"from_display"`
	State       string    `json:"state"`
	Detail      string    `json:"detail"`
	AnsweredAt  time.Time `json:"answered_at"`
	EndedAt     time.Time `json:"ended_at"`
	Media       bool      `json:"media"`
}

// Snapshot copies the call's mutable state.
func (c *Call) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Snapshot{
		ID: c.ID, Direction: c.Direction, To: c.To, From: c.From,
		FromDisplay: c.FromDisplay, State: c.State, Detail: c.Detail,
		AnsweredAt: c.AnsweredAt, EndedAt: c.EndedAt, Media: c.mediaOn,
	}
}

// Live reports whether the call can still act (dialing through answered).
func (s Snapshot) Live() bool {
	switch s.State {
	case StateDialing, StateRinging, StateAnswered, StateIncoming:
		return true
	}
	return false
}

// Connected reports whether media flows.
func (s Snapshot) Connected() bool { return s.State == StateAnswered && s.Media }

// Duration returns the connected phase length when the call has ended.
func (s Snapshot) Duration() time.Duration {
	if s.AnsweredAt.IsZero() {
		return 0
	}
	end := s.EndedAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(s.AnsweredAt)
}

// Title is the human identity of the far side.
func (s Snapshot) Title() string {
	if s.Direction == CallIn {
		if s.FromDisplay != "" {
			return s.FromDisplay
		}
		if s.From != "" {
			return s.From
		}
	}
	return s.To
}

func (c *Call) setMedia(on bool) {
	c.mu.Lock()
	c.mediaOn = on
	c.mu.Unlock()
}

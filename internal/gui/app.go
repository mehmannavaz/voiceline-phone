// Package gui is the voiceline-phone desktop app: a softphone speaking
// the Voiceline Call Protocol (VCP) — dial through the line, receive the
// line's calls, DTMF, transfer, group calls — with real microphone and
// speaker audio via miniaudio.
package gui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/mehmannavaz/voiceline-phone/internal/audioio"
	"github.com/mehmannavaz/voiceline-phone/internal/vcp"
)

// settings persist the login between runs (never the password unless
// the user opts in).
type settings struct {
	Server   string `json:"server"`
	LineID   string `json:"line_id"`
	Display  string `json:"display"`
	Receive  bool   `json:"receive"`
	Remember bool   `json:"remember"`
	Password string `json:"password,omitempty"`
}

func settingsPath() string {
	base, err := os.UserConfigDir()
	if err != nil {
		base = "."
	}
	dir := filepath.Join(base, "voiceline-phone")
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, "settings.json")
}

func loadSettings() settings {
	s := settings{Server: "ws://127.0.0.1:8086/call", Receive: true}
	if raw, err := os.ReadFile(settingsPath()); err == nil {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func saveSettings(s settings) {
	if !s.Remember {
		s.Password = ""
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(settingsPath(), raw, 0o600)
}

// App owns the window, the protocol client and the audio engine.
type App struct {
	fyne     fyne.App
	win      fyne.Window
	log      *slog.Logger
	settings settings

	mu     sync.Mutex
	client *vcp.Client
	audio  *audioio.Engine
	// audioNotice explains degraded audio after login.
	audioNotice string

	login *loginView
	phone *phoneView

	ringing bool
}

// Run shows the window and blocks until it closes.
func Run() error {
	a := &App{
		fyne:     app.NewWithID("io.github.mehmannavaz.voiceline-phone"),
		log:      slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		settings: loadSettings(),
	}
	a.win = a.fyne.NewWindow("Voiceline Phone")
	a.win.Resize(fyne.NewSize(400, 680))
	a.win.SetIcon(appIcon())

	a.login = newLoginView(a)
	a.phone = newPhoneView(a)
	a.win.SetContent(a.login.root)
	a.startTicker()
	a.win.ShowAndRun()

	a.disconnect()
	return nil
}

// connect logs in and switches to the phone view.
func (a *App) connect(server, lineID, password, display string, receive bool) {
	go func() {
		c := vcp.NewClient(vcp.Options{
			URL: server, LineID: lineID, Password: password,
			DisplayName: display, UserAgent: userAgent,
			AcceptInbound: receive,
			Log:           a.log,
		})

		aud, err := audioio.Start(a.mic)
		if err != nil {
			// Audio is degraded, not fatal: the app can still connect,
			// dial and signal (e.g. Android before the microphone
			// permission is granted); a note tells the user.
			a.log.Warn("audio unavailable", "error", err.Error())
			aud = nil
			a.audioNotice = "microphone/speakers unavailable: " + err.Error()
		} else {
			aud.SetRing(false)
		}

		a.wire(c, aud)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err = c.Connect(ctx)
		cancel()
		if err != nil {
			if aud != nil {
				aud.Close()
			}
			a.login.fail(err.Error())
			return
		}

		a.mu.Lock()
		a.client, a.audio = c, aud
		a.mu.Unlock()

		a.login.clearFail()
		fyne.Do(func() { a.showPhone() })
	}()
}

// wire installs every protocol callback onto the client.
func (a *App) wire(c *vcp.Client, aud *audioio.Engine) {
	c.OnLine = func(l vcp.LineInfo) {
		fyne.Do(func() { a.phone.setLine(l) })
	}
	c.OnCallEvent = func(call *vcp.Call) {
		s := call.Snapshot()
		if s.State == vcp.StateEnded || s.State == vcp.StateFailed {
			aud.EndCall(s.ID)
		}
		fyne.Do(func() { a.phone.updateCall(s) })
	}
	c.OnIncoming = func(inc vcp.Incoming) {
		fyne.Do(func() { a.phone.incoming(inc) })
	}
	c.OnDtmf = func(callID, digit, method string) {
		fyne.Do(func() { a.phone.note(fmt.Sprintf("digit %s from the far end", digit)) })
	}
	c.OnTransfer = func(p vcp.TransferProgress) {
		fyne.Do(func() { a.phone.note(fmt.Sprintf("transfer %s: %s", p.State, p.Detail)) })
	}
	c.OnConference = func(p vcp.ConferenceProgress) {
		fyne.Do(func() {
			if p.Action == "joined" {
				a.phone.note(fmt.Sprintf("%s joined the call (%d in the room)", p.Participant, p.Participants))
			} else {
				a.phone.note(fmt.Sprintf("%s left the call (%d remain)", p.Participant, p.Participants))
			}
		})
	}
	c.OnAudio = func(callID string, pcm []int16) {
		aud.Play(callID, pcm)
	}
	c.OnBye = func(reason string, code uint32) {
		fyne.Do(func() { a.phone.note("server closed the session: " + reason) })
	}
	c.OnClosed = func(err error) {
		fyne.Do(func() { a.sessionEnded() })
	}
}

// showPhone swaps to the softphone view.
func (a *App) showPhone() {
	a.phone.reset()
	a.win.SetContent(a.phone.root)
}

// showLogin swaps back to the login view.
func (a *App) showLogin() {
	a.ring(false)
	a.win.SetContent(a.login.root)
}

// sessionEnded is the OnClosed path: tear audio + client down.
func (a *App) sessionEnded() {
	a.disconnect()
	a.showLogin()
	a.login.fail("connection lost")
}

// disconnect stops the client and audio engine.
func (a *App) disconnect() {
	a.mu.Lock()
	c, aud := a.client, a.audio
	a.client, a.audio = nil, nil
	a.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if aud != nil {
		aud.Close()
	}
	a.ring(false)
}

// ring controls the ringtone with reference counting from the view.
func (a *App) ring(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.audio != nil {
		a.audio.SetRing(on)
	}
	a.ringing = on
}

// dial places a call through the connected client.
func (a *App) dial(to string) {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c == nil {
		return
	}
	if err := c.Dial(to, a.settings.Display, 0); err != nil {
		a.phone.note(err.Error())
	}
}

// act runs a protocol action with the current client.
func (a *App) act(fn func(c *vcp.Client) error) {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c == nil {
		return
	}
	if err := fn(c); err != nil {
		a.phone.note(err.Error())
	}
}

// mic distributes microphone audio to every answered call.
func (a *App) mic(pcm []int16) {
	a.mu.Lock()
	c := a.client
	a.mu.Unlock()
	if c == nil {
		return
	}
	for _, call := range c.Calls() {
		s := call.Snapshot()
		if s.Connected() {
			_ = c.SendAudio(s.ID, pcm)
		}
	}
}

// userAgent identifies the build on the wire.
var userAgent = "voiceline-phone/1.0"

// oneTicker drives the live duration labels.
func (a *App) startTicker() {
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for range t.C {
			a.mu.Lock()
			alive := a.client != nil
			a.mu.Unlock()
			if !alive {
				continue
			}
			fyne.Do(func() { a.phone.tick() })
		}
	}()
}

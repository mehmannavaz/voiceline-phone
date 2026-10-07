package gui

import (
	"fmt"
	"sort"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"image/color"

	"github.com/mehmannavaz/voiceline-phone/internal/vcp"
)

var (
	brandGreen = color.NRGBA{R: 22, G: 163, B: 74, A: 255} // #16A34A
	brandRed   = color.NRGBA{R: 220, G: 38, B: 38, A: 255} // #DC2626
)

// phoneView is the softphone screen.
type phoneView struct {
	app  *App
	root fyne.CanvasObject

	lineNum  *widget.Label
	lineStat *widget.Label

	number *widget.Entry
	calls  *fyne.Container
	cards  map[string]*callCard
	order  []string

	notes *widget.Label
}

func newPhoneView(a *App) *phoneView {
	v := &phoneView{app: a, cards: map[string]*callCard{}}

	v.lineNum = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	v.lineStat = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	v.notes = widget.NewLabel("")
	v.notes.Wrapping = fyne.TextWrapWord
	v.notes.Hide()

	v.number = widget.NewEntry()
	v.number.PlaceHolder = "number to call"

	pad := newKeypad(v.number)

	callBtn := widget.NewButtonWithIcon("CALL", theme.LoginIcon(), func() {
		if v.number.Text == "" {
			v.note("type a number first")
			return
		}
		v.app.dial(v.number.Text)
	})
	callBtn.Importance = widget.HighImportance

	hangAll := widget.NewButtonWithIcon("Hang up everything", theme.CancelIcon(), func() {
		v.app.act(func(c *vcp.Client) error {
			for _, call := range c.Calls() {
				if s := call.Snapshot(); s.Live() {
					_ = c.Hangup(s.ID)
				}
			}
			return nil
		})
	})
	hangAll.Importance = widget.DangerImportance

	header := container.NewVBox(
		v.lineNum,
		v.lineStat,
		widget.NewSeparator(),
	)

	v.calls = container.NewVBox()

	v.root = container.NewBorder(
		header,
		container.NewVBox(v.notes, widget.NewSeparator()),
		nil, nil,
		container.NewVBox(
			v.number,
			pad,
			callBtn,
			widget.NewSeparator(),
			container.NewVScroll(v.calls),
			hangAll,
		),
	)
	return v
}

// reset clears state when (re)entering the phone view.
func (v *phoneView) reset() {
	v.cards = map[string]*callCard{}
	v.order = nil
	v.calls.Objects = nil
	v.calls.Refresh()
	v.notes.Hide()
}

// setLine updates the header.
func (v *phoneView) setLine(l vcp.LineInfo) {
	v.lineNum.SetText("☎ " + displayNumber(l))
	stat := fmt.Sprintf("line %s · %s · %s", l.ID, l.RegState, hookLabel(l.Hook))
	if l.MaxConcurrent > 0 {
		stat += fmt.Sprintf(" · %d/%d calls", l.ActiveCalls, l.MaxConcurrent)
	} else {
		stat += fmt.Sprintf(" · %d live", l.ActiveCalls)
	}
	v.lineStat.SetText(stat)
}

// incoming handles an offered call: ringtone + note (the card shows the
// accept/reject buttons).
func (v *phoneView) incoming(inc vcp.Incoming) {
	who := inc.From
	if inc.FromDisplay != "" {
		who = inc.FromDisplay + " (" + inc.From + ")"
	}
	v.note(fmt.Sprintf("incoming call from %s — ring window %ds", who, inc.RingSec))
	v.app.ring(true)
}

// updateCall refreshes one card from a snapshot.
func (v *phoneView) updateCall(s vcp.Snapshot) {
	card, ok := v.cards[s.ID]
	if !ok {
		card = newCallCard(v.app, s.ID)
		v.cards[s.ID] = card
	}
	card.apply(s)
	if s.State == vcp.StateEnded || s.State == vcp.StateFailed {
		v.app.ring(false)
	}
	v.refresh()
}

// tick refreshes the live duration labels.
func (v *phoneView) tick() {
	for _, id := range v.order {
		if card, ok := v.cards[id]; ok {
			card.refreshDuration()
		}
	}
}

// refresh re-renders the call list (live first).
func (v *phoneView) refresh() {
	type entry struct {
		id   string
		card *callCard
	}
	list := make([]entry, 0, len(v.cards))
	for id, card := range v.cards {
		list = append(list, entry{id: id, card: card})
	}
	sort.SliceStable(list, func(i, j int) bool {
		si, sj := orderScore(list[i].card.snap.State), orderScore(list[j].card.snap.State)
		if si != sj {
			return si < sj
		}
		return list[i].id < list[j].id
	})

	objs := make([]fyne.CanvasObject, 0, len(list))
	v.order = nil
	for _, e := range list {
		objs = append(objs, e.card.root)
		v.order = append(v.order, e.id)
	}
	if len(list) == 0 {
		objs = append(objs, widget.NewLabel("no calls yet"))
	}
	v.calls.Objects = objs
	v.calls.Refresh()
}

// note shows a transient status line.
func (v *phoneView) note(msg string) {
	if v.notes.Hidden {
		v.notes.Show()
	}
	v.notes.SetText(msg)
	go func() {
		time.Sleep(6 * time.Second)
		fyne.Do(func() {
			if v.notes.Text == msg {
				v.notes.Hide()
			}
		})
	}()
}

// orderScore sorts live calls to the top.
func orderScore(state string) int {
	switch state {
	case vcp.StateIncoming:
		return 0
	case vcp.StateDialing, vcp.StateRinging:
		return 1
	case vcp.StateAnswered:
		return 2
	default:
		return 9
	}
}

func displayNumber(l vcp.LineInfo) string {
	if l.DisplayName != "" {
		return fmt.Sprintf("%s · %s", l.DisplayName, l.Number)
	}
	return l.Number
}

func hookLabel(hook string) string {
	switch hook {
	case "on_hook":
		return "idle"
	case "off_hook":
		return "call up"
	case "disconnected":
		return "no registration"
	}
	return hook
}

// ---- call card ----

// callCard renders one call with its actions.
type callCard struct {
	app  *App
	id   string
	root *widget.Card

	snap vcp.Snapshot

	title   *widget.Label
	state   *widget.Label
	dur     *widget.Label
	actions *fyne.Container
}

func newCallCard(a *App, id string) *callCard {
	c := &callCard{app: a, id: id}
	c.title = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	c.state = widget.NewLabel("")
	c.dur = widget.NewLabel("")
	c.dur.TextStyle = fyne.TextStyle{Monospace: true}
	c.actions = container.NewVBox()
	c.root = widget.NewCard("", "", container.NewVBox(
		container.NewBorder(nil, nil, c.title, c.dur, nil),
		c.state,
		c.actions,
	))
	return c
}

// apply updates the card from a snapshot.
func (c *callCard) apply(s vcp.Snapshot) {
	c.snap = s
	c.title.SetText(callTitle(s))
	c.state.SetText(callState(s))
	c.dur.SetText("")
	c.actions.Objects = nil

	switch s.State {
	case vcp.StateIncoming:
		accept := widget.NewButtonWithIcon("Accept", theme.ConfirmIcon(), func() {
			c.app.ring(false)
			c.app.act(func(cl *vcp.Client) error { return cl.Accept(c.id) })
		})
		accept.Importance = widget.HighImportance
		reject := widget.NewButtonWithIcon("Reject", theme.CancelIcon(), func() {
			c.app.ring(false)
			c.app.act(func(cl *vcp.Client) error { return cl.Reject(c.id, "declined") })
		})
		reject.Importance = widget.DangerImportance
		c.actions.Add(container.NewGridWithColumns(2, accept, reject))

	case vcp.StateDialing, vcp.StateRinging:
		hang := widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), func() {
			c.app.act(func(cl *vcp.Client) error { return cl.Hangup(c.id) })
		})
		hang.Importance = widget.DangerImportance
		c.actions.Add(hang)

	case vcp.StateAnswered:
		var mute *widget.Button
		if c.muted() {
			mute = widget.NewButtonWithIcon("Unmute", theme.MediaPlayIcon(), func() { c.toggleMute() })
		} else {
			mute = widget.NewButtonWithIcon("Mute", theme.MediaPauseIcon(), func() { c.toggleMute() })
		}
		keys := widget.NewButtonWithIcon("Keys", theme.GridIcon(), func() { c.dtmf() })
		xfer := widget.NewButtonWithIcon("Transfer", theme.MailForwardIcon(), func() { c.transfer() })
		group := widget.NewButtonWithIcon("Add person", theme.ContentAddIcon(), func() { c.conference() })
		hang := widget.NewButtonWithIcon("Hang up", theme.CancelIcon(), func() {
			c.app.act(func(cl *vcp.Client) error { return cl.Hangup(c.id) })
		})
		hang.Importance = widget.DangerImportance
		c.actions.Add(container.NewGridWithColumns(2, mute, keys))
		c.actions.Add(container.NewGridWithColumns(3, xfer, group, hang))

	default:
		if s.Detail != "" {
			c.actions.Add(widget.NewLabel("detail: " + s.Detail))
		}
	}
	c.refreshDuration()
}

func (c *callCard) refreshDuration() {
	if c.snap.State == vcp.StateAnswered {
		c.dur.SetText(fmtDuration(c.snap.Duration()))
	}
}

func (c *callCard) muted() bool {
	c.app.mu.Lock()
	aud := c.app.audio
	c.app.mu.Unlock()
	return aud != nil && aud.Muted()
}

func (c *callCard) toggleMute() {
	c.app.mu.Lock()
	aud := c.app.audio
	c.app.mu.Unlock()
	if aud == nil {
		return
	}
	aud.SetMuted(!aud.Muted())
	c.apply(c.snap)
}

// dtmf opens the in-call keypad.
func (c *callCard) dtmf() {
	pad := container.NewGridWithColumns(3)
	sent := widget.NewLabel("")
	for _, d := range []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"} {
		digit := d
		pad.Add(widget.NewButton(digit, func() {
			c.app.act(func(cl *vcp.Client) error { return cl.SendDTMF(c.id, digit) })
			sent.SetText(sent.Text + digit)
		}))
	}
	dlg := dialog.NewCustomConfirm("Send digits", "Close", "", container.NewVBox(sent, pad), func(bool) {}, c.app.win)
	dlg.Show()
}

// transfer opens the blind-transfer dialog.
func (c *callCard) transfer() {
	entry := widget.NewEntry()
	entry.PlaceHolder = "outside number, e.g. +989221234567"
	form := dialog.NewForm("Transfer the caller", "Transfer", "Cancel", []*widget.FormItem{
		widget.NewFormItem("To", entry),
	}, func(ok bool) {
		if !ok || entry.Text == "" {
			return
		}
		c.app.act(func(cl *vcp.Client) error { return cl.Transfer(c.id, entry.Text) })
	}, c.app.win)
	form.Show()
}

// conference adds a person to the call.
func (c *callCard) conference() {
	entry := widget.NewEntry()
	entry.PlaceHolder = "person to add, e.g. 09991234567"
	form := dialog.NewForm("Add person to the call", "Add", "Cancel", []*widget.FormItem{
		widget.NewFormItem("To", entry),
	}, func(ok bool) {
		if !ok || entry.Text == "" {
			return
		}
		c.app.act(func(cl *vcp.Client) error { return cl.Conference(c.id, entry.Text, "") })
	}, c.app.win)
	form.Show()
}

func callTitle(s vcp.Snapshot) string {
	dir := "↗"
	if s.Direction == vcp.CallIn {
		dir = "↙"
	}
	who := s.Title()
	if who == "" && len(s.ID) >= 8 {
		who = s.ID[:8]
	}
	return fmt.Sprintf("%s %s", dir, who)
}

func callState(s vcp.Snapshot) string {
	switch s.State {
	case vcp.StateIncoming:
		return "incoming — ringing"
	case vcp.StateDialing:
		return "dialing…"
	case vcp.StateRinging:
		return "ringing…"
	case vcp.StateAnswered:
		if s.Media {
			return "connected — audio flowing"
		}
		return "answered"
	case vcp.StateEnded:
		return "ended"
	case vcp.StateFailed:
		return "failed"
	}
	return s.State
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", sec/60, sec%60)
}

// newKeypad builds the dial pad.
func newKeypad(entry *widget.Entry) fyne.CanvasObject {
	grid := container.NewGridWithColumns(3)
	keys := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"}
	for _, k := range keys {
		key := k
		grid.Add(widget.NewButton(key, func() {
			entry.SetText(entry.Text + key)
		}))
	}
	clear := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
		t := entry.Text
		if t != "" {
			entry.SetText(t[:len(t)-1])
		}
	})
	grid.Add(clear)
	grid.Add(widget.NewButton("+", func() { entry.SetText(entry.Text + "+") }))
	return container.NewPadded(grid)
}

// statusDot renders the connection indicator.
func statusDot(ok bool) fyne.CanvasObject {
	if ok {
		return canvas.NewCircle(brandGreen)
	}
	return canvas.NewCircle(brandRed)
}

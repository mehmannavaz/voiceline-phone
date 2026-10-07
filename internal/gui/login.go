package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// loginView is the connect screen.
type loginView struct {
	app  *App
	root fyne.CanvasObject

	server  *widget.Entry
	line    *widget.Entry
	pass    *widget.Entry
	display *widget.Entry
	receive *widget.Check
	keep    *widget.Check
	connect *widget.Button
	status  *widget.Label
}

func newLoginView(a *App) *loginView {
	v := &loginView{app: a}
	s := a.settings

	v.server = widget.NewEntry()
	v.server.SetText(s.Server)
	v.server.Validator = func(string) error { return nil }

	v.line = widget.NewEntry()
	v.line.SetText(s.LineID)

	v.pass = widget.NewPasswordEntry()
	v.pass.SetText(s.Password)
	v.pass.PlaceHolder = "line call password"

	v.display = widget.NewEntry()
	v.display.SetText(s.Display)
	v.display.PlaceHolder = "shown to callees (optional)"

	v.receive = widget.NewCheck("Receive this line's calls here (rings)", nil)
	v.receive.SetChecked(s.Receive)

	v.keep = widget.NewCheck("Remember (stores the password on this device)", nil)
	v.keep.SetChecked(s.Remember)

	v.status = widget.NewLabel("")
	v.status.Hide()
	v.status.Importance = widget.HighImportance

	v.connect = widget.NewButtonWithIcon("Connect", theme.LoginIcon(), func() {
		v.connectNow()
	})
	v.connect.Importance = widget.HighImportance

	form := widget.NewForm(
		widget.NewFormItem("Server", v.server),
		widget.NewFormItem("Line", v.line),
		widget.NewFormItem("Password", v.pass),
		widget.NewFormItem("Display name", v.display),
		widget.NewFormItem("", v.receive),
		widget.NewFormItem("", v.keep),
	)

	header := container.NewVBox(
		logoBanner(),
		widget.NewLabelWithStyle("Voiceline Phone", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		widget.NewLabelWithStyle("the encrypted call protocol client", fyne.TextAlignCenter, fyne.TextStyle{Italic: true}),
	)

	v.root = container.NewVScroll(container.NewMax(container.NewPadded(container.NewVBox(
		header,
		form,
		v.connect,
		v.status,
	))))
	return v
}

// connectNow validates and dials the server.
func (v *loginView) connectNow() {
	server := normalizeServer(v.server.Text)
	line := v.line.Text
	pass := v.pass.Text
	display := v.display.Text
	if line == "" || pass == "" {
		v.fail("line id and call password are required")
		return
	}
	if server == "" {
		v.fail("server is required (ws://host:8086/call)")
		return
	}

	v.status.Hide()
	v.connect.Disable()
	v.connect.SetText("Connecting…")
	defer func() {
		v.connect.Enable()
		v.connect.SetText("Connect")
	}()

	v.app.settings = settings{
		Server: server, LineID: line, Display: display,
		Receive: v.receive.Checked, Remember: v.keep.Checked, Password: pass,
	}
	saveSettings(v.app.settings)
	v.app.connect(server, line, pass, display, v.receive.Checked)
}

// fail shows a login error.
func (v *loginView) fail(msg string) {
	fyne.Do(func() {
		v.status.SetText(msg)
		v.status.Show()
		v.connect.Enable()
		v.connect.SetText("Connect")
		dialog.ShowInformation("Voiceline Phone", msg, v.app.win)
	})
}

// clearFail hides the error.
func (v *loginView) clearFail() {
	fyne.Do(func() { v.status.Hide() })
}

// logoBanner draws the top-of-login wordmark block.
func logoBanner() fyne.CanvasObject {
	bg := canvas.NewRectangle(brandGreen)
	bg.SetMinSize(fyne.NewSize(96, 96))
	circle := canvas.NewCircle(color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	circle.Resize(fyne.NewSize(72, 72))
	return container.NewStack(bg, container.NewPadded(circle))
}

// normalizeServer appends the /call endpoint when only host:port given.
func normalizeServer(s string) string {
	if s == "" {
		return ""
	}
	switch {
	case len(s) > 6 && s[:6] == "ws://", len(s) > 7 && s[:7] == "wss://":
		if len(s) > 5 && s[len(s)-5:] == "/call" {
			return s
		}
		return s + "/call"
	default:
		return "ws://" + s + "/call"
	}
}

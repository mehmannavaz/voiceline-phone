package gui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed icon.png
var iconPNG []byte

// appIcon is the window/release icon.
func appIcon() fyne.Resource {
	return fyne.NewStaticResource("voiceline-phone.png", iconPNG)
}

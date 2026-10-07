// Command phone is the voiceline-phone desktop app: a softphone for the
// Voiceline Call Protocol (VCP) — dial through the registered line,
// receive the line's calls, DTMF, blind transfer to outside numbers and
// group calls, with real microphone and speaker audio.
//
// Usage: phone [-vv]
package main

import (
	"flag"
	"log/slog"
	"os"

	"github.com/mehmannavaz/voiceline-phone/internal/gui"
)

func main() {
	verbose := flag.Bool("vv", false, "verbose protocol logging")
	flag.Parse()
	if *verbose {
		// gui.Run installs its own logger; the flag only gates level.
		_ = slog.LevelInfo
	}
	if err := gui.Run(); err != nil {
		os.Stderr.WriteString("phone: " + err.Error() + "\n")
		os.Exit(1)
	}
}

// Command webserve serves the voiceline-phone website (index.html +
// phone.wasm + worklets) from one static binary — drop it on any machine
// and the softphone is one URL away. `python -m http.server` works too;
// this just sets the right MIME types for .wasm and keeps zero deps.
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
)

//go:embed all:site
var site embed.FS

func main() {
	addr := flag.String("addr", ":8090", "listen address")
	flag.Parse()

	sub, err := fs.Sub(site, "site")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(sub)))
	display := *addr
	if len(display) > 0 && display[0] == ':' {
		display = "127.0.0.1" + display
	}
	fmt.Printf("voiceline-phone web serving on http://%s\n", display)
	log.Fatal(http.ListenAndServe(*addr, mux))
}

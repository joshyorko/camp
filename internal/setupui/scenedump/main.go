// Command scenedump renders a static Trailhead scene frame to stdout as ANSI
// for visual review and golden capture. It composes the same renderer the live
// setup program uses, with deterministic sample data, so a PNG made from its
// output (via tools/ansishot) is a faithful preview of the real scene.
//
// Usage:
//
//	go run ./internal/setupui/scenedump -w 120 -h 40 -state ready
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/joshyorko/camp/internal/setupui"
)

func main() {
	w := flag.Int("w", 120, "width in cells")
	h := flag.Int("h", 40, "height in cells")
	state := flag.String("state", "ready", setupui.SampleStateUsage())
	out := flag.String("out", "", "optional ANSI output file")
	flag.Parse()

	sprites, err := setupui.LoadSprites()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pal := setupui.DefaultPalette()
	frame := setupui.SampleFrame(*state, *w, *h, pal, sprites)
	if *out != "" {
		if err := os.WriteFile(*out, []byte(frame), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	fmt.Print(frame)
}

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshyorko/camp/internal/cli"
	"github.com/joshyorko/camp/internal/docsgen"
)

func main() {
	files, err := docsgen.Generate(cli.NewRoot())
	if err != nil {
		fatal(err)
	}
	for name, contents := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			fatal(err)
		}
		if err := os.WriteFile(name, contents, 0o644); err != nil {
			fatal(err)
		}
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

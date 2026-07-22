package main

import (
	"context"
	"fmt"
	"os"

	"github.com/joshyorko/camp/internal/cli"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
	dirty     = "true"
)

func run(args []string, streams cli.Streams) int {
	root := cli.NewRoot()
	root.Version = fmt.Sprintf("%s (commit %s, built %s, dirty %s)", version, commit, buildDate, dirty)
	return cli.Execute(context.Background(), root, args, streams)
}

func main() {
	os.Exit(run(os.Args[1:], cli.Streams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}))
}

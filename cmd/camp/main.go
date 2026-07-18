package main

import (
	"context"
	"os"

	"github.com/joshyorko/camp/internal/cli"
)

func run(args []string, streams cli.Streams) int {
	return cli.Execute(context.Background(), cli.NewRoot(), args, streams)
}

func main() {
	os.Exit(run(os.Args[1:], cli.Streams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}))
}

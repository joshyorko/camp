package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/joshyorko/camp/internal/cli"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
	dirty     = "true"
)

func run(args []string, streams cli.Streams) int {
	if len(args) == 1 && args[0] == "__remote-worker" {
		if err := cli.RunRemoteWorker(context.Background(), streams); err != nil {
			return int(cli.ExitFailure)
		}
		return int(cli.ExitSuccess)
	}
	if len(args) == 3 && args[0] == "__remote-worker-gate" {
		if err := cli.RunRemoteWorkerGate(context.Background(), args[1], args[2], streams); err != nil {
			return int(cli.ExitFailure)
		}
		return int(cli.ExitSuccess)
	}
	if len(args) == 3 && args[0] == "__remote-worker-await" {
		if err := cli.AwaitRemoteWorkerGate(context.Background(), args[1], args[2]); err != nil {
			return int(cli.ExitFailure)
		}
		return int(cli.ExitSuccess)
	}
	if len(args) == 3 && args[0] == "__doctor-listener" {
		port, err := strconv.Atoi(args[1])
		if err != nil {
			return int(cli.ExitUsage)
		}
		if err := cli.RunDoctorProbeListener(context.Background(), port, args[2]); err != nil {
			return int(cli.ExitFailure)
		}
		return int(cli.ExitSuccess)
	}
	root := cli.NewRoot()
	root.Version = fmt.Sprintf("%s (commit %s, built %s, dirty %s)", version, commit, buildDate, dirty)
	return cli.Execute(context.Background(), root, args, streams)
}

func main() {
	os.Exit(run(os.Args[1:], cli.Streams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}))
}

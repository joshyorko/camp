package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/cli"
)

func TestRootCommandIsSingleCampCobraBinary(t *testing.T) {
	t.Parallel()

	command := cli.NewRoot()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if command.Use != "camp" || !strings.Contains(output.String(), "Recoverable capsule workspaces") {
		t.Fatalf("unexpected root command: use=%q output=%q", command.Use, output.String())
	}
}

func TestRunDelegatesProcessBoundaryToCLI(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"open"}, cli.Streams{Out: &stdout, ErrOut: &stderr})

	if exitCode != int(cli.ExitUsage) {
		t.Fatalf("exit code = %d, want %d", exitCode, cli.ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, want unknown-command error", stderr.String())
	}
}

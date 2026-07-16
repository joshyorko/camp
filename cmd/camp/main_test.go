package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRootCommandIsSingleCampCobraBinary(t *testing.T) {
	command := newRootCommand()
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

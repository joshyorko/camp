package main

import (
	"bytes"
	"os"
	"path/filepath"
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
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"open"}, cli.Streams{Out: &stdout, ErrOut: &stderr})

	if exitCode == int(cli.ExitSuccess) {
		t.Fatal("open unexpectedly succeeded without a configured source or remote")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q, command was not registered", stderr.String())
	}
}

func TestRunProductionInitUsesXDGAndDockerManifestBoundary(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	docker := filepath.Join(bin, "docker")
	body := "#!/bin/sh\nprintf '[{\"Descriptor\":{\"digest\":\"sha256:" + strings.Repeat("a", 64) + "\"}}]'\n"
	if err := os.WriteFile(docker, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	brain := filepath.Join(root, "brain")
	if err := os.Mkdir(brain, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--json", "init", brain, "--name", "brain"}, cli.Streams{Out: &stdout, ErrOut: &stderr}); code != int(cli.ExitSuccess) {
		t.Fatalf("exit = %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"kind":"init"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
	for _, name := range []string{"capsule.yaml", "lock.yaml", "images.json", "hauler-manifest.yaml"} {
		if _, err := os.Stat(filepath.Join(brain, ".camp", name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

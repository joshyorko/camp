package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/cli"
	"github.com/joshyorko/camp/internal/remoteworker"
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

func TestRunRegistersHiddenRemoteWorkerCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"__remote-worker"}, cli.Streams{
		In:     strings.NewReader("{}"),
		Out:    &stdout,
		ErrOut: &stderr,
	})
	if exitCode == int(cli.ExitSuccess) {
		t.Fatal("__remote-worker unexpectedly accepted an invalid request")
	}
	if !strings.Contains(stdout.String(), `"operation":"rejected"`) ||
		!strings.Contains(stdout.String(), `"code":"invalid_request"`) ||
		strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	help := renderRootHelp(t)
	if strings.Contains(help, "__remote-worker") {
		t.Fatalf("hidden command appeared in help: %q", help)
	}
}

func TestRunRegistersHiddenRemoteServiceSupervisorCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"__remote-service-supervisor"}, cli.Streams{
		In: strings.NewReader("{}"), Out: &stdout, ErrOut: &stderr,
	})
	if exitCode == int(cli.ExitSuccess) || strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if strings.Contains(renderRootHelp(t), "__remote-service-supervisor") {
		t.Fatal("hidden remote service supervisor appeared in help")
	}
}

func TestRunRemoteWorkerKeepsProtocolDiagnosticsOffStderr(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "manifest.json")
	kit := filepath.Join(root, "camp-hauler-kit.tar.zst")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kit, []byte("kit"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := remoteworker.Request{
		SchemaVersion: remoteworker.ProtocolSchemaVersion,
		Operation:     remoteworker.OperationHydrate,
		SessionID:     "session-1",
		WorkspaceRoot: root,
		RuntimeRoot:   root,
		ManifestPath:  manifest,
		Expected: remoteworker.ExpectedIdentity{
			Architecture: "linux/" + runtime.GOARCH,
			Helper:       commandIdentity(t, "camp", executable),
			Kit:          commandIdentity(t, filepath.Base(kit), kit),
			Manifest:     commandIdentity(t, filepath.Base(manifest), manifest),
			Image:        "example/final@sha256:" + strings.Repeat("a", 64),
		},
	}
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	exitCode := run([]string{"__remote-worker"}, cli.Streams{
		In: bytes.NewReader(body), Out: &stdout, ErrOut: &stderr,
	})
	if exitCode != int(cli.ExitFailure) || stdout.Len() == 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func commandIdentity(t *testing.T, name, path string) remoteworker.FileIdentity {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return remoteworker.FileIdentity{
		Name: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body)),
	}
}

func renderRootHelp(t *testing.T) string {
	t.Helper()
	command := cli.NewRoot()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	return output.String()
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

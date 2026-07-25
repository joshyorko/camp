package docsgen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/cli"
)

func TestDocumentedInvocationsCoverEveryVisibleCommand(t *testing.T) {
	want := make(map[string]bool)
	for _, command := range visibleCommands(cli.NewRoot()) {
		want[command.CommandPath()] = true
	}
	for _, invocation := range DocumentedInvocations() {
		delete(want, invocation.CommandPath)
	}
	if len(want) != 0 {
		t.Fatalf("visible commands lack deterministic transcript coverage: %v", want)
	}
}

func TestDocumentedInvocationsExecute(t *testing.T) {
	for _, invocation := range DocumentedInvocations() {
		t.Run(strings.ReplaceAll(invocation.CommandPath, " ", "_"), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := cli.Execute(context.Background(), transcriptRoot(), invocation.Args, cli.Streams{Out: &stdout, ErrOut: &stderr})
			if code != 0 {
				t.Fatalf("%v exited %d\nstdout:\n%s\nstderr:\n%s", invocation.Args, code, stdout.String(), stderr.String())
			}
			if invocation.CommandPath != "camp" && invocation.CommandPath != "camp completion" {
				marker := fmt.Sprintf("effect-free docs fixture: %s dispatched; external effects disabled", strings.TrimPrefix(invocation.CommandPath, "camp "))
				if !strings.Contains(stdout.String(), marker) {
					t.Fatalf("%v did not emit dispatch marker %q\nstdout:\n%s", invocation.Args, marker, stdout.String())
				}
			}
		})
	}
}

func TestGeneratedReferenceIncludesHelpFlags(t *testing.T) {
	generated, err := Generate(cli.NewRoot())
	if err != nil {
		t.Fatal(err)
	}
	reference := generated["docs/generated/commands.md"]
	for _, command := range visibleCommands(cli.NewRoot()) {
		heading := []byte("## `" + command.CommandPath() + "`\n")
		start := bytes.Index(reference, heading)
		if start == -1 {
			t.Fatalf("generated reference lacks %q", command.CommandPath())
		}
		section := reference[start+len(heading):]
		if end := bytes.Index(section, []byte("\n## `")); end >= 0 {
			section = section[:end]
		}
		if !bytes.Contains(section, []byte("-h, --help")) {
			t.Errorf("generated reference for %q lacks Cobra help flag", command.CommandPath())
		}
	}
}

func TestGeneratedFilesMatchRepository(t *testing.T) {
	repository := filepath.Join("..", "..")
	generated, err := Generate(cli.NewRoot())
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range generated {
		got, err := os.ReadFile(filepath.Join(repository, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale; run go run ./cmd/camp-docs", name)
		}
	}
}

func TestGeneratedReferenceExcludesHiddenCommands(t *testing.T) {
	generated, err := Generate(cli.NewRoot())
	if err != nil {
		t.Fatal(err)
	}
	reference := generated["docs/generated/commands.md"]
	if bytes.Contains(reference, []byte("camp supervise")) {
		t.Fatal("hidden supervisor command was documented as shipped")
	}
	for _, command := range []string{"camp setup", "camp init", "camp list", "camp open", "camp sync", "camp close", "camp reopen", "camp recover", "camp status", "camp strike", "camp doctor", "camp completion"} {
		if !bytes.Contains(reference, []byte("`"+command)) {
			t.Errorf("command reference does not contain %q", command)
		}
	}
}

func TestGeneratedPresentationExamplesAreVersioned(t *testing.T) {
	generated, err := Generate(cli.NewRoot())
	if err != nil {
		t.Fatal(err)
	}
	examples := generated["docs/generated/presentation.md"]
	if !bytes.Contains(examples, []byte(`"schemaVersion": 1`)) || !bytes.Contains(examples, []byte("BLOCKED  pasta  pasta_missing")) {
		t.Fatal("generated presentation examples do not contain the versioned JSON and human doctor goldens")
	}
}

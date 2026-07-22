package docsgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/cli"
)

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
	for _, command := range []string{"camp setup", "camp init", "camp open", "camp sync", "camp close", "camp reopen", "camp recover", "camp doctor", "camp completion"} {
		if !bytes.Contains(reference, []byte("`"+command)) {
			t.Errorf("command reference does not contain %q", command)
		}
	}
}

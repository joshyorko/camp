package capsule

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSourceTreatsExplicitLocalRootAsAdoptionIntent(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "SecondBrain")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveSource(SourceRequest{Capsule: "brain", ExplicitPath: root, ConfiguredPath: "/does/not/matter", RemoteAvailable: true})
	if err != nil {
		t.Fatalf("ResolveSource() error = %v", err)
	}
	if resolved.Kind != SourceAdopted || resolved.Root != root {
		t.Fatalf("source = %#v", resolved)
	}
}

func TestResolveSourceRejectsUnexplainedOrSymlinkedRootsAndAmbiguity(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveSource(SourceRequest{Capsule: "brain", ExplicitPath: link}); !errors.Is(err, ErrUnsafeSource) {
		t.Fatalf("symlink error = %v, want ErrUnsafeSource", err)
	}
	if _, err := ResolveSource(SourceRequest{Capsule: "brain", ConfiguredPath: root, RemoteAvailable: true}); !errors.Is(err, ErrSourceConflict) {
		t.Fatalf("configured-local/remote error = %v, want ErrSourceConflict", err)
	}
	if _, err := ResolveSource(SourceRequest{Capsule: "brain"}); !errors.Is(err, ErrNoSource) || !strings.Contains(err.Error(), "camp init") {
		t.Fatalf("no-source error = %v, want exact camp init recovery", err)
	}
}

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestLocalTransportReturnsNoopOnlyForCanonicalLocalWorkspaceRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	transport := Local{}
	result, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{
		Provider: "docker", LocalProvider: true, StagingRoot: root, WorkspaceLocalFolder: root,
	})
	if err != nil {
		t.Fatalf("ReturnToStaging() error = %v", err)
	}
	if result.Mode != ports.MirrorLocalNoop || result.Root != root {
		t.Fatalf("result = %#v", result)
	}
	other := t.TempDir()
	if _, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "docker", LocalProvider: true, StagingRoot: root, WorkspaceLocalFolder: other}); !errors.Is(err, ErrNotLocalNoop) {
		t.Fatalf("mismatched root error = %v", err)
	}
}

func TestMapTargetUsesEffectiveWorkspaceRootAfterHydration(t *testing.T) {
	t.Parallel()
	mapped, err := MapTarget("/host/root", "/workspaces/custom", "MemoryD")
	if err != nil || mapped != "/workspaces/custom/MemoryD" {
		t.Fatalf("MapTarget() = %q, %v", mapped, err)
	}
	if _, err := MapTarget("/host/root", "/workspaces/custom", "../escape"); !errors.Is(err, ErrTargetEscape) {
		t.Fatalf("MapTarget(escape) error = %v", err)
	}
}

func TestDeterministicWorkspaceIDIsStableAndDevPodSafe(t *testing.T) {
	t.Parallel()
	first := DeterministicID("Second Brain", "main", "/tmp/root")
	second := DeterministicID("Second Brain", "main", "/tmp/root")
	other := DeterministicID("Second Brain", "branch", "/tmp/root")
	if first != second || first == other || len(first) > 48 {
		t.Fatalf("ids = %q %q %q", first, second, other)
	}
}

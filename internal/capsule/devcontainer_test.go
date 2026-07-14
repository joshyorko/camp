package capsule

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestDevcontainerPreservesExistingRootConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".devcontainer", "devcontainer.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"name":"existing","image":"example.test/existing:1"}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveDevcontainer(root, "", domain.CapsuleLock{})
	if err != nil {
		t.Fatalf("ResolveDevcontainer() error = %v", err)
	}
	if resolved.Path != path || resolved.Generated {
		t.Fatalf("resolved = %#v", resolved)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(body) {
		t.Fatal("existing devcontainer was overwritten")
	}
}

func TestDevcontainerGeneratesDigestLockedFallbackAndIgnoresNestedConfig(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	nested := filepath.Join(root, "MemoryD", ".devcontainer", "devcontainer.json")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte(`{"image":"nested"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := domain.CapsuleLock{Room: domain.RoomLock{Image: "ghcr.io/example/room:wolfi", Digest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	resolved, err := ResolveDevcontainer(root, "", lock)
	if err != nil {
		t.Fatalf("ResolveDevcontainer() error = %v", err)
	}
	want := filepath.Join(root, ".camp", "runtime", "devcontainer.json")
	if resolved.Path != want || !resolved.Generated {
		t.Fatalf("resolved = %#v", resolved)
	}
	body, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatal(err)
	}
	if document["image"] != lock.Room.Image+"@"+lock.Room.Digest {
		t.Fatalf("fallback image = %#v", document["image"])
	}
}

func TestDevcontainerRejectsInvalidOrEscapingExplicitConfiguration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, ".devcontainer.json")
	if err := os.WriteFile(path, []byte(`{"image":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDevcontainer(root, "", domain.CapsuleLock{}); !errors.Is(err, ErrInvalidDevcontainer) {
		t.Fatalf("invalid root config error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "devcontainer.json")
	if err := os.WriteFile(outside, []byte(`{"image":"outside"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveDevcontainer(root, outside, domain.CapsuleLock{}); !errors.Is(err, ErrInvalidDevcontainer) {
		t.Fatalf("escaping explicit config error = %v", err)
	}
}

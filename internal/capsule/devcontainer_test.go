package capsule

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	compatibilityPath := filepath.Join(root, ".camp", "runtime", "iptables-compat")
	info, err := os.Stat(compatibilityPath)
	if err != nil {
		t.Fatalf("stat fallback iptables compatibility wrapper: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("fallback iptables compatibility wrapper mode = %o, want 755", info.Mode().Perm())
	}
	compatibilityBody, err := os.ReadFile(compatibilityPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"iptables-legacy -t nat -L", "iptables-nft", "ip6tables-legacy", "ip6tables-nft"} {
		if !strings.Contains(string(compatibilityBody), required) {
			t.Fatalf("fallback iptables compatibility wrapper lacks %q:\n%s", required, compatibilityBody)
		}
	}
	mounts, ok := document["mounts"].([]any)
	if !ok {
		t.Fatalf("fallback mounts = %#v", document["mounts"])
	}
	for _, executable := range []string{"iptables", "iptables-save", "iptables-restore", "ip6tables", "ip6tables-save", "ip6tables-restore"} {
		want := "source=${localWorkspaceFolder}/.camp/runtime/iptables-compat,target=/usr/local/sbin/" + executable + ",type=bind,readonly"
		found := false
		for _, mount := range mounts {
			if mount == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("fallback mounts lack %q: %#v", want, mounts)
		}
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

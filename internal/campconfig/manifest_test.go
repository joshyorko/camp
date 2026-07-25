package campconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateReadAndDiscoverManifest(t *testing.T) {
	root := t.TempDir()
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	want := Manifest{
		SchemaVersion: 1,
		ID:            "alpha",
		Source:        ".",
		Backend:       backend,
		Workspace:     Workspace{Provider: "docker", Context: "default"},
	}
	path, err := Create(root, want)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %o, want 600", info.Mode().Perm())
	}
	nested := filepath.Join(root, "one", "two")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(nested)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.Root != canonical || got.Path != path || got.Manifest != want {
		t.Fatalf("discovery = %#v, want root %q path %q manifest %#v", got, canonical, path, want)
	}
	if _, err := Create(root, want); err != nil {
		t.Fatalf("identical create should be idempotent: %v", err)
	}
}

func TestManifestFailsClosed(t *testing.T) {
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	valid := "schemaVersion: 1\nid: alpha\nsource: .\nbackend: " + backend + "\nworkspace:\n  provider: docker\n  context: default\n"
	tests := []struct {
		name string
		body string
	}{
		{name: "duplicate key", body: valid + "id: beta\n"},
		{name: "unknown field", body: valid + "secret: nope\n"},
		{name: "unsupported schema", body: "schemaVersion: 2\nid: alpha\nsource: .\nbackend: " + backend + "\nworkspace:\n  provider: docker\n  context: default\n"},
		{name: "credential backend", body: "schemaVersion: 1\nid: alpha\nsource: .\nbackend: https://user:secret@example.test\nworkspace:\n  provider: docker\n  context: default\n"},
		{name: "escaped source", body: "schemaVersion: 1\nid: alpha\nsource: ..\nbackend: " + backend + "\nworkspace:\n  provider: docker\n  context: default\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".camp"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, ".camp", "camp.yaml"), []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Read(filepath.Join(root, ".camp", "camp.yaml")); err == nil {
				t.Fatal("unsafe manifest was accepted")
			}
		})
	}
}

func TestManifestRejectsSymlinkComponentsAndConflicts(t *testing.T) {
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	manifest := Manifest{SchemaVersion: 1, ID: "alpha", Source: ".", Backend: backend, Workspace: Workspace{Provider: "docker", Context: "default"}}

	root := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".camp")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(root, manifest); err == nil {
		t.Fatal("symlink .camp directory was accepted")
	}

	root = t.TempDir()
	if _, err := Create(root, manifest); err != nil {
		t.Fatal(err)
	}
	conflict := manifest
	conflict.ID = "beta"
	if _, err := Create(root, conflict); !errors.Is(err, ErrManifestConflict) {
		t.Fatalf("conflict error = %v, want ErrManifestConflict", err)
	}

	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	targetFile := filepath.Join(t.TempDir(), "camp.yaml")
	if err := os.WriteFile(targetFile, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetFile, filepath.Join(root, ".camp", "camp.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(filepath.Join(root, ".camp", "camp.yaml")); err == nil {
		t.Fatal("symlink manifest was accepted")
	}
}

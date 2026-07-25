package campconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateLegacySingletonCreatesManifestBackupAndMachineDefaults(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".camp", "capsule.yaml"), []byte("schemaVersion: 1\nid: alpha\ndefaultBranch: main\ncreatedAt: 2026-07-25T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "camp", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := "defaultCapsule: alpha\nsource: " + root + "\nbackend: file://" + filepath.Join(t.TempDir(), "backend") + "\ndevpodProvider: docker\ndevpodContext: default\nregistryPort: 5001\n"
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Migrate(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Manifest.ID != "alpha" || result.Manifest.Root != root {
		t.Fatalf("migration result = %#v", result)
	}
	backup, err := os.ReadFile(configPath + ".bak")
	if err != nil || string(backup) != legacy {
		t.Fatalf("backup = %q, %v", backup, err)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "defaultCapsule") || strings.Contains(string(body), "source:") {
		t.Fatalf("rewritten config retains singleton selection:\n%s", body)
	}
	if _, err := Migrate(configPath); err != nil {
		t.Fatalf("idempotent migration failed: %v", err)
	}
}

func TestMigrateLegacySingletonRequiresMatchingCapsuleMetadata(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata string
	}{
		{name: "missing"},
		{name: "different identity", metadata: "schemaVersion: 1\nid: beta\ndefaultBranch: main\ncreatedAt: 2026-07-25T00:00:00Z\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.metadata != "" {
				if err := os.Mkdir(filepath.Join(root, ".camp"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, ".camp", "capsule.yaml"), []byte(test.metadata), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			legacy := "defaultCapsule: alpha\nsource: " + root + "\nbackend: file://" + filepath.Join(t.TempDir(), "backend") + "\ndevpodProvider: docker\ndevpodContext: default\n"
			if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Migrate(configPath); !errors.Is(err, ErrLegacyMetadata) {
				t.Fatalf("migration error = %v, want ErrLegacyMetadata", err)
			}
			body, _ := os.ReadFile(configPath)
			if string(body) != legacy {
				t.Fatalf("unsafe migration rewrote config:\n%s", body)
			}
		})
	}
}

func TestMigrateLegacySingletonPreservesConflict(t *testing.T) {
	root := t.TempDir()
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	if _, err := Create(root, Manifest{SchemaVersion: 1, ID: "beta", Source: ".", Backend: backend, Workspace: Workspace{Provider: "docker", Context: "default"}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".camp", "capsule.yaml"), []byte("schemaVersion: 1\nid: alpha\ndefaultBranch: main\ncreatedAt: 2026-07-25T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	legacy := "defaultCapsule: alpha\nsource: " + root + "\nbackend: " + backend + "\ndevpodProvider: docker\ndevpodContext: default\n"
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(configPath); !errors.Is(err, ErrManifestConflict) {
		t.Fatalf("migration error = %v, want manifest conflict", err)
	}
	body, _ := os.ReadFile(configPath)
	if string(body) != legacy {
		t.Fatalf("conflict rewrote config:\n%s", body)
	}
}

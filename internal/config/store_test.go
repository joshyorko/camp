package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreUpdatePersistsOnlyAllowedNonSecretFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "camp", "config.yaml")
	store := NewStore(path)
	want := Persistent{DefaultCapsule: "brain", Backend: "file:///srv/camp", Source: "/srv/brain", DevPodProvider: "docker", RegistryPort: 5001, FileserverPort: 8081}
	if err := store.Update(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(body)), "token") || strings.Contains(strings.ToLower(string(body)), "credential") {
		t.Fatalf("persisted secret-shaped field: %s", body)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, error = %v", info.Mode().Perm(), err)
	}
}

func TestStoreRejectsCredentialBearingValuesBeforeReplacingConfig(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := NewStore(path)
	baseline := Persistent{DefaultCapsule: "brain", Backend: "file:///srv/camp"}
	if err := store.Update(baseline); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Persistent{
		{DefaultCapsule: "brain", Backend: "https://user:secret@example.test/camp"},
		{DefaultCapsule: "brain", Source: "https://example.test/camp?access_token=secret"},
	} {
		if err := store.Update(candidate); !errors.Is(err, ErrCredentialPersistence) {
			t.Fatalf("Update(%#v) error = %v, want ErrCredentialPersistence", candidate, err)
		}
		got, err := store.Read()
		if err != nil || got != baseline {
			t.Fatalf("config changed after rejected update: %#v, %v", got, err)
		}
	}
}

package profilestore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/domain"
)

func TestStorePersistsProfilesAndActiveSelectionPrivately(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "camp", "profiles.json")
	store := New(path)
	profile := mustProfile(t, "local")

	if err := store.Import(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	if err := store.Activate(context.Background(), profile.Digest); err != nil {
		t.Fatal(err)
	}

	reopened := New(path)
	got, err := reopened.Get(context.Background(), profile.Digest)
	if err != nil {
		t.Fatal(err)
	}
	current, err := reopened.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != profile || current != profile.Digest {
		t.Fatalf("Get/Current = %#v, %q; want %#v, %q", got, current, profile, profile.Digest)
	}
	for _, candidate := range []string{path, path + ".lock"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode(%q) = %04o, want 0600", candidate, got)
		}
	}
}

func TestStoreListsProfilesDeterministicallyAndDeactivates(t *testing.T) {
	t.Parallel()
	store := New(filepath.Join(t.TempDir(), "profiles.json"))
	second := mustProfile(t, "zebra")
	first := mustProfile(t, "alpha")
	for _, profile := range []app.Profile{second, first, first} {
		if err := store.Import(context.Background(), profile); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Name != "alpha" || listed[1].Name != "zebra" {
		t.Fatalf("List() = %#v, want alpha then zebra without duplicate", listed)
	}
	if err := store.Activate(context.Background(), first.Digest); err != nil {
		t.Fatal(err)
	}
	if err := store.Deactivate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if current, err := store.Current(context.Background()); err != nil || current != "" {
		t.Fatalf("Current() = %q, %v; want empty", current, err)
	}
}

func TestStoreRejectsMissingActivationAndInvalidOrSecretShapedState(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "profiles.json")
	store := New(path)
	if err := store.Activate(context.Background(), strings.Repeat("a", 64)); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("Activate() error = %v, want ErrProfileNotFound", err)
	}

	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"profiles":[],"current":"","token":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background()); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("List() error = %v, want ErrInvalidStore", err)
	}
}

func TestStoreFailedAtomicPublicationPreservesPriorSelection(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "profiles.json")
	profile := mustProfile(t, "local")
	store := New(path)
	if err := store.Import(context.Background(), profile); err != nil {
		t.Fatal(err)
	}
	publicationErr := errors.New("stop before rename")
	failing := New(path, WithBeforeRename(func() error { return publicationErr }))
	if err := failing.Activate(context.Background(), profile.Digest); !errors.Is(err, publicationErr) {
		t.Fatalf("Activate() error = %v, want publication failure", err)
	}
	if current, err := New(path).Current(context.Background()); err != nil || current != "" {
		t.Fatalf("Current() after failed publication = %q, %v; want prior empty selection", current, err)
	}
}

func mustProfile(t *testing.T, name string) app.Profile {
	t.Helper()
	profile, err := app.NewProfile(app.ProfileInput{
		Name: name,
		Values: app.ProfileValues{
			WorkspaceEngine: domain.WorkspaceEngineDevPod,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

package app

import (
	"context"
	"errors"
	"testing"
)

func TestProfilesImportStoresCanonicalImmutableProfileAndActivationUsesDigest(t *testing.T) {
	t.Parallel()
	store := &profileStoreStub{}
	profiles := NewProfiles(store)
	profile, err := profiles.Import(context.Background(), ProfileInput{Name: "local", Values: map[string]string{"workspaceEngine": "devpod"}})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Digest == "" || len(store.imported) != 1 {
		t.Fatalf("profile=%#v store=%#v", profile, store)
	}
	if err := profiles.Activate(context.Background(), profile.Digest); err != nil {
		t.Fatal(err)
	}
	if store.active != profile.Digest {
		t.Fatalf("active=%q", store.active)
	}
}

func TestProfilesRejectSecretAndPathBearingImportsBeforeStoreEffects(t *testing.T) {
	t.Parallel()
	store := &profileStoreStub{}
	profiles := NewProfiles(store)
	for _, input := range []ProfileInput{{Name: "secret", Values: map[string]string{"token": "value"}}, {Name: "path", Values: map[string]string{"workspace": "/home/josh/brain"}}} {
		if _, err := profiles.Import(context.Background(), input); !errors.Is(err, ErrUnsafeProfile) {
			t.Fatalf("Import(%#v) = %v", input, err)
		}
	}
	if len(store.imported) != 0 {
		t.Fatalf("unsafe imports reached store: %#v", store.imported)
	}
}

func TestProfilesImportCopiesValuesBeforePersistingImmutableProfile(t *testing.T) {
	t.Parallel()
	store := &profileStoreStub{}
	values := map[string]string{"workspaceEngine": "devpod"}
	profile, err := NewProfiles(store).Import(context.Background(), ProfileInput{Name: "local", Values: values})
	if err != nil {
		t.Fatal(err)
	}
	values["workspaceEngine"] = "mutated"
	if profile.Values["workspaceEngine"] != "devpod" || store.imported[0].Values["workspaceEngine"] != "devpod" {
		t.Fatalf("profile values were mutable: %#v", store.imported[0])
	}
}

type profileStoreStub struct {
	imported []Profile
	active   string
}

func (s *profileStoreStub) Import(_ context.Context, profile Profile) error {
	s.imported = append(s.imported, profile)
	return nil
}
func (s *profileStoreStub) List(context.Context) ([]Profile, error) { return s.imported, nil }
func (s *profileStoreStub) Get(_ context.Context, digest string) (Profile, error) {
	for _, profile := range s.imported {
		if profile.Digest == digest {
			return profile, nil
		}
	}
	return Profile{}, errors.New("missing")
}
func (s *profileStoreStub) Current(context.Context) (string, error) { return s.active, nil }
func (s *profileStoreStub) Activate(_ context.Context, digest string) error {
	s.active = digest
	return nil
}
func (s *profileStoreStub) Deactivate(context.Context) error { s.active = ""; return nil }

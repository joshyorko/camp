package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestProfilesImportStoresClosedCanonicalProfileAndActivationUsesDigest(t *testing.T) {
	t.Parallel()
	store := &profileStoreStub{}
	profile, err := NewProfiles(store).Import(context.Background(), ProfileInput{
		Name: "local",
		Values: ProfileValues{
			WorkspaceEngine: domain.WorkspaceEngineDevPod,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Digest == "" || len(store.imported) != 1 {
		t.Fatalf("profile=%#v store=%#v", profile, store)
	}
	if err := NewProfiles(store).Activate(context.Background(), profile.Digest); err != nil {
		t.Fatal(err)
	}
	if store.active != profile.Digest {
		t.Fatalf("active=%q", store.active)
	}
}

func TestProfilesRejectMalformedInputsBeforeStoreEffects(t *testing.T) {
	t.Parallel()
	tests := []ProfileInput{
		{},
		{Name: "access-token", Values: ProfileValues{WorkspaceEngine: domain.WorkspaceEngineDevPod}},
		{Name: "local", Values: ProfileValues{}},
		{Name: "local", Values: ProfileValues{WorkspaceEngine: "docker"}},
	}
	for _, input := range tests {
		store := &profileStoreStub{}
		if _, err := NewProfiles(store).Import(context.Background(), input); !errors.Is(err, ErrInvalidProfile) {
			t.Fatalf("Import(%#v) = %v, want ErrInvalidProfile", input, err)
		}
		if len(store.imported) != 0 {
			t.Fatalf("invalid import reached store: %#v", store.imported)
		}
	}
}

func TestProfilesValidateAndCopyStoreResults(t *testing.T) {
	t.Parallel()
	valid, err := NewProfile(ProfileInput{Name: "local", Values: ProfileValues{WorkspaceEngine: domain.WorkspaceEngineDevPod}})
	if err != nil {
		t.Fatal(err)
	}
	store := &profileStoreStub{imported: []Profile{valid}}
	profiles := NewProfiles(store)

	listed, err := profiles.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	listed[0].Name = "mutated"
	shown, err := profiles.Show(context.Background(), valid.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if shown.Name != "local" || store.imported[0].Name != "local" {
		t.Fatalf("store result was aliased: shown=%#v store=%#v", shown, store.imported[0])
	}

	store.imported = append(store.imported, Profile{})
	if _, err := profiles.List(context.Background()); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("List() error = %v, want ErrInvalidProfile", err)
	}
	store.imported = []Profile{{Digest: valid.Digest}}
	if _, err := profiles.Show(context.Background(), valid.Digest); !errors.Is(err, ErrInvalidProfile) {
		t.Fatalf("Show() error = %v, want ErrInvalidProfile", err)
	}
}

func TestProfileDecoderRejectsUnknownFieldsAndDigestMismatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		body    string
		message string
	}{
		{
			name:    "wrong digest",
			body:    `{"schemaVersion":1,"name":"local","values":{"workspaceEngine":"devpod"},"digest":"` + strings.Repeat("0", 64) + `"}`,
			message: "digest does not match canonical profile",
		},
		{
			name:    "nested unknown field",
			body:    `{"schemaVersion":1,"name":"local","values":{"workspaceEngine":"devpod","token":"secret"},"digest":"` + strings.Repeat("0", 64) + `"}`,
			message: `unknown field "token"`,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeProfile([]byte(tt.body))
			if !errors.Is(err, ErrInvalidProfile) || !strings.Contains(err.Error(), tt.message) {
				t.Fatalf("DecodeProfile() error = %v, want ErrInvalidProfile containing %q", err, tt.message)
			}
		})
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
func (s *profileStoreStub) List(context.Context) ([]Profile, error) {
	out := make([]Profile, len(s.imported))
	copy(out, s.imported)
	return out, nil
}
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

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrInvalidProfile = errors.New("invalid profile")

const ProfileSchemaVersion = 1

var (
	profileNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	profileDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ProfileValues is intentionally closed. New portable, non-secret settings
// require an explicit schema revision rather than being admitted through an
// arbitrary key/value map.
type ProfileValues struct {
	WorkspaceEngine string `json:"workspaceEngine"`
}

type ProfileInput struct {
	Name   string
	Values ProfileValues
}

type Profile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Name          string        `json:"name"`
	Values        ProfileValues `json:"values"`
	Digest        string        `json:"digest"`
}

type ProfileStore interface {
	Import(context.Context, Profile) error
	List(context.Context) ([]Profile, error)
	Get(context.Context, string) (Profile, error)
	Current(context.Context) (string, error)
	Activate(context.Context, string) error
	Deactivate(context.Context) error
}

type Profiles struct{ store ProfileStore }

func NewProfiles(store ProfileStore) *Profiles { return &Profiles{store: store} }

func NewProfile(input ProfileInput) (Profile, error) {
	if !validProfileName(input.Name) {
		return Profile{}, fmt.Errorf("%w: name is not a portable identifier", ErrInvalidProfile)
	}
	if input.Values.WorkspaceEngine != domain.WorkspaceEngineDevPod {
		return Profile{}, fmt.Errorf("%w: unsupported workspace engine %q", ErrInvalidProfile, input.Values.WorkspaceEngine)
	}
	canonical := struct {
		SchemaVersion int           `json:"schemaVersion"`
		Name          string        `json:"name"`
		Values        ProfileValues `json:"values"`
	}{ProfileSchemaVersion, input.Name, input.Values}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return Profile{}, err
	}
	digest := sha256.Sum256(encoded)
	return Profile{
		SchemaVersion: ProfileSchemaVersion,
		Name:          input.Name,
		Values:        input.Values,
		Digest:        hex.EncodeToString(digest[:]),
	}, nil
}

func (p Profile) Validate() error {
	if p.SchemaVersion != ProfileSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidProfile, p.SchemaVersion)
	}
	expected, err := NewProfile(ProfileInput{Name: p.Name, Values: p.Values})
	if err != nil {
		return err
	}
	if !profileDigestPattern.MatchString(p.Digest) || p.Digest != expected.Digest {
		return fmt.Errorf("%w: digest does not match canonical profile", ErrInvalidProfile)
	}
	return nil
}

func DecodeProfile(encoded []byte) (Profile, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Profile{}, fmt.Errorf("%w: %v", ErrInvalidProfile, err)
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func (p *Profiles) Import(ctx context.Context, input ProfileInput) (Profile, error) {
	if p == nil || p.store == nil {
		return Profile{}, errors.New("profile store is nil")
	}
	profile, err := NewProfile(input)
	if err != nil {
		return Profile{}, err
	}
	if err := p.store.Import(ctx, cloneProfile(profile)); err != nil {
		return Profile{}, err
	}
	return cloneProfile(profile), nil
}

func (p *Profiles) List(ctx context.Context) ([]Profile, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("profile store is nil")
	}
	stored, err := p.store.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, len(stored))
	for index, profile := range stored {
		if err := profile.Validate(); err != nil {
			return nil, err
		}
		out[index] = cloneProfile(profile)
	}
	return out, nil
}

func (p *Profiles) Show(ctx context.Context, digest string) (Profile, error) {
	if p == nil || p.store == nil {
		return Profile{}, errors.New("profile store is nil")
	}
	if !profileDigestPattern.MatchString(digest) {
		return Profile{}, fmt.Errorf("%w: digest must be 64 lowercase hexadecimal characters", ErrInvalidProfile)
	}
	profile, err := p.store.Get(ctx, digest)
	if err != nil {
		return Profile{}, err
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, err
	}
	return cloneProfile(profile), nil
}

func (p *Profiles) Current(ctx context.Context) (string, error) {
	if p == nil || p.store == nil {
		return "", errors.New("profile store is nil")
	}
	digest, err := p.store.Current(ctx)
	if err != nil || digest == "" {
		return digest, err
	}
	if !profileDigestPattern.MatchString(digest) {
		return "", fmt.Errorf("%w: current digest must be 64 lowercase hexadecimal characters", ErrInvalidProfile)
	}
	return digest, nil
}

func (p *Profiles) Activate(ctx context.Context, digest string) error {
	if p == nil || p.store == nil {
		return errors.New("profile store is nil")
	}
	if _, err := p.Show(ctx, digest); err != nil {
		return err
	}
	return p.store.Activate(ctx, digest)
}

func (p *Profiles) Deactivate(ctx context.Context) error {
	if p == nil || p.store == nil {
		return errors.New("profile store is nil")
	}
	return p.store.Deactivate(ctx)
}

func cloneProfile(profile Profile) Profile {
	// Profile is a closed value graph today. Keeping the copy at each boundary
	// makes that ownership rule explicit if a future schema adds reference data.
	return profile
}

func validProfileName(value string) bool {
	if !profileNamePattern.MatchString(value) {
		return false
	}
	for _, part := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	}) {
		switch part {
		case "secret", "token", "password", "credential", "port", "timestamp", "session":
			return false
		}
	}
	return true
}

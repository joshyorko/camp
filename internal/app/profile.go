package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

var ErrUnsafeProfile = errors.New("profile contains a secret or host-specific value")

const ProfileSchemaVersion = 1

type ProfileInput struct {
	Name   string
	Values map[string]string
}
type Profile struct {
	SchemaVersion int               `json:"schemaVersion"`
	Name          string            `json:"name"`
	Values        map[string]string `json:"values"`
	Digest        string            `json:"digest"`
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
func (p *Profiles) Import(ctx context.Context, input ProfileInput) (Profile, error) {
	if p == nil || p.store == nil {
		return Profile{}, errors.New("profile store is nil")
	}
	if unsafeProfile(input) {
		return Profile{}, ErrUnsafeProfile
	}
	values := cloneProfileValues(input.Values)
	canonical := struct {
		SchemaVersion int               `json:"schemaVersion"`
		Name          string            `json:"name"`
		Values        map[string]string `json:"values"`
	}{ProfileSchemaVersion, input.Name, values}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return Profile{}, err
	}
	digest := sha256.Sum256(encoded)
	profile := Profile{SchemaVersion: ProfileSchemaVersion, Name: input.Name, Values: values, Digest: hex.EncodeToString(digest[:])}
	if err := p.store.Import(ctx, profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func cloneProfileValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
func (p *Profiles) List(ctx context.Context) ([]Profile, error) {
	if p == nil || p.store == nil {
		return nil, errors.New("profile store is nil")
	}
	return p.store.List(ctx)
}
func (p *Profiles) Show(ctx context.Context, digest string) (Profile, error) {
	if p == nil || p.store == nil {
		return Profile{}, errors.New("profile store is nil")
	}
	return p.store.Get(ctx, digest)
}
func (p *Profiles) Current(ctx context.Context) (string, error) {
	if p == nil || p.store == nil {
		return "", errors.New("profile store is nil")
	}
	return p.store.Current(ctx)
}
func (p *Profiles) Activate(ctx context.Context, digest string) error {
	if p == nil || p.store == nil {
		return errors.New("profile store is nil")
	}
	if _, err := p.store.Get(ctx, digest); err != nil {
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
func unsafeProfile(input ProfileInput) bool {
	if input.Name == "" {
		return true
	}
	for key, value := range input.Values {
		if strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "token") || strings.Contains(strings.ToLower(key), "password") || strings.HasPrefix(value, "/") || strings.Contains(value, "://") {
			return true
		}
	}
	return false
}

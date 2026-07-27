package profilestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/joshyorko/camp/internal/app"
)

const storeSchemaVersion = 1

var (
	ErrInvalidStore    = errors.New("invalid profile store")
	ErrProfileNotFound = errors.New("profile not found")
)

type Option func(*Store)

func WithBeforeRename(hook func() error) Option {
	return func(store *Store) { store.beforeRename = hook }
}

type Store struct {
	path         string
	beforeRename func() error
}

var _ app.ProfileStore = (*Store)(nil)

type state struct {
	SchemaVersion int           `json:"schemaVersion"`
	Profiles      []app.Profile `json:"profiles"`
	Current       string        `json:"current,omitempty"`
}

func New(path string, options ...Option) *Store {
	store := &Store{path: path}
	for _, option := range options {
		option(store)
	}
	return store
}

func (s *Store) Import(ctx context.Context, profile app.Profile) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	return s.modify(ctx, func(current *state) error {
		for _, existing := range current.Profiles {
			if existing.Digest == profile.Digest {
				if existing != profile {
					return fmt.Errorf("%w: digest collision", ErrInvalidStore)
				}
				return nil
			}
		}
		current.Profiles = append(current.Profiles, profile)
		return nil
	})
}

func (s *Store) List(ctx context.Context) ([]app.Profile, error) {
	current, err := s.read(ctx)
	if err != nil {
		return nil, err
	}
	result := append([]app.Profile(nil), current.Profiles...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Name == result[j].Name {
			return result[i].Digest < result[j].Digest
		}
		return result[i].Name < result[j].Name
	})
	return result, nil
}

func (s *Store) Get(ctx context.Context, digest string) (app.Profile, error) {
	current, err := s.read(ctx)
	if err != nil {
		return app.Profile{}, err
	}
	for _, profile := range current.Profiles {
		if profile.Digest == digest {
			return profile, nil
		}
	}
	return app.Profile{}, ErrProfileNotFound
}

func (s *Store) Current(ctx context.Context) (string, error) {
	current, err := s.read(ctx)
	return current.Current, err
}

func (s *Store) Activate(ctx context.Context, digest string) error {
	return s.modify(ctx, func(current *state) error {
		for _, profile := range current.Profiles {
			if profile.Digest == digest {
				current.Current = digest
				return nil
			}
		}
		return ErrProfileNotFound
	})
}

func (s *Store) Deactivate(ctx context.Context) error {
	return s.modify(ctx, func(current *state) error {
		current.Current = ""
		return nil
	})
}

func (s *Store) read(ctx context.Context) (state, error) {
	if err := ctx.Err(); err != nil {
		return state{}, err
	}
	body, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state{SchemaVersion: storeSchemaVersion, Profiles: []app.Profile{}}, nil
	}
	if err != nil {
		return state{}, err
	}
	return decode(body)
}

func (s *Store) modify(ctx context.Context, change func(*state) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.path == "" {
		return fmt.Errorf("%w: path is empty", ErrInvalidStore)
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := s.read(ctx)
	if err != nil {
		return err
	}
	if err := change(&current); err != nil {
		return err
	}
	if err := validate(current); err != nil {
		return err
	}
	return s.write(current)
}

func (s *Store) write(current state) error {
	body, err := json.Marshal(current)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".profiles.json-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if s.beforeRename != nil {
		if err := s.beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return err
	}
	parent, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}

func decode(body []byte) (state, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var current state
	if err := decoder.Decode(&current); err != nil {
		return state{}, fmt.Errorf("%w: %v", ErrInvalidStore, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return state{}, fmt.Errorf("%w: %v", ErrInvalidStore, err)
	}
	if err := validate(current); err != nil {
		return state{}, err
	}
	return current, nil
}

func validate(current state) error {
	if current.SchemaVersion != storeSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidStore, current.SchemaVersion)
	}
	seen := make(map[string]struct{}, len(current.Profiles))
	activeFound := current.Current == ""
	for _, profile := range current.Profiles {
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidStore, err)
		}
		if _, exists := seen[profile.Digest]; exists {
			return fmt.Errorf("%w: duplicate profile digest", ErrInvalidStore)
		}
		seen[profile.Digest] = struct{}{}
		activeFound = activeFound || profile.Digest == current.Current
	}
	if !activeFound {
		return fmt.Errorf("%w: current profile does not exist", ErrInvalidStore)
	}
	return nil
}

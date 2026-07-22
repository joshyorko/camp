package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

var ErrCredentialPersistence = errors.New("credentials cannot be persisted in Camp configuration")

type Persistent struct {
	DefaultCapsule string `yaml:"defaultCapsule,omitempty" json:"defaultCapsule,omitempty"`
	Backend        string `yaml:"backend,omitempty" json:"backend,omitempty"`
	Source         string `yaml:"source,omitempty" json:"source,omitempty"`
	DevPodProvider string `yaml:"devpodProvider,omitempty" json:"devpodProvider,omitempty"`
	RegistryPort   int    `yaml:"registryPort,omitempty" json:"registryPort,omitempty"`
	FileserverPort int    `yaml:"fileserverPort,omitempty" json:"fileserverPort,omitempty"`
}

type Store struct{ path string }

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Read() (Persistent, error) {
	body, err := os.ReadFile(s.path)
	if err != nil {
		return Persistent{}, err
	}
	var value Persistent
	decoder := yaml.NewDecoder(strings.NewReader(string(body)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&value); err != nil {
		return Persistent{}, fmt.Errorf("decode Camp configuration: %w", err)
	}
	return value, nil
}

func (s *Store) Update(value Persistent) error {
	if err := ValidatePersistent(value); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	guard, err := os.OpenFile(s.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open Camp configuration lock: %w", err)
	}
	defer guard.Close()
	if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock Camp configuration: %w", err)
	}
	defer syscall.Flock(int(guard.Fd()), syscall.LOCK_UN)
	body, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	directory := filepath.Dir(s.path)
	temporary, err := os.CreateTemp(directory, ".config.yaml-*")
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

func ValidatePersistent(value Persistent) error {
	if err := ValidateDevPodProvider(value.DevPodProvider); err != nil {
		return err
	}
	for _, candidate := range []string{value.Backend, value.Source} {
		parsed, err := url.Parse(candidate)
		if err != nil {
			return err
		}
		if parsed.User != nil {
			return ErrCredentialPersistence
		}
		for key := range parsed.Query() {
			if sensitiveKey(key) {
				return ErrCredentialPersistence
			}
		}
	}
	if value.RegistryPort != 0 {
		if err := validatePort(value.RegistryPort); err != nil {
			return err
		}
	}
	if value.FileserverPort != 0 {
		if err := validatePort(value.FileserverPort); err != nil {
			return err
		}
	}
	return nil
}

func ValidateDevPodProvider(provider string) error {
	if provider == "" {
		return nil
	}
	if strings.TrimSpace(provider) != provider || provider == "." || provider == ".." || strings.ContainsAny(provider, "/\\\t\r\n ") {
		return errors.New("DevPod provider is invalid")
	}
	return nil
}

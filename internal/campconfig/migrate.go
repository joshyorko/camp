package campconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
	"gopkg.in/yaml.v3"
)

const MigrationCommand = "camp init --migrate"

var ErrLegacyMetadata = errors.New("legacy capsule metadata is missing or does not match")

type MigrationResult struct {
	Manifest Resolved          `json:"manifest"`
	Defaults config.Persistent `json:"defaults"`
	Backup   string            `json:"backup,omitempty"`
	Migrated bool              `json:"migrated"`
}

func Migrate(configPath string) (MigrationResult, error) {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return MigrationResult{}, err
	}
	legacy, err := config.NewStore(configPath).Read()
	if err != nil {
		return MigrationResult{}, err
	}
	defaults := legacy
	defaults.DefaultCapsule = ""
	defaults.Source = ""
	if legacy.DefaultCapsule == "" && legacy.Source == "" {
		return MigrationResult{Defaults: defaults}, nil
	}
	if legacy.DefaultCapsule == "" || legacy.Source == "" {
		return MigrationResult{}, fmt.Errorf("legacy Camp configuration is incomplete; next: %s", MigrationCommand)
	}
	if err := validateLegacyMetadata(legacy.Source, legacy.DefaultCapsule); err != nil {
		return MigrationResult{}, err
	}
	provider := legacy.DevPodProvider
	if provider == "" {
		provider = "docker"
	}
	contextName := legacy.DevPodContext
	if contextName == "" {
		contextName = "default"
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion,
		ID:            legacy.DefaultCapsule,
		Source:        ".",
		Backend:       legacy.Backend,
		Workspace:     Workspace{Provider: provider, Context: contextName},
	}
	backup := configPath + ".bak"
	if err := createBackup(backup, body); err != nil {
		return MigrationResult{}, err
	}
	path, err := Create(legacy.Source, manifest)
	if err != nil {
		return MigrationResult{}, err
	}
	if err := config.NewStore(configPath).Update(defaults); err != nil {
		return MigrationResult{}, err
	}
	resolved, err := Read(path)
	if err != nil {
		return MigrationResult{}, err
	}
	return MigrationResult{Manifest: resolved, Defaults: defaults, Backup: backup, Migrated: true}, nil
}

func validateLegacyMetadata(root, id string) error {
	path := filepath.Join(root, ".camp", "capsule.yaml")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: %v; next: %s", ErrLegacyMetadata, err, MigrationCommand)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: capsule metadata is not a regular file; next: %s", ErrLegacyMetadata, MigrationCommand)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: %v; next: %s", ErrLegacyMetadata, err, MigrationCommand)
	}
	var metadata domain.CapsuleMetadata
	if err := yaml.Unmarshal(body, &metadata); err != nil ||
		metadata.SchemaVersion != domain.SchemaVersion ||
		metadata.ID != id ||
		metadata.DefaultBranch == "" ||
		metadata.CreatedAt.IsZero() {
		return fmt.Errorf("%w for %q; next: %s", ErrLegacyMetadata, id, MigrationCommand)
	}
	return nil
}

func createBackup(path string, body []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, body) {
			return nil
		}
		return errors.New("legacy Camp configuration backup already exists with different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

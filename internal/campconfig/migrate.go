package campconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshyorko/camp/internal/config"
)

const MigrationCommand = "camp init --migrate"

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

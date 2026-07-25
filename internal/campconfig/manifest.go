package campconfig

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshyorko/camp/internal/config"
	"gopkg.in/yaml.v3"
)

const SchemaVersion = 1

var (
	ErrManifestNotFound = errors.New("no .camp/camp.yaml found")
	ErrManifestConflict = errors.New("camp manifest conflicts with existing state")
)

type Workspace struct {
	Provider string `yaml:"provider" json:"provider"`
	Context  string `yaml:"context" json:"context"`
}

type Manifest struct {
	SchemaVersion int       `yaml:"schemaVersion" json:"schemaVersion"`
	ID            string    `yaml:"id" json:"id"`
	Source        string    `yaml:"source" json:"source"`
	Backend       string    `yaml:"backend" json:"backend"`
	Workspace     Workspace `yaml:"workspace" json:"workspace"`
}

type Resolved struct {
	Root     string   `json:"root"`
	Path     string   `json:"path"`
	Manifest Manifest `json:"manifest"`
}

func Discover(start string) (Resolved, error) {
	canonical, err := canonicalDirectory(start)
	if err != nil {
		return Resolved{}, err
	}
	for {
		path := filepath.Join(canonical, ".camp", "camp.yaml")
		resolved, err := Read(path)
		if err == nil {
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return Resolved{}, err
		}
		parent := filepath.Dir(canonical)
		if parent == canonical {
			return Resolved{}, fmt.Errorf("%w from %s; next: camp init", ErrManifestNotFound, start)
		}
		canonical = parent
	}
}

func Read(path string) (Resolved, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Resolved{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Resolved{}, errors.New("camp manifest is not a regular file")
	}
	campDir := filepath.Dir(path)
	dirInfo, err := os.Lstat(campDir)
	if err != nil {
		return Resolved{}, err
	}
	if !dirInfo.IsDir() || dirInfo.Mode()&os.ModeSymlink != 0 {
		return Resolved{}, errors.New(".camp is not a real directory")
	}
	root, err := canonicalDirectory(filepath.Dir(campDir))
	if err != nil {
		return Resolved{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return Resolved{}, err
	}
	manifest, err := decodeManifest(body, root)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Root: root, Path: filepath.Join(root, ".camp", "camp.yaml"), Manifest: manifest}, nil
}

func Create(root string, manifest Manifest) (string, error) {
	canonical, err := canonicalDirectory(root)
	if err != nil {
		return "", err
	}
	if err := validateManifest(manifest, canonical); err != nil {
		return "", err
	}
	campDir := filepath.Join(canonical, ".camp")
	if info, err := os.Lstat(campDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New(".camp is not a real directory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	} else if err := os.Mkdir(campDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(campDir, "camp.yaml")
	if existing, err := Read(path); err == nil {
		if existing.Manifest == manifest {
			return path, nil
		}
		return "", fmt.Errorf("%w at %s", ErrManifestConflict, path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	body, err := yaml.Marshal(manifest)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(campDir, ".camp.yaml-*")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", err
	}
	if _, err := temporary.Write(body); err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	parent, err := os.Open(campDir)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return "", err
	}
	return path, nil
}

func decodeManifest(body []byte, root string) (Manifest, error) {
	var node yaml.Node
	if err := yaml.Unmarshal(body, &node); err != nil {
		return Manifest{}, fmt.Errorf("decode camp manifest: %w", err)
	}
	if err := rejectDuplicateKeys(&node); err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode camp manifest: %w", err)
	}
	if err := validateManifest(manifest, root); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest Manifest, root string) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported camp manifest schema version %d", manifest.SchemaVersion)
	}
	if manifest.ID == "" || filepath.Base(manifest.ID) != manifest.ID || manifest.ID == "." || manifest.ID == ".." {
		return errors.New("invalid camp id")
	}
	if manifest.Source != "." {
		return errors.New("camp manifest source must be .")
	}
	source, err := filepath.EvalSymlinks(filepath.Join(root, manifest.Source))
	if err != nil {
		return fmt.Errorf("resolve camp source: %w", err)
	}
	if filepath.Clean(source) != filepath.Clean(root) {
		return errors.New("camp manifest source escapes its root")
	}
	if strings.TrimSpace(manifest.Backend) != manifest.Backend || manifest.Backend == "" {
		return errors.New("camp manifest backend is required")
	}
	parsed, err := url.Parse(manifest.Backend)
	if err != nil || parsed.User != nil {
		return config.ErrCredentialPersistence
	}
	for key := range parsed.Query() {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "key") {
			return config.ErrCredentialPersistence
		}
	}
	if parsed.Scheme != "file" && parsed.Scheme != "s3" {
		return errors.New("camp manifest backend must be file:/// or s3://")
	}
	if err := config.ValidateDevPodProvider(manifest.Workspace.Provider); err != nil {
		return err
	}
	if manifest.Workspace.Provider == "" {
		return errors.New("camp manifest workspace provider is required")
	}
	if err := config.ValidateDevPodContext(manifest.Workspace.Context); err != nil {
		return err
	}
	if manifest.Workspace.Context == "" {
		return errors.New("camp manifest workspace context is required")
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("camp root is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("camp root is not a directory")
	}
	return canonical, nil
}

func rejectDuplicateKeys(node *yaml.Node) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]struct{}{}
		for index := 0; index+1 < len(node.Content); index += 2 {
			key := node.Content[index].Value
			if _, ok := seen[key]; ok {
				return fmt.Errorf("camp manifest contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
		}
	}
	for _, child := range node.Content {
		if err := rejectDuplicateKeys(child); err != nil {
			return err
		}
	}
	return nil
}

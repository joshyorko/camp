package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type XDGInput struct {
	Home        string
	Environment map[string]string
}

type XDGPaths struct {
	ConfigPath  string `json:"configPath" yaml:"configPath"`
	DataRoot    string `json:"dataRoot" yaml:"dataRoot"`
	WorkRoot    string `json:"workRoot" yaml:"workRoot"`
	StoreRoot   string `json:"storeRoot" yaml:"storeRoot"`
	SessionRoot string `json:"sessionRoot" yaml:"sessionRoot"`
	CacheRoot   string `json:"cacheRoot" yaml:"cacheRoot"`
	RuntimeRoot string `json:"runtimeRoot" yaml:"runtimeRoot"`
}

type FileBackend struct {
	Root         string `json:"root" yaml:"root"`
	SanitizedURL string `json:"url" yaml:"url"`
	Fingerprint  string `json:"fingerprint" yaml:"fingerprint"`
}

func ResolveXDGPaths(input XDGInput) (XDGPaths, error) {
	home := strings.TrimSpace(input.Home)
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return XDGPaths{}, fmt.Errorf("resolve home directory: %w", err)
		}
	}
	if err := validateXDGRoot("home", home); err != nil {
		return XDGPaths{}, err
	}
	configHome := filepath.Join(home, ".config")
	dataHome := filepath.Join(home, ".local", "share")
	cacheHome := filepath.Join(home, ".cache")
	if value, ok := input.Environment["XDG_CONFIG_HOME"]; ok && strings.TrimSpace(value) != "" {
		configHome = strings.TrimSpace(value)
	}
	if value, ok := input.Environment["XDG_DATA_HOME"]; ok && strings.TrimSpace(value) != "" {
		dataHome = strings.TrimSpace(value)
	}
	if value, ok := input.Environment["XDG_CACHE_HOME"]; ok && strings.TrimSpace(value) != "" {
		cacheHome = strings.TrimSpace(value)
	}
	roots := []struct {
		name string
		path string
	}{{"config", configHome}, {"data", dataHome}, {"cache", cacheHome}}
	for _, root := range roots {
		if err := validateXDGRoot(root.name, root.path); err != nil {
			return XDGPaths{}, err
		}
	}
	for left := range roots {
		for right := left + 1; right < len(roots); right++ {
			if pathsOverlap(roots[left].path, roots[right].path) {
				return XDGPaths{}, fmt.Errorf("XDG %s and %s roots overlap", roots[left].name, roots[right].name)
			}
		}
	}
	dataRoot := filepath.Join(filepath.Clean(dataHome), "camp")
	runtimeRoot := filepath.Join(dataRoot, "runtime")
	if value, ok := input.Environment["XDG_RUNTIME_DIR"]; ok && strings.TrimSpace(value) != "" {
		runtimeHome := strings.TrimSpace(value)
		if err := validateXDGRoot("runtime", runtimeHome); err != nil {
			return XDGPaths{}, err
		}
		runtimeRoot = filepath.Join(filepath.Clean(runtimeHome), "camp")
	}
	return XDGPaths{
		ConfigPath:  filepath.Join(filepath.Clean(configHome), "camp", "config.yaml"),
		DataRoot:    dataRoot,
		WorkRoot:    filepath.Join(dataRoot, "work"),
		StoreRoot:   filepath.Join(dataRoot, "stores"),
		SessionRoot: filepath.Join(dataRoot, "sessions"),
		CacheRoot:   filepath.Join(filepath.Clean(cacheHome), "camp"),
		RuntimeRoot: runtimeRoot,
	}, nil
}

func ResolveFileBackend(raw string) (FileBackend, error) {
	if strings.TrimSpace(raw) != raw || !strings.HasPrefix(raw, "file:///") || strings.Contains(raw, "%") {
		return FileBackend{}, errors.New("file backend must be an unescaped absolute file:/// URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return FileBackend{}, fmt.Errorf("parse file backend: %w", err)
	}
	if parsed.Scheme != "file" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" {
		return FileBackend{}, errors.New("file backend URL may contain only an absolute local path")
	}
	root := filepath.Clean(parsed.Path)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) || root != parsed.Path || strings.ContainsRune(root, '\x00') {
		return FileBackend{}, errors.New("file backend root must be a clean non-root absolute path")
	}
	digest := sha256.Sum256([]byte("file\x00" + root))
	return FileBackend{Root: root, SanitizedURL: "file://" + root, Fingerprint: hex.EncodeToString(digest[:])}, nil
}

func validateXDGRoot(name, path string) error {
	if strings.ContainsRune(path, '\x00') || !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return fmt.Errorf("XDG %s root must be a non-root absolute path", name)
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	leftRelative, leftErr := filepath.Rel(left, right)
	rightRelative, rightErr := filepath.Rel(right, left)
	return leftErr == nil && leftRelative != ".." && !strings.HasPrefix(leftRelative, ".."+string(filepath.Separator)) ||
		rightErr == nil && rightRelative != ".." && !strings.HasPrefix(rightRelative, ".."+string(filepath.Separator))
}

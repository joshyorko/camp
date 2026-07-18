package tools

import (
	"errors"
	"fmt"
	"io"
	"regexp"

	"gopkg.in/yaml.v3"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

var (
	toolNamePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type Lock struct {
	SchemaVersion int             `yaml:"schemaVersion"`
	Tools         map[string]Tool `yaml:"tools"`
	Fixtures      Fixtures        `yaml:"fixtures"`
}

type Tool struct {
	Repository string                      `yaml:"repository"`
	Version    string                      `yaml:"version"`
	Commit     string                      `yaml:"commit"`
	Assets     map[string]map[string]Asset `yaml:"assets"`
}

type Asset struct {
	URL    string `yaml:"url"`
	SHA256 string `yaml:"sha256"`
}

type Fixtures struct {
	Room Fixture `yaml:"room"`
}

type Fixture struct {
	Repository string `yaml:"repository"`
	Version    string `yaml:"version"`
	Commit     string `yaml:"commit"`
}

func ParseLock(reader io.Reader) (Lock, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)
	var lock Lock
	if err := decoder.Decode(&lock); err != nil {
		return Lock{}, fmt.Errorf("decode distribution tool lock: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Lock{}, errors.New("distribution tool lock contains multiple YAML documents")
		}
		return Lock{}, fmt.Errorf("decode trailing distribution tool lock data: %w", err)
	}
	if err := lock.validate(); err != nil {
		return Lock{}, err
	}
	return lock, nil
}

func (l Lock) Resolve(name, goos, arch string) (Tool, Asset, error) {
	tool, ok := l.Tools[name]
	if !ok {
		return Tool{}, Asset{}, fmt.Errorf("tool %q is not locked", name)
	}
	platforms, ok := tool.Assets[goos]
	if !ok {
		return Tool{}, Asset{}, fmt.Errorf("tool %q has no assets for %s", name, goos)
	}
	asset, ok := platforms[arch]
	if !ok {
		return Tool{}, Asset{}, fmt.Errorf("tool %q has no asset for %s/%s", name, goos, arch)
	}
	return tool, asset, nil
}

func (l Lock) validate() error {
	if l.SchemaVersion != 1 {
		return fmt.Errorf("unsupported distribution tool lock schemaVersion %d", l.SchemaVersion)
	}
	if len(l.Tools) == 0 {
		return errors.New("distribution tool lock has no tools")
	}
	for name, tool := range l.Tools {
		if !toolNamePattern.MatchString(name) || !repositoryPattern.MatchString(tool.Repository) || !versionPattern.MatchString(tool.Version) || !commitPattern.MatchString(tool.Commit) {
			return fmt.Errorf("tool %q identity is incomplete", name)
		}
		if len(tool.Assets) == 0 {
			return fmt.Errorf("tool %q has no assets", name)
		}
		for goos, platforms := range tool.Assets {
			if !toolNamePattern.MatchString(goos) || len(platforms) == 0 {
				return fmt.Errorf("tool %q platform %q has no assets", name, goos)
			}
			for arch, asset := range platforms {
				if !toolNamePattern.MatchString(arch) || asset.URL == "" || !sha256Pattern.MatchString(asset.SHA256) {
					return fmt.Errorf("tool %q asset %s/%s is incomplete", name, goos, arch)
				}
			}
		}
	}
	if l.Fixtures.Room.Repository == "" || l.Fixtures.Room.Version == "" || l.Fixtures.Room.Commit == "" {
		return errors.New("Room fixture identity is incomplete")
	}
	return nil
}

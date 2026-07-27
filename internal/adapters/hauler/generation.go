package hauler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
	"gopkg.in/yaml.v3"
)

type GenerationArtifact struct {
	Path      string
	SHA256    string
	Size      int64
	Validated bool
}

type GenerationAssembler struct {
	client *Client
}

func NewGenerationAssembler(client *Client) *GenerationAssembler {
	return &GenerationAssembler{client: client}
}

func (a *GenerationAssembler) Assemble(ctx context.Context, manifest, workingDirectory, output string) (GenerationArtifact, error) {
	if a == nil || a.client == nil || !filepath.IsAbs(manifest) || !filepath.IsAbs(workingDirectory) || !filepath.IsAbs(output) {
		return GenerationArtifact{}, errors.New("generation assembler requires absolute paths and a Hauler client")
	}
	if _, err := os.Stat(manifest); err != nil {
		return GenerationArtifact{}, err
	}
	campDirectory := filepath.Dir(manifest)
	if filepath.Base(campDirectory) != ".camp" {
		return GenerationArtifact{}, errors.New("generation manifest must be the capsule .camp manifest")
	}
	capsuleRoot := filepath.Dir(campDirectory)
	expected, err := readGenerationExpectations(manifest)
	if err != nil {
		return GenerationArtifact{}, err
	}
	if _, err := os.Lstat(output); err == nil {
		return GenerationArtifact{}, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return GenerationArtifact{}, err
	}
	var store string
	var syncErrors []error
	for attempt := 1; attempt <= 2; attempt++ {
		candidate, err := os.MkdirTemp(workingDirectory, "fresh-hauler-store-")
		if err != nil {
			syncErrors = append(syncErrors, fmt.Errorf("attempt %d: create fresh store: %w", attempt, err))
			break
		}
		defer os.RemoveAll(candidate)
		result, runErr := a.client.SyncFrom(ctx, candidate, []string{manifest}, capsuleRoot)
		if syncErr := generationSyncError(attempt, result, runErr); syncErr == nil {
			store = candidate
			break
		} else {
			syncErrors = append(syncErrors, syncErr)
		}
		if ctx.Err() != nil {
			break
		}
	}
	if store == "" {
		return GenerationArtifact{}, fmt.Errorf("sync fresh Hauler store: %w", errors.Join(syncErrors...))
	}
	validation, err := os.MkdirTemp(workingDirectory, "validation-hauler-store-")
	if err != nil {
		return GenerationArtifact{}, err
	}
	defer os.RemoveAll(validation)
	validated := false
	defer func() {
		if !validated {
			_ = os.Remove(output)
		}
	}()
	if result, err := a.client.Save(ctx, store, output); err != nil || result.ExitCode != 0 {
		_ = os.Remove(output)
		return GenerationArtifact{}, fmt.Errorf("save Hauler generation: %w", err)
	}
	digest, size, err := hashFile(output)
	if err != nil {
		return GenerationArtifact{}, err
	}
	if result, err := a.client.Load(ctx, validation, []string{output}); err != nil || result.ExitCode != 0 {
		return GenerationArtifact{}, fmt.Errorf("load saved generation into fresh store: %w", err)
	}
	info, err := a.client.Info(ctx, validation)
	if err != nil || info.ExitCode != 0 {
		return GenerationArtifact{}, fmt.Errorf("inspect loaded generation: %w", err)
	}
	var entries []generationInfoEntry
	if len(info.Stdout) == 0 || json.Unmarshal(info.Stdout, &entries) != nil {
		return GenerationArtifact{}, errors.New("Hauler info validation did not return JSON")
	}
	if err := validateGenerationInfo(expected, entries); err != nil {
		return GenerationArtifact{}, err
	}
	validated = true
	return GenerationArtifact{Path: output, SHA256: digest, Size: size, Validated: true}, nil
}

func generationSyncError(attempt int, result ports.Result, err error) error {
	if err == nil && result.ExitCode == 0 {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("exit status %d", result.ExitCode)
	}
	if stderr := strings.TrimSpace(string(result.Stderr)); stderr != "" {
		return fmt.Errorf("attempt %d: %w; stderr: %s", attempt, err, stderr)
	}
	return fmt.Errorf("attempt %d: %w", attempt, err)
}

type generationExpectations struct {
	Files  []string
	Images []expectedGenerationImage
}

type expectedGenerationImage struct {
	Digest   string
	Platform string
}

type generationManifestDocument struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Files  []manifestFile  `yaml:"files"`
		Images []manifestImage `yaml:"images"`
	} `yaml:"spec"`
}

type generationInfoEntry struct {
	Reference string `json:"Reference"`
	Type      string `json:"Type"`
	Platform  string `json:"Platform"`
	Digest    string `json:"Digest"`
}

func readGenerationExpectations(path string) (generationExpectations, error) {
	file, err := os.Open(path)
	if err != nil {
		return generationExpectations{}, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	result := generationExpectations{}
	for {
		var document generationManifestDocument
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return generationExpectations{}, fmt.Errorf("decode Hauler generation manifest: %w", err)
		}
		switch document.Kind {
		case "Files":
			for _, item := range document.Spec.Files {
				if item.Name == "" || item.Path == "" {
					return generationExpectations{}, errors.New("Hauler file manifest entry is incomplete")
				}
				result.Files = append(result.Files, "hauler/"+item.Name+":latest")
			}
		case "Images":
			for _, item := range document.Spec.Images {
				digest := item.ExpectedDigest
				if digest == "" {
					separator := strings.LastIndexByte(item.Name, '@')
					if separator >= 1 {
						digest = item.Name[separator+1:]
					}
				}
				if !manifestDigestPattern.MatchString(digest) {
					return generationExpectations{}, errors.New("Hauler image manifest lacks a verified digest expectation")
				}
				result.Images = append(result.Images, expectedGenerationImage{Digest: digest, Platform: item.Platform})
			}
		default:
			return generationExpectations{}, fmt.Errorf("unsupported Hauler manifest kind %q", document.Kind)
		}
	}
	if len(result.Files) == 0 {
		return generationExpectations{}, errors.New("Hauler generation manifest has no root archive")
	}
	return result, nil
}

func validateGenerationInfo(expected generationExpectations, entries []generationInfoEntry) error {
	for _, reference := range expected.Files {
		found := false
		for _, entry := range entries {
			if entry.Type == "file" && entry.Reference == reference && manifestDigestPattern.MatchString(entry.Digest) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("Hauler validation is missing expected root archive %q", reference)
		}
	}
	for _, image := range expected.Images {
		found := false
		for _, entry := range entries {
			platformMatches := image.Platform == "" || entry.Platform == image.Platform || entry.Platform == "-"
			digestMatches := entry.Digest == image.Digest || strings.HasSuffix(entry.Reference, "@"+image.Digest)
			if entry.Type == "image" && digestMatches && platformMatches {
				found = true
				break
			}
		}
		if !found {
			observed, _ := json.Marshal(entries)
			return fmt.Errorf("Hauler validation is missing expected image digest %s; observed entries: %s", image.Digest, observed)
		}
	}
	return nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

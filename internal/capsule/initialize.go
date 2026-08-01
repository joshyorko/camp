package capsule

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"gopkg.in/yaml.v3"
)

var ErrInitializationConflict = errors.New("capsule initialization conflicts with existing state")

const (
	roomImage      = "ghcr.io/joshyorko/room-of-requirement:wolfi"
	roomRepository = "joshyorko/room-of-requirement"
	roomVersion    = "v1.18.3"
	roomCommit     = "3d675a1fbc4c2c494730722e6396a42416a35e22"
	devpodVersion  = "v0.26.1"
	haulerVersion  = "v2.0.2"
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type NowClock interface{ Now() time.Time }

type DigestResolver interface {
	Resolve(context.Context, string) (string, error)
}

type CommandDigestResolver struct {
	executable string
	runner     ports.Runner
}

func NewCommandDigestResolver(executable string, runner ports.Runner) *CommandDigestResolver {
	return &CommandDigestResolver{executable: executable, runner: runner}
}

func (r *CommandDigestResolver) Resolve(ctx context.Context, reference string) (string, error) {
	if r == nil || r.executable == "" || r.runner == nil || reference == "" {
		return "", errors.New("Docker manifest resolver is unconfigured")
	}
	result, err := r.runner.Run(ctx, ports.Command{Executable: r.executable, Argv: []string{"manifest", "inspect", "--verbose", reference}})
	if err != nil {
		return "", err
	}
	type descriptor struct {
		Digest string `json:"digest"`
	}
	type entry struct {
		Descriptor descriptor `json:"Descriptor"`
	}
	var entries []entry
	if err := json.Unmarshal(result.Stdout, &entries); err == nil {
		for _, candidate := range entries {
			if digestPattern.MatchString(candidate.Descriptor.Digest) {
				return candidate.Descriptor.Digest, nil
			}
		}
	}
	var single entry
	if err := json.Unmarshal(result.Stdout, &single); err == nil && digestPattern.MatchString(single.Descriptor.Digest) {
		return single.Descriptor.Digest, nil
	}
	return "", errors.New("Docker manifest output lacks a valid descriptor digest")
}

func (r *CommandDigestResolver) ResolveConfigDigest(ctx context.Context, reference string) (string, error) {
	if r == nil || r.executable == "" || r.runner == nil || reference == "" {
		return "", errors.New("Docker manifest resolver is unconfigured")
	}
	result, err := r.runner.Run(ctx, ports.Command{Executable: r.executable, Argv: []string{"manifest", "inspect", "--verbose", reference}})
	if err != nil || result.ExitCode != 0 {
		return "", fmt.Errorf("inspect Docker manifest config: %w", err)
	}
	type manifest struct {
		Descriptor struct {
			Platform struct {
				OS           string `json:"os"`
				Architecture string `json:"architecture"`
			} `json:"platform"`
		} `json:"Descriptor"`
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		SchemaV2Manifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		} `json:"SchemaV2Manifest"`
		OCIManifest struct {
			Config struct {
				Digest string `json:"digest"`
			} `json:"config"`
		} `json:"OCIManifest"`
	}
	configDigest := func(candidate manifest) string {
		if digestPattern.MatchString(candidate.OCIManifest.Config.Digest) {
			return candidate.OCIManifest.Config.Digest
		}
		if digestPattern.MatchString(candidate.SchemaV2Manifest.Config.Digest) {
			return candidate.SchemaV2Manifest.Config.Digest
		}
		if digestPattern.MatchString(candidate.Config.Digest) {
			return candidate.Config.Digest
		}
		return ""
	}
	var entries []manifest
	if err := json.Unmarshal(result.Stdout, &entries); err == nil {
		for _, candidate := range entries {
			if candidate.Descriptor.Platform.OS == runtime.GOOS && candidate.Descriptor.Platform.Architecture == runtime.GOARCH {
				if digest := configDigest(candidate); digest != "" {
					return digest, nil
				}
			}
		}
	}
	var single manifest
	if err := json.Unmarshal(result.Stdout, &single); err == nil {
		if digest := configDigest(single); digest != "" {
			return digest, nil
		}
	}
	return "", errors.New("Docker manifest output lacks a valid config digest for the host platform")
}

type Initializer struct {
	clock    NowClock
	resolver DigestResolver
}

type Initialization struct {
	Metadata domain.CapsuleMetadata
	Lock     domain.CapsuleLock
}

func NewInitializer(clock NowClock, resolver DigestResolver) *Initializer {
	return &Initializer{clock: clock, resolver: resolver}
}

func (i *Initializer) Initialize(ctx context.Context, root, capsuleID string) (Initialization, error) {
	if i.clock == nil || i.resolver == nil {
		return Initialization{}, errors.New("initializer dependencies are incomplete")
	}
	if capsuleID == "" || filepath.Base(capsuleID) != capsuleID || capsuleID == "." || capsuleID == ".." {
		return Initialization{}, errors.New("invalid capsule id")
	}
	canonical, _, err := validateSourceRoot(root)
	if err != nil {
		return Initialization{}, err
	}
	campDirectory := filepath.Join(canonical, ".camp")
	metadataPath := filepath.Join(campDirectory, "capsule.yaml")
	if existing, err := readExistingInitialization(metadataPath, filepath.Join(campDirectory, "lock.yaml"), capsuleID); err == nil {
		return existing, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Initialization{}, err
	}
	digest, err := i.resolver.Resolve(ctx, roomImage)
	if err != nil {
		return Initialization{}, fmt.Errorf("resolve Room image digest: %w", err)
	}
	if !digestPattern.MatchString(digest) {
		return Initialization{}, fmt.Errorf("resolved Room digest %q is invalid", digest)
	}
	initialization := Initialization{
		Metadata: domain.CapsuleMetadata{SchemaVersion: domain.SchemaVersion, ID: capsuleID, DefaultBranch: "main", CreatedAt: i.clock.Now().UTC()},
		Lock: domain.CapsuleLock{
			SchemaVersion: domain.SchemaVersion,
			Room:          domain.RoomLock{Repository: roomRepository, Version: roomVersion, Commit: roomCommit, Image: roomImage, Digest: digest},
			Tools:         domain.ToolVersions{DevPod: devpodVersion, Hauler: haulerVersion},
		},
	}
	if initialization.Metadata.CreatedAt.IsZero() {
		return Initialization{}, errors.New("initializer clock returned zero time")
	}
	if err := os.MkdirAll(campDirectory, 0o700); err != nil {
		return Initialization{}, fmt.Errorf("create .camp directory: %w", err)
	}
	metadataBody, _ := yaml.Marshal(initialization.Metadata)
	lockBody, _ := yaml.Marshal(initialization.Lock)
	imagesBody, _ := json.MarshalIndent(domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: initialization.Metadata.CreatedAt, Images: []domain.Image{}}, "", "  ")
	manifestBody := []byte(fmt.Sprintf("apiVersion: content.hauler.cattle.io/v1\nkind: Files\nmetadata:\n  name: camp-%s\nspec:\n  files:\n    - path: .camp/build/%s.tar.zst\n      name: %s.tar.zst\n---\napiVersion: content.hauler.cattle.io/v1\nkind: Images\nmetadata:\n  name: camp-%s-images\nspec:\n  images: []\n", capsuleID, capsuleID, capsuleID, capsuleID))
	// Metadata is the initialization commit marker, so stable supporting
	// documents are durable first.
	for _, document := range []struct {
		name string
		body []byte
	}{
		{"lock.yaml", lockBody}, {"images.json", append(imagesBody, '\n')}, {"hauler-manifest.yaml", manifestBody}, {"capsule.yaml", metadataBody},
	} {
		if err := writeStable(filepath.Join(campDirectory, document.name), document.body); err != nil {
			return Initialization{}, err
		}
	}
	if err := syncDir(campDirectory); err != nil {
		return Initialization{}, err
	}
	return initialization, nil
}

func readExistingInitialization(metadataPath, lockPath, capsuleID string) (Initialization, error) {
	metadataBody, err := os.ReadFile(metadataPath)
	if err != nil {
		return Initialization{}, err
	}
	var metadata domain.CapsuleMetadata
	if err := yaml.Unmarshal(metadataBody, &metadata); err != nil || metadata.SchemaVersion != domain.SchemaVersion || metadata.ID != capsuleID || metadata.DefaultBranch == "" || metadata.CreatedAt.IsZero() {
		return Initialization{}, fmt.Errorf("existing capsule metadata does not match %q: %w", capsuleID, ErrInitializationConflict)
	}
	lockBody, err := os.ReadFile(lockPath)
	if err != nil {
		return Initialization{}, fmt.Errorf("initialized capsule lacks lock: %w", ErrInitializationConflict)
	}
	var lock domain.CapsuleLock
	if err := yaml.Unmarshal(lockBody, &lock); err != nil || lock.SchemaVersion != domain.SchemaVersion || !digestPattern.MatchString(lock.Room.Digest) {
		return Initialization{}, fmt.Errorf("existing capsule lock is invalid: %w", ErrInitializationConflict)
	}
	return Initialization{Metadata: metadata, Lock: lock}, nil
}

func writeStable(path string, body []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, body) {
			return nil
		}
		return fmt.Errorf("stable document %q differs: %w", path, ErrInitializationConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := path + ".partial"
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncDir(filepath.Dir(path))
}

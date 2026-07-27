package haulkit

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type staticStoreValidator struct {
	identity StoreIdentity
}

func (v staticStoreValidator) ValidateStore(context.Context, string) (StoreIdentity, error) {
	return v.identity, nil
}

func (v staticStoreValidator) PrepareStore(_ context.Context, source, destination string) (StoreIdentity, error) {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return StoreIdentity{}, err
	}
	sourceFile, err := openRegular(filepath.Join(source, "index.json"))
	if err != nil {
		return StoreIdentity{}, err
	}
	defer sourceFile.Close()
	body, err := io.ReadAll(sourceFile)
	if err != nil {
		return StoreIdentity{}, err
	}
	if err := os.WriteFile(filepath.Join(destination, "index.json"), body, 0o400); err != nil {
		return StoreIdentity{}, err
	}
	return v.identity, nil
}

func (v staticStoreValidator) ObserveRoot(_ context.Context, _ string, reference string) (RootIdentity, error) {
	canonical, err := NormalizeRootReference(reference)
	if err != nil {
		return RootIdentity{}, err
	}
	for _, entry := range v.identity.Entries {
		if entry.Type == "file" && entry.Reference == canonical {
			return RootIdentity{Reference: canonical, SHA256: entry.Digest, Size: entry.Size}, nil
		}
	}
	return RootIdentity{}, errors.New("root not found")
}

func TestBuilderProducesDeterministicVerifiedReadyStoreArchive(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}

	first, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.OutputDirectory = filepath.Join(t.TempDir(), "second")
	if err := os.Mkdir(request.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	second, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 || first.Size != second.Size {
		t.Fatalf("archives differ: %#v != %#v", first, second)
	}
	for _, path := range []string{first.ArchivePath, first.ManifestPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode = %o, want private", path, info.Mode().Perm())
		}
	}
}

func TestBuilderOfficialStorePreparationExcludesUntrackedLinks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, BuildRequest)
	}{
		{"symlink", func(t *testing.T, request BuildRequest) {
			t.Helper()
			if err := os.Symlink(filepath.Join(request.StoreDirectory, "index.json"), filepath.Join(request.StoreDirectory, "link")); err != nil {
				t.Fatal(err)
			}
		}},
		{"hardlink", func(t *testing.T, request BuildRequest) {
			t.Helper()
			if err := os.Link(filepath.Join(request.StoreDirectory, "index.json"), filepath.Join(request.StoreDirectory, "hardlink")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, validator := buildFixture(t)
			test.setup(t, request)
			builder := NewBuilder(validator)
			builder.chunkSize = 64
			builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
			if _, err := builder.Build(context.Background(), request); err != nil {
				t.Fatalf("Build() error = %v", err)
			}
		})
	}
}

func TestBuilderRejectsCorruptionAtChunkReassemblyAcceptanceBarrier(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
	builder.afterSplit = func(directory string, chunks []ChunkIdentity) error {
		path := filepath.Join(directory, chunks[0].Name)
		if err := os.Chmod(path, 0o600); err != nil {
			return err
		}
		return os.WriteFile(path, []byte("corrupt"), 0o600)
	}
	if _, err := builder.Build(context.Background(), request); err == nil {
		t.Fatal("Build() error = nil")
	}
	for _, name := range []string{"camp-hauler-kit.tar.zst", "camp-hauler-kit.json", "chunks"} {
		if _, err := os.Stat(filepath.Join(request.OutputDirectory, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed build retained %s: %v", name, err)
		}
	}
}

func TestBuilderRejectsStoreFileSwappedToSymlinkAfterValidation(t *testing.T) {
	request, validator := buildFixture(t)
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("must-not-archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
	builder.afterStoreValidation = func() error {
		index := filepath.Join(request.StoreDirectory, "index.json")
		if err := os.Remove(index); err != nil {
			return err
		}
		return os.Symlink(secret, index)
	}
	if _, err := builder.Build(context.Background(), request); err == nil {
		t.Fatal("Build() error = nil")
	}
}

func TestBuilderDerivesRuntimeIdentityAndRejectsWrongCallerClaims(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
	request.Architecture = "linux/not-host"
	if _, err := builder.Build(context.Background(), request); err == nil {
		t.Fatal("Build() accepted caller architecture")
	}
	request.Architecture = "linux/" + runtime.GOARCH
	request.HaulerVersion = "v0.0.0"
	if _, err := builder.Build(context.Background(), request); err == nil {
		t.Fatal("Build() accepted caller Hauler version")
	} else if strings.Contains(err.Error(), "%!w") || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Build() identity diagnostic = %q", err)
	}
}

func TestBuilderRecordsObservedCampIdentityWhenCallerDoesNotProvideOne(t *testing.T) {
	request, validator := buildFixture(t)
	request.CampVersion = ""
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable, probe: func(_ context.Context, _ string, kind string) (string, error) {
		if kind == "camp" {
			return "camp 1.2.3\n", nil
		}
		return fixtureRuntimeProbe(context.Background(), "", kind)
	}}
	artifact, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tools.Camp.Version != "camp 1.2.3" {
		t.Fatalf("Camp version = %q", manifest.Tools.Camp.Version)
	}
}

func TestBuilderProbesAndHashesTheSamePrivateToolSnapshot(t *testing.T) {
	request, validator := buildFixture(t)
	originalDigest, _, err := hashPath(request.CampExecutable)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(replacement, []byte("attacker-controlled"), 0o700); err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable, probe: func(ctx context.Context, path, kind string) (string, error) {
		if kind == "camp" {
			if err := os.Remove(request.CampExecutable); err != nil {
				return "", err
			}
			if err := os.Symlink(replacement, request.CampExecutable); err != nil {
				return "", err
			}
		}
		return fixtureRuntimeProbe(ctx, path, kind)
	}}
	artifact, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tools.Camp.SHA256 != originalDigest {
		t.Fatalf("Camp digest = %s, want snapshotted %s", manifest.Tools.Camp.SHA256, originalDigest)
	}
}

func TestProductionRuntimeObserverUsesCampDashDashVersion(t *testing.T) {
	directory := t.TempDir()
	arguments := filepath.Join(directory, "arguments")
	executable := filepath.Join(directory, "camp")
	script := "#!/bin/sh\nprintf '%s' \"$1\" > " + arguments + "\nprintf 'dev\\n'\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := (productionRuntimeObserver{}).Probe(context.Background(), executable, "camp")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "dev" {
		t.Fatalf("output = %q", output)
	}
	body, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "--version" {
		t.Fatalf("Camp argv = %q, want --version", body)
	}
}

func TestBuilderUsesObservedRunningCampExecutableInsteadOfCallerPath(t *testing.T) {
	request, validator := buildFixture(t)
	runningCamp := filepath.Join(t.TempDir(), "running-camp")
	if err := os.WriteFile(runningCamp, []byte("running-camp-bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	runningDigest, _, err := hashPath(runningCamp)
	if err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: runningCamp}
	artifact, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tools.Camp.SHA256 != runningDigest {
		t.Fatalf("Camp digest = %s, want running executable %s", manifest.Tools.Camp.SHA256, runningDigest)
	}
}

func TestBuilderKeepsOpenedRunningCampIdentityAcrossPathReplacement(t *testing.T) {
	request, validator := buildFixture(t)
	originalDigest, _, err := hashPath(request.CampExecutable)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(t.TempDir(), "replacement")
	if err := os.WriteFile(replacement, []byte("replacement-running-bytes"), 0o700); err != nil {
		t.Fatal(err)
	}
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = &swappingRuntimeObserver{
		path:        request.CampExecutable,
		replacement: replacement,
	}
	artifact, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tools.Camp.SHA256 != originalDigest {
		t.Fatalf("Camp digest = %s, want opened running bytes %s", manifest.Tools.Camp.SHA256, originalDigest)
	}
}

type swappingRuntimeObserver struct {
	path        string
	replacement string
	swapped     bool
}

func (observer *swappingRuntimeObserver) RunningExecutable() (string, error) {
	if err := observer.swap(); err != nil {
		return "", err
	}
	return observer.path, nil
}

func (observer *swappingRuntimeObserver) OpenRunningExecutable() (*os.File, error) {
	file, err := openRegular(observer.path)
	if err != nil {
		return nil, err
	}
	if err := observer.swap(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func (observer *swappingRuntimeObserver) Probe(ctx context.Context, path, kind string) (string, error) {
	return fixtureRuntimeProbe(ctx, path, kind)
}

func (observer *swappingRuntimeObserver) swap() error {
	if observer.swapped {
		return nil
	}
	observer.swapped = true
	if err := os.Remove(observer.path); err != nil {
		return err
	}
	return os.Rename(observer.replacement, observer.path)
}

type fakeRuntimeObserver struct {
	runningCamp string
	probe       func(context.Context, string, string) (string, error)
}

func (observer fakeRuntimeObserver) OpenRunningExecutable() (*os.File, error) {
	return openRegular(observer.runningCamp)
}

func (observer fakeRuntimeObserver) Probe(ctx context.Context, path, kind string) (string, error) {
	if observer.probe != nil {
		return observer.probe(ctx, path, kind)
	}
	return fixtureRuntimeProbe(ctx, path, kind)
}

func fixtureRuntimeProbe(_ context.Context, _ string, kind string) (string, error) {
	switch kind {
	case "camp":
		return "dev", nil
	case "hauler":
		return "v2.0.2", nil
	case "pasta":
		return "pasta 1", nil
	default:
		return "", errors.New("unknown tool")
	}
}

func buildFixture(t *testing.T) (BuildRequest, StoreValidator) {
	t.Helper()
	root := t.TempDir()
	store := filepath.Join(root, "store")
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	indexBody := []byte(`{"ready":true}`)
	indexPath := filepath.Join(store, "index.json")
	if err := os.WriteFile(indexPath, indexBody, 0o600); err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]string)
	for _, name := range []string{"camp", "hauler", "pasta"} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(name+"-executable"), 0o700); err != nil {
			t.Fatal(err)
		}
		tools[name] = path
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		t.Fatal(err)
	}
	indexDigest, _, err := hashPath(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	storeIdentity := StoreIdentity{
		HaulerVersion: "v2.0.2",
		IndexSHA256:   indexDigest,
		Entries:       []StoreEntry{{Reference: "hauler/root.tar.zst:latest", Type: "file", Digest: indexDigest, Size: int64(len(indexBody))}},
	}
	return BuildRequest{
		SessionID:        "session-1",
		Capsule:          "capsule",
		Lineage:          domain.Lineage{Branch: "main"},
		Generation:       &domain.GenerationRef{Generation: 1, ArchiveSHA256: indexDigest},
		Architecture:     "linux/" + runtime.GOARCH,
		StoreDirectory:   store,
		Root:             RootIdentity{Reference: "hauler/root.tar.zst:latest", SHA256: indexDigest, Size: int64(len(indexBody))},
		CampExecutable:   tools["camp"],
		CampVersion:      "dev",
		HaulerExecutable: tools["hauler"],
		HaulerVersion:    "v2.0.2",
		PastaExecutable:  tools["pasta"],
		PastaVersion:     "pasta 1",
		OutputDirectory:  output,
	}, staticStoreValidator{identity: storeIdentity}
}

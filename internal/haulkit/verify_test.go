package haulkit

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestVerifierRejectsWrongArchitectureToolIdentityAndStoreDrift(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
	artifact, err := builder.Build(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	manifestBody, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := DecodeCanonical(manifestBody)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		validator StoreValidator
		mutate    func(*VerifyRequest)
	}{
		{"architecture", validator, func(request *VerifyRequest) { request.Architecture = "linux/not-host" }},
		{"tool version", validator, func(request *VerifyRequest) { request.Tools.Hauler.Version = "v0.0.0" }},
		{"tool digest", validator, func(request *VerifyRequest) { request.Tools.Pasta.SHA256 = strings.Repeat("b", 64) }},
		{"store drift", staticStoreValidator{identity: StoreIdentity{
			HaulerVersion: manifest.Store.HaulerVersion,
			IndexSHA256:   strings.Repeat("b", 64),
			Entries:       manifest.Store.Entries,
		}}, func(*VerifyRequest) {}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verifyRequest := VerifyRequest{
				ManifestPath:   artifact.ManifestPath,
				ArchivePath:    artifact.ArchivePath,
				Architecture:   manifest.Architecture,
				Tools:          manifest.Tools,
				StoreDirectory: request.StoreDirectory,
			}
			test.mutate(&verifyRequest)
			if _, err := NewVerifier(test.validator).Verify(context.Background(), verifyRequest); err == nil {
				t.Fatal("Verify() error = nil")
			}
		})
	}
}

func TestVerifierBindsRootReferenceDigestAndSizeToObservedStore(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*RootIdentity)
	}{
		{"reference", func(root *RootIdentity) { root.Reference = "hauler/other.tar.zst:latest" }},
		{"digest", func(root *RootIdentity) { root.SHA256 = strings.Repeat("b", 64) }},
		{"size", func(root *RootIdentity) { root.Size++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, validator := buildFixture(t)
			builder := NewBuilder(validator)
			builder.chunkSize = 64
			builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
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
			test.mutate(&manifest.Root)
			body, err = MarshalCanonical(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(artifact.ManifestPath, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(artifact.ManifestPath, body, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = NewVerifier(validator).Verify(context.Background(), VerifyRequest{
				ManifestPath: artifact.ManifestPath,
				ArchivePath:  artifact.ArchivePath,
				Architecture: manifest.Architecture,
				Tools:        manifest.Tools,
			})
			if !errors.Is(err, ErrIdentityMismatch) {
				t.Fatalf("Verify() error = %v, want ErrIdentityMismatch", err)
			}
		})
	}
}

func TestVerifierRejectsMaliciousPathsAndOuterLinks(t *testing.T) {
	for _, test := range []struct {
		name     string
		entry    string
		typeflag byte
		linkname string
	}{
		{"traversal", "../escape", tar.TypeReg, ""},
		{"absolute", "/tmp/escape", tar.TypeReg, ""},
		{"symlink", "store/link", tar.TypeSymlink, "../../escape"},
		{"hardlink", "store/link", tar.TypeLink, "store/index.json"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			archive := filepath.Join(directory, "hostile.tar.zst")
			writeHostileArchive(t, archive, test.entry, test.typeflag, test.linkname)
			digest, size, err := hashPath(archive)
			if err != nil {
				t.Fatal(err)
			}
			manifest := validTestManifest()
			manifest.Archive = ArchiveIdentity{SHA256: digest, Size: size}
			manifest.Chunks = []ChunkIdentity{{Index: 0, Name: "hostile.part", SHA256: digest, Size: size}}
			manifestPath := filepath.Join(directory, "manifest.json")
			body, err := MarshalCanonical(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(manifestPath, body, 0o400); err != nil {
				t.Fatal(err)
			}
			_, err = NewVerifier(staticStoreValidator{identity: manifest.Store}).Verify(context.Background(), VerifyRequest{
				ManifestPath: manifestPath,
				ArchivePath:  archive,
				Architecture: manifest.Architecture,
				Tools:        manifest.Tools,
			})
			if !errors.Is(err, ErrUnsafeKit) {
				t.Fatalf("Verify() error = %v, want ErrUnsafeKit", err)
			}
		})
	}
}

func TestVerifierStagesExtractionAndRemovesItOnLateStoreFailure(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
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
	destination := filepath.Join(t.TempDir(), "ready")
	drifted := manifest.Store
	drifted.IndexSHA256 = strings.Repeat("b", 64)
	_, err = NewVerifier(staticStoreValidator{identity: drifted}).Verify(context.Background(), VerifyRequest{
		ManifestPath: artifact.ManifestPath,
		ArchivePath:  artifact.ArchivePath,
		Architecture: manifest.Architecture,
		Tools:        manifest.Tools,
		Destination:  destination,
	})
	if err == nil {
		t.Fatal("Verify() error = nil")
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed verification retained destination: %v", statErr)
	}
	matches, globErr := filepath.Glob(filepath.Join(filepath.Dir(destination), ".haulkit-stage-*"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(matches) != 0 {
		t.Fatalf("failed verification retained stages: %v", matches)
	}
}

func TestVerifierBoundsOuterEntriesAndExpandedBytes(t *testing.T) {
	for _, test := range []struct {
		name   string
		limits readyArchiveLimits
	}{
		{"entries", readyArchiveLimits{MaxEntries: 1, MaxFileBytes: 1024, MaxExpandedBytes: 2048, MaxDecoderMemory: 1 << 20}},
		{"file bytes", readyArchiveLimits{MaxEntries: 8, MaxFileBytes: 0, MaxExpandedBytes: 16, MaxDecoderMemory: 1 << 20}},
		{"total bytes", readyArchiveLimits{MaxEntries: 8, MaxFileBytes: 16, MaxExpandedBytes: 0, MaxDecoderMemory: 1 << 20}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			archive := filepath.Join(directory, "bounded.tar.zst")
			if test.name == "entries" {
				writeRegularArchive(t, archive, []string{"store/one", "store/two"})
			} else {
				writeHostileArchive(t, archive, "store/content", tar.TypeReg, "")
			}
			digest, size, err := hashPath(archive)
			if err != nil {
				t.Fatal(err)
			}
			manifest := validTestManifest()
			manifest.Archive = ArchiveIdentity{SHA256: digest, Size: size}
			manifest.Chunks = []ChunkIdentity{{Index: 0, Name: "bounded.part", SHA256: digest, Size: size}}
			body, err := MarshalCanonical(manifest)
			if err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(directory, "manifest.json")
			if err := os.WriteFile(manifestPath, body, 0o400); err != nil {
				t.Fatal(err)
			}
			verifier := NewVerifier(staticStoreValidator{identity: manifest.Store})
			verifier.limits = test.limits
			_, err = verifier.Verify(context.Background(), VerifyRequest{
				ManifestPath: manifestPath,
				ArchivePath:  archive,
				Architecture: manifest.Architecture,
				Tools:        manifest.Tools,
			})
			if !errors.Is(err, ErrArchiveLimit) {
				t.Fatalf("Verify() error = %v, want ErrArchiveLimit", err)
			}
		})
	}
}

func writeRegularArchive(t *testing.T, path string, names []string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	for _, name := range names {
		if err := writer.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o444, Size: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeHostileArchive(t *testing.T, path, name string, typeflag byte, linkname string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(file, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	header := &tar.Header{Name: name, Typeflag: typeflag, Linkname: linkname, Mode: 0o444}
	if typeflag == tar.TypeReg {
		header.Size = 1
	}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if typeflag == tar.TypeReg {
		if _, err := writer.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

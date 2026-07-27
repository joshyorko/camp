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

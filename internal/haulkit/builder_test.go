package haulkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type staticStoreValidator struct {
	identity StoreIdentity
}

func (v staticStoreValidator) ValidateStore(context.Context, string) (StoreIdentity, error) {
	return v.identity, nil
}

func TestBuilderProducesDeterministicVerifiedReadyStoreArchive(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64

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

func TestBuilderRejectsOuterSymlinksAndHardlinks(t *testing.T) {
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
			if _, err := builder.Build(context.Background(), request); err == nil {
				t.Fatal("Build() error = nil")
			}
		})
	}
}

func TestBuilderRejectsCorruptionAtChunkReassemblyAcceptanceBarrier(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
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
		Entries:       []StoreEntry{{Reference: "root.tar.zst", Type: "file", Digest: indexDigest}},
	}
	return BuildRequest{
		SessionID:        "session-1",
		Capsule:          "capsule",
		Lineage:          domain.Lineage{Branch: "main"},
		Generation:       &domain.GenerationRef{Generation: 1, ArchiveSHA256: indexDigest},
		Architecture:     "linux/" + runtime.GOARCH,
		StoreDirectory:   store,
		Root:             RootIdentity{Reference: "root.tar.zst", SHA256: indexDigest, Size: int64(len(indexBody))},
		CampExecutable:   tools["camp"],
		CampVersion:      "dev",
		HaulerExecutable: tools["hauler"],
		HaulerVersion:    "v2.0.2",
		PastaExecutable:  tools["pasta"],
		PastaVersion:     "pasta 1",
		OutputDirectory:  output,
	}, staticStoreValidator{identity: storeIdentity}
}

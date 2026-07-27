package haulkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBuilderCleansOwnedOutputsAfterEveryAtomicPublicationBoundary(t *testing.T) {
	for _, boundary := range []string{
		"archive-finalize",
		"zstd-finalize",
		"chunk-write",
		"file-fsync",
		"publish",
		"directory-fsync",
		"reassembly-write",
		"manifest-write",
		"extraction-write",
		"extraction-file-fsync",
		"extraction-directory-fsync",
		"extraction-publish",
		"cleanup-remove",
	} {
		t.Run(boundary, func(t *testing.T) {
			request, validator := buildFixture(t)
			builder := NewBuilder(validator)
			builder.chunkSize = 64
			builder.runtimeProbe = fixtureRuntimeProbe
			previous := atomicBoundaryHook
			t.Cleanup(func() { atomicBoundaryHook = previous })
			atomicBoundaryHook = func(observed string) error {
				if observed == boundary {
					return errors.New("injected " + boundary + " failure")
				}
				return nil
			}

			if _, err := builder.Build(context.Background(), request); err == nil {
				t.Fatal("Build() error = nil")
			}
			for _, name := range []string{"camp-hauler-kit.tar.zst", "camp-hauler-kit.json", "chunks"} {
				if _, err := os.Stat(filepath.Join(request.OutputDirectory, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failure at %s retained %s: %v", boundary, name, err)
				}
			}
			matches, err := filepath.Glob(filepath.Join(request.OutputDirectory, ".haulkit-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("failure at %s retained temporaries: %v", boundary, matches)
			}
		})
	}
}

func TestAtomicPublicationNeverReplacesExistingOutput(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "manifest.json")
	if err := os.WriteFile(output, []byte("original"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateAtomic(output, []byte("replacement")); err == nil {
		t.Fatal("writePrivateAtomic() error = nil")
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "original" {
		t.Fatalf("existing output = %q", body)
	}
}

package haulkit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBuilderCleansOwnedOutputsAfterEveryAtomicPublicationBoundary(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
	previous := atomicBoundaryHook
	t.Cleanup(func() { atomicBoundaryHook = previous })
	var trace []atomicBoundaryEvent
	atomicBoundaryHook = func(observed atomicBoundaryEvent) error {
		trace = append(trace, observed)
		return nil
	}
	if _, err := builder.Build(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	required := []string{
		"tool-snapshot-write", "tool-snapshot-fsync",
		"archive-header-write", "archive-content-write", "archive-finalize", "zstd-finalize",
		"chunk-write", "file-fsync", "publish", "directory-fsync",
		"reassembly-write", "manifest-write",
		"extraction-write", "extraction-file-fsync", "extraction-directory-fsync",
		"extraction-publish", "cleanup-remove",
	}
	for _, requiredBoundary := range required {
		found := false
		for _, observed := range trace {
			if observed.Name == requiredBoundary {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("durability trace missing %q: %v", requiredBoundary, trace)
		}
	}

	for _, boundary := range trace {
		t.Run(fmt.Sprintf("%s-%d", boundary.Name, boundary.Occurrence), func(t *testing.T) {
			request, validator := buildFixture(t)
			builder := NewBuilder(validator)
			builder.chunkSize = 64
			builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
			fired := false
			recoveryFsync := false
			atomicBoundaryHook = func(observed atomicBoundaryEvent) error {
				if fired {
					if observed.Name == "directory-fsync" {
						recoveryFsync = true
					}
					return nil
				}
				if observed == boundary {
					fired = true
					return errors.New("injected " + boundary.Name + " failure")
				}
				return nil
			}

			if _, err := builder.Build(context.Background(), request); err == nil {
				t.Fatal("Build() error = nil")
			}
			if !fired {
				t.Fatal("targeted occurrence was not reached")
			}
			for _, name := range []string{"camp-hauler-kit.tar.zst", "camp-hauler-kit.json", "chunks"} {
				if _, err := os.Stat(filepath.Join(request.OutputDirectory, name)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failure at %s retained %s: %v", boundary.Name, name, err)
				}
			}
			matches, err := filepath.Glob(filepath.Join(request.OutputDirectory, ".haulkit-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("failure at %s retained temporaries: %v", boundary.Name, matches)
			}
			if !recoveryFsync {
				t.Fatalf("failure at %s did not reach a disarmed recovery directory fsync", boundary.Name)
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

func TestCleanupFailureDoesNotMutateUntilDisarmedOwnerRetry(t *testing.T) {
	request, validator := buildFixture(t)
	builder := NewBuilder(validator)
	builder.chunkSize = 64
	builder.runtimeObserver = fakeRuntimeObserver{runningCamp: request.CampExecutable}
	previous := atomicBoundaryHook
	t.Cleanup(func() { atomicBoundaryHook = previous })
	verifiedReady := filepath.Join(request.OutputDirectory, ".haulkit-verified-ready")
	firstSawOwnedPath := false
	retrySawOwnedPath := false
	recoveryFsync := false
	fired := false
	atomicBoundaryHook = func(event atomicBoundaryEvent) error {
		if event.Name == "cleanup-remove" && !fired {
			fired = true
			_, err := os.Stat(verifiedReady)
			firstSawOwnedPath = err == nil
			return errors.New("injected cleanup remove failure")
		}
		if event.Name == "cleanup-remove" && fired && !retrySawOwnedPath {
			_, err := os.Stat(verifiedReady)
			retrySawOwnedPath = err == nil
		}
		if fired && event.Name == "directory-fsync" {
			recoveryFsync = true
		}
		return nil
	}
	if _, err := builder.Build(context.Background(), request); err == nil {
		t.Fatal("Build() error = nil")
	}
	if !firstSawOwnedPath || !retrySawOwnedPath {
		t.Fatalf("cleanup path visibility: first=%v retry=%v", firstSawOwnedPath, retrySawOwnedPath)
	}
	if _, err := os.Stat(verifiedReady); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owner retry retained verified stage: %v", err)
	}
	if !recoveryFsync {
		t.Fatal("owner retry did not reach parent-directory fsync")
	}
}

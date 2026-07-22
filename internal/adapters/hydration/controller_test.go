package hydration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"golang.org/x/sys/unix"
)

func TestHydrateCommitsCreatedOwnershipOnlyAfterVerifiedAtomicRename(t *testing.T) {
	t.Parallel()
	fixture := newHydrationFixture(t)
	var phases []Phase
	controller := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{
		After: func(_ context.Context, phase Phase, _ Result) error {
			phases = append(phases, phase)
			return nil
		},
	})

	result, err := controller.Hydrate(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("Hydrate() error = %v", err)
	}
	if result.Materialization.Mode != domain.MaterializationCreated || result.Materialization.OwnershipMarker != fixture.request.Token {
		t.Fatalf("materialization = %#v", result.Materialization)
	}
	if _, err := os.Lstat(fixture.request.FinalRoot); err != nil {
		t.Fatalf("final root was not committed: %v", err)
	}
	if _, err := os.Lstat(fixture.request.StageRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage root = %v, want absent", err)
	}
	if !strings.Contains(strings.Join(stringPhases(phases), ","), string(PhaseOwnershipFact)) {
		t.Fatalf("phases = %#v", phases)
	}
	removed, err := fixture.ownership.RemoveOwned(context.Background(), result.Materialization)
	if err != nil || !removed {
		t.Fatalf("RemoveOwned() = %v, %v", removed, err)
	}
}

func TestHydrateCrashCutsConvergeWithoutDuplicateExtractionOrUnsafeOverwrite(t *testing.T) {
	for _, cut := range []Phase{PhaseStageCreated, PhaseExtractComplete, PhaseRenameComplete, PhaseOwnershipFact} {
		t.Run(string(cut), func(t *testing.T) {
			fixture := newHydrationFixture(t)
			crash := errors.New("injected crash")
			first := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{
				Cut: func(phase Phase) error {
					if phase == cut {
						return crash
					}
					return nil
				},
			})
			if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
				t.Fatalf("first Hydrate() error = %v, want crash", err)
			}
			loadCount, extractCount := fixture.hauler.loadCount, fixture.hauler.extractCount
			second := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
			result, err := second.Hydrate(context.Background(), fixture.request)
			if err != nil {
				t.Fatalf("recovery Hydrate() error = %v", err)
			}
			if result.Materialization.Mode != domain.MaterializationCreated || result.Materialization.OwnershipMarker != fixture.request.Token {
				t.Fatalf("recovered materialization = %#v", result.Materialization)
			}
			if fixture.hauler.loadCount != loadCount && cut != PhaseStageCreated {
				t.Fatalf("Hauler load repeated at %s: before=%d after=%d", cut, loadCount, fixture.hauler.loadCount)
			}
			if fixture.hauler.extractCount != extractCount && cut != PhaseStageCreated {
				t.Fatalf("Hauler extract repeated at %s: before=%d after=%d", cut, extractCount, fixture.hauler.extractCount)
			}
			if _, err := os.Lstat(fixture.request.FinalRoot); err != nil {
				t.Fatalf("recovered final root: %v", err)
			}
		})
	}
}

func TestHydrateCrashDuringHydrationMarkerPersistence(t *testing.T) {
	for _, test := range []struct {
		name           string
		alterPartial   bool
		addFinal       bool
		wantRecoveryOK bool
	}{
		{name: "owned durable partial completes", wantRecoveryOK: true},
		{name: "unexplained partial is preserved", alterPartial: true},
		{name: "final and partial are both preserved", addFinal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHydrationFixture(t)
			extractor := &countingArchiveExtractor{delegate: archive.NewTarZstd()}
			crash := errors.New("crash after hydration marker prepare")
			first := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{
				Cut: func(phase Phase) error {
					if phase == PhaseHydrationMarkerPrepared {
						return crash
					}
					return nil
				},
			})
			if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
				t.Fatalf("first Hydrate() error = %v, want crash", err)
			}

			markerPath := filepath.Join(fixture.request.StageRoot, "root", ".camp", "runtime", "hydration.json")
			if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final marker after prepare crash = %v, want absent", err)
			}
			partialPath := markerPath + ".partial"
			partial, err := os.ReadFile(partialPath)
			if err != nil {
				t.Fatalf("read durable marker partial: %v", err)
			}
			if test.alterPartial {
				partial = []byte("unexplained")
				if err := os.WriteFile(partialPath, partial, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.addFinal {
				if err := os.WriteFile(markerPath, partial, 0o600); err != nil {
					t.Fatal(err)
				}
			}

			second := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{})
			result, err := second.Hydrate(context.Background(), fixture.request)
			if test.wantRecoveryOK {
				if err != nil {
					t.Fatalf("recovery Hydrate() error = %v", err)
				}
				if result.Materialization.Mode != domain.MaterializationCreated || result.Materialization.OwnershipMarker != fixture.request.Token {
					t.Fatalf("recovered materialization = %#v", result.Materialization)
				}
				if err := second.validateHydrationMarker(fixture.request.FinalRoot, fixture.request); err != nil {
					t.Fatalf("recovered hydration marker: %v", err)
				}
				if _, err := os.Lstat(filepath.Join(fixture.request.FinalRoot, ".camp", "runtime", "hydration.json.partial")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("marker partial after recovery = %v, want absent", err)
				}
			} else {
				if !errors.Is(err, ErrUnsafeMaterialization) {
					t.Fatalf("recovery Hydrate() error = %v, want ErrUnsafeMaterialization", err)
				}
				if _, err := os.Lstat(markerPath); test.addFinal != (err == nil) {
					t.Fatalf("final marker presence after unsafe replay = %v, want present %v", err, test.addFinal)
				}
				preserved, readErr := os.ReadFile(partialPath)
				if readErr != nil || !bytes.Equal(preserved, partial) {
					t.Fatalf("unexplained marker partial = %q, %v; want preserved", preserved, readErr)
				}
			}
			if extractor.extractCount != 1 {
				t.Fatalf("archive extraction count = %d, want 1", extractor.extractCount)
			}
		})
	}
}

func TestHydrateCrashAfterExtractionPublishBeforeMarkerPartialRecovers(t *testing.T) {
	fixture := newHydrationFixture(t)
	extractor := &countingArchiveExtractor{delegate: archive.NewTarZstd()}
	crash := errors.New("crash after extraction publish")
	first := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{Cut: func(phase Phase) error {
		if phase == PhaseHydrationRootPublished {
			return crash
		}
		return nil
	}})
	if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
		t.Fatalf("first Hydrate() error = %v, want crash", err)
	}
	rootStage := filepath.Join(fixture.request.StageRoot, "root")
	if _, err := os.Stat(rootStage); err != nil {
		t.Fatalf("published extraction root: %v", err)
	}
	for _, name := range []string{"hydration.json", "hydration.json.partial"} {
		if _, err := os.Lstat(filepath.Join(rootStage, ".camp", "runtime", name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s before recovery = %v, want absent", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(rootStage, ".camp-extract-owner")); err != nil {
		t.Fatalf("published extraction provenance: %v", err)
	}

	second := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{})
	result, err := second.Hydrate(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("recovery Hydrate() error = %v", err)
	}
	if result.Materialization.Mode != domain.MaterializationCreated {
		t.Fatalf("recovered materialization = %#v", result.Materialization)
	}
	if extractor.extractCount != 1 {
		t.Fatalf("archive extraction count = %d, want 1", extractor.extractCount)
	}
	if _, err := os.Lstat(filepath.Join(fixture.request.FinalRoot, ".camp-extract-owner")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("extraction provenance after marker durability = %v, want absent", err)
	}
}

func TestHydrateRejectsPublishedRootWithoutExactProvenance(t *testing.T) {
	for _, mutation := range []string{"missing", "altered"} {
		t.Run(mutation, func(t *testing.T) {
			fixture := newHydrationFixture(t)
			extractor := &countingArchiveExtractor{delegate: archive.NewTarZstd()}
			crash := errors.New("crash after extraction publish")
			first := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{Cut: func(phase Phase) error {
				if phase == PhaseHydrationRootPublished {
					return crash
				}
				return nil
			}})
			if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
				t.Fatalf("first Hydrate() error = %v, want crash", err)
			}
			rootStage := filepath.Join(fixture.request.StageRoot, "root")
			provenancePath := filepath.Join(rootStage, ".camp-extract-owner")
			if mutation == "missing" {
				if err := os.Remove(provenancePath); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(provenancePath, []byte("altered"), 0o600); err != nil {
				t.Fatal(err)
			}
			second := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{})
			if _, err := second.Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
				t.Fatalf("recovery Hydrate() error = %v, want ErrUnsafeMaterialization", err)
			}
			if extractor.extractCount != 1 {
				t.Fatalf("archive extraction count = %d, want 1", extractor.extractCount)
			}
			if _, err := os.Lstat(filepath.Join(rootStage, ".camp", "runtime", "hydration.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("hydration marker after unsafe provenance = %v, want absent", err)
			}
		})
	}
}

func TestHydrateRecoversInterruptedRequestBoundExtractionPartial(t *testing.T) {
	fixture := newHydrationFixture(t)
	cut := errors.New("cut before extraction")
	first := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{Cut: func(phase Phase) error {
		if phase == PhaseGenerationLoaded {
			return cut
		}
		return nil
	}})
	if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, cut) {
		t.Fatalf("first Hydrate() error = %v, want cut", err)
	}
	partialRoot := filepath.Join(fixture.request.StageRoot, "root.partial")
	if err := os.Mkdir(partialRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	fd, stat, err := openPinnedDirectory(partialRoot)
	if err != nil {
		t.Fatal(err)
	}
	os.NewFile(uintptr(fd), partialRoot).Close()
	if err := os.WriteFile(filepath.Join(partialRoot, extractionOwnerName), extractionProvenanceBytes(stat, fixture.request), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partialRoot, "interrupted"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	extractor := &countingArchiveExtractor{delegate: archive.NewTarZstd()}
	second := NewController(fixture.store, fixture.hauler, extractor, fixture.ownership, Hooks{})
	if _, err := second.Hydrate(context.Background(), fixture.request); err != nil {
		t.Fatalf("recovery Hydrate() error = %v", err)
	}
	if extractor.extractCount != 1 {
		t.Fatalf("archive extraction count = %d, want 1", extractor.extractCount)
	}
	if _, err := os.Lstat(partialRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted extraction partial = %v, want absent", err)
	}
}

func TestHydrateRejectsMarkerParentAndPartialSubstitution(t *testing.T) {
	for _, target := range []string{"parent", "partial"} {
		t.Run(target, func(t *testing.T) {
			fixture := newHydrationFixture(t)
			markerPath := filepath.Join(fixture.request.StageRoot, "root", ".camp", "runtime", "hydration.json")
			partialPath := markerPath + ".partial"
			controller := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{Cut: func(phase Phase) error {
				if phase != PhaseHydrationMarkerPrepared {
					return nil
				}
				if target == "parent" {
					runtimePath := filepath.Dir(markerPath)
					if err := os.Rename(runtimePath, runtimePath+".owned"); err != nil {
						return err
					}
					if err := os.Mkdir(runtimePath, 0o700); err != nil {
						return err
					}
				} else if err := os.Remove(partialPath); err != nil {
					return err
				}
				return os.WriteFile(partialPath, []byte("replacement"), 0o600)
			}})
			if _, err := controller.Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
				t.Fatalf("Hydrate() error = %v, want ErrUnsafeMaterialization", err)
			}
			if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("final marker after %s substitution = %v, want absent", target, err)
			}
			body, err := os.ReadFile(partialPath)
			if err != nil || string(body) != "replacement" {
				t.Fatalf("replacement partial after %s substitution = %q, %v", target, body, err)
			}
		})
	}
}

func TestHydrateCrashBeforeOwnershipFactConverges(t *testing.T) {
	t.Parallel()
	fixture := newHydrationFixture(t)
	crash := errors.New("before ownership fact")
	first := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{
		Before: func(_ context.Context, phase Phase, _ Request) error {
			if phase == PhaseOwnershipFact {
				return crash
			}
			return nil
		},
	})
	if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
		t.Fatalf("first Hydrate() error = %v, want crash", err)
	}
	if _, err := os.Stat(fixture.request.FinalRoot); err != nil {
		t.Fatalf("final root after pre-ownership cut: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.request.FinalRoot, ".camp", "runtime", "ownership.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ownership marker after pre-ownership cut = %v, want absent", err)
	}
	second := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	result, err := second.Hydrate(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("recovery Hydrate() error = %v", err)
	}
	if result.Materialization.Mode != domain.MaterializationCreated || result.Materialization.OwnershipMarker != fixture.request.Token {
		t.Fatalf("recovered materialization = %#v", result.Materialization)
	}
}

func TestHydrateRejectsUnexplainedFinalStageAndOutOfRootPaths(t *testing.T) {
	fixture := newHydrationFixture(t)
	if err := os.MkdirAll(fixture.request.FinalRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("unexplained final error = %v, want ErrUnsafeMaterialization", err)
	}

	fixture = newHydrationFixture(t)
	if err := os.MkdirAll(fixture.request.StageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("unexplained stage error = %v, want ErrUnsafeMaterialization", err)
	}

	fixture = newHydrationFixture(t)
	fixture.request.FinalRoot = filepath.Join(t.TempDir(), "outside")
	if _, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("outside final error = %v, want ErrUnsafeMaterialization", err)
	}
}

func TestControllerWithHooksAddsObserverWithoutMutatingBaseController(t *testing.T) {
	fixture := newHydrationFixture(t)
	var observed []Phase
	controller := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	withHooks := controller.WithHooks(Hooks{Before: func(_ context.Context, phase Phase, _ Request) error {
		observed = append(observed, phase)
		return nil
	}})
	if _, err := withHooks.Hydrate(context.Background(), fixture.request); err != nil {
		t.Fatalf("WithHooks().Hydrate() error = %v", err)
	}
	if len(observed) == 0 || observed[0] != PhaseStageCreated {
		t.Fatalf("observed phases = %#v", observed)
	}
}

func TestHydrateRejectsUnverifiedOrMismatchedGenerationMetadata(t *testing.T) {
	for name, mutate := range map[string]func(*Request){
		"unverified":       func(request *Request) { request.Metadata.Verified.RemoteBytesVerified = false },
		"wrong object key": func(request *Request) { request.Metadata.ObjectKey = "brain/generations/other.tar.zst" },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newHydrationFixture(t)
			mutate(&fixture.request)
			_, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request)
			if !errors.Is(err, ErrHydrationIntegrity) {
				t.Fatalf("Hydrate() error = %v, want ErrHydrationIntegrity", err)
			}
		})
	}
}

func TestHydrateRejectsSymlinkAndChangedFinalIdentity(t *testing.T) {
	fixture := newHydrationFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(fixture.request.FinalRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.request.FinalRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("final symlink error = %v, want ErrUnsafeMaterialization", err)
	}

	fixture = newHydrationFixture(t)
	if err := os.Symlink(outside, fixture.request.StageRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("stage symlink error = %v, want ErrUnsafeMaterialization", err)
	}

	fixture = newHydrationFixture(t)
	controller := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	if _, err := controller.Hydrate(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(fixture.request.FinalRoot, ".camp", "runtime", "hydration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.request.FinalRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.request.FinalRoot, ".camp", "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.request.FinalRoot, ".camp", "runtime", "hydration.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("changed final identity error = %v, want ErrUnsafeMaterialization", err)
	}

	fixture = newHydrationFixture(t)
	controller = NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
	if _, err := controller.Hydrate(context.Background(), fixture.request); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(fixture.request.FinalRoot, ".camp", "runtime", "hydration.json")
	marker, err = os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	outsideMarker := filepath.Join(outside, "hydration.json")
	if err := os.WriteFile(outsideMarker, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideMarker, markerPath); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("hydration marker symlink error = %v, want ErrUnsafeMaterialization", err)
	}
}

func TestHydrateRejectsNonCanonicalOrWeakHydrationMarkers(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, path string, body []byte)
	}{
		{name: "reordered", mutate: func(t *testing.T, path string, body []byte) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			reordered, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(reordered, body) {
				t.Fatal("test marker did not reorder")
			}
			if err := os.WriteFile(path, reordered, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown field", mutate: func(t *testing.T, path string, body []byte) {
			body = append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"unknown":true}`)...)
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "trailing bytes", mutate: func(t *testing.T, path string, body []byte) {
			if err := os.WriteFile(path, append(body, '\n'), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "weak mode", mutate: func(t *testing.T, path string, _ []byte) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "special mode bits", mutate: func(t *testing.T, path string, _ []byte) {
			if err := os.Chmod(path, 0o600|os.ModeSetuid); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newHydrationFixture(t)
			controller := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{})
			if _, err := controller.Hydrate(context.Background(), fixture.request); err != nil {
				t.Fatal(err)
			}
			markerPath := filepath.Join(fixture.request.FinalRoot, ".camp", "runtime", "hydration.json")
			body, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, markerPath, body)
			if _, err := controller.Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
				t.Fatalf("Hydrate() error = %v, want ErrUnsafeMaterialization", err)
			}
		})
	}
}

func TestRemoveExactEntryPreservesReplacement(t *testing.T) {
	root := t.TempDir()
	const name = "hydration.json.partial"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootFD, _, err := openPinnedDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.NewFile(uintptr(rootFD), root).Close()
	owned, err := validateEntryIdentityAt(rootFD, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeExactEntryAt(rootFD, name, owned); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("removeExactEntryAt() error = %v, want ErrUnsafeMaterialization", err)
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "replacement" {
		t.Fatalf("replacement after cleanup = %q, %v", body, err)
	}
}

func TestValidateExactMarkerRejectsNameReplacementAfterRead(t *testing.T) {
	root := t.TempDir()
	const name = "hydration.json"
	want := []byte(`{"exact":true}`)
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	rootFD, _, err := openPinnedDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer os.NewFile(uintptr(rootFD), root).Close()
	_, err = validateExactMarkerAtWithHook(rootFD, name, want, func() error {
		if err := os.Remove(path); err != nil {
			return err
		}
		return os.WriteFile(path, want, 0o600)
	})
	if !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("validateExactMarkerAtWithHook() error = %v, want ErrUnsafeMaterialization", err)
	}
}

func TestHydrationCleanupRejectsSubstitutedChildBeforeRecursion(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	rootFD, _, err := openPinnedDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rootFD)
	var named unix.Stat_t
	if err := unix.Fstatat(rootFD, "child", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		t.Fatal(err)
	}
	fd, err := openHydrationCleanupChildAtWithHook(rootFD, "child", named, func() error {
		if err := os.Rename(child, child+".owned"); err != nil {
			return err
		}
		if err := os.Mkdir(child, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(child, "replacement"), []byte("keep"), 0o600)
	})
	if fd >= 0 {
		unix.Close(fd)
	}
	if !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("openHydrationCleanupChildAtWithHook() error = %v, want ErrUnsafeMaterialization", err)
	}
	if body, err := os.ReadFile(filepath.Join(child, "replacement")); err != nil || string(body) != "keep" {
		t.Fatalf("replacement child = %q, %v; want preserved", body, err)
	}
}

func TestHydrationCleanupDoesNotCrossBindMount(t *testing.T) {
	root := t.TempDir()
	source := t.TempDir()
	mounted := filepath.Join(root, "mounted")
	if err := os.Mkdir(mounted, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "canary"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, mounted, "", unix.MS_BIND, ""); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) {
			t.Skipf("bind mounts unavailable: %v", err)
		}
		t.Fatal(err)
	}
	defer unix.Unmount(mounted, unix.MNT_DETACH)
	rootFD, _, err := openPinnedDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	err = removeDirectoryContentsAt(rootFD)
	unix.Close(rootFD)
	if !errors.Is(err, unix.EXDEV) {
		t.Fatalf("removeDirectoryContentsAt() error = %v, want EXDEV", err)
	}
	if body, err := os.ReadFile(filepath.Join(mounted, "canary")); err != nil || string(body) != "keep" {
		t.Fatalf("mounted canary = %q, %v; want preserved", body, err)
	}
}

func TestHydrateRejectsReplacedStageIdentityDuringRecovery(t *testing.T) {
	fixture := newHydrationFixture(t)
	crash := errors.New("cut")
	first := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{Cut: func(phase Phase) error {
		if phase == PhaseStageCreated {
			return crash
		}
		return nil
	}})
	if _, err := first.Hydrate(context.Background(), fixture.request); !errors.Is(err, crash) {
		t.Fatalf("first Hydrate() error = %v, want cut", err)
	}
	statePath := filepath.Join(fixture.request.StageRoot, ".camp-stage.json")
	state, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.request.StageRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.request.StageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, state, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewController(fixture.store, fixture.hauler, archive.NewTarZstd(), fixture.ownership, Hooks{}).Hydrate(context.Background(), fixture.request); !errors.Is(err, ErrUnsafeMaterialization) {
		t.Fatalf("replaced stage error = %v, want ErrUnsafeMaterialization", err)
	}
}

func TestSameDirectoryIdentityRejectsReusedInodeWithDifferentBirthTime(t *testing.T) {
	if sameDirectoryIdentity(7, 11, 13, 7, 11, 17) {
		t.Fatal("directory identity accepted a reused device and inode with a different birth time")
	}
}

func TestRenameNoReplacePreservesExistingDestination(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	oldPath := filepath.Join(root, "old")
	newPath := filepath.Join(root, "new")
	if err := os.Mkdir(oldPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newPath, "keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(oldPath, newPath); !errors.Is(err, os.ErrExist) {
		t.Fatalf("renameNoReplace() error = %v, want os.ErrExist", err)
	}
	if _, err := os.Stat(filepath.Join(newPath, "keep")); err != nil {
		t.Fatalf("existing destination changed: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("source disappeared: %v", err)
	}
}

type hydrationFixture struct {
	store     *hydrationStore
	hauler    *hydrationHauler
	ownership *capsule.Ownership
	request   Request
}

func newHydrationFixture(t *testing.T) hydrationFixture {
	t.Helper()
	root := t.TempDir()
	dataHome := filepath.Join(root, "home", ".local", "share")
	if err := os.MkdirAll(dataHome, 0o700); err != nil {
		t.Fatal(err)
	}
	ownership, err := capsule.NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(source, ".camp", "runtime", "target"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".camp", "capsule.yaml"), []byte("capsule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("hydration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "inner.tar.zst")
	if _, err := archive.NewTarZstd().Create(context.Background(), source, inner); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(inner)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	digestText := fmtHash(digest[:])
	store := &hydrationStore{body: body, meta: ports.ObjectMeta{Key: "brain/generations/42-" + digestText + ".tar.zst", Size: int64(len(body)), SHA256: digestText, Revision: "r1"}}
	hauler := &hydrationHauler{inner: inner}
	sessionRoot := filepath.Join(root, "sessions", "session-a")
	if err := os.MkdirAll(sessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	finalRoot := filepath.Join(dataHome, "camp", "materializations", "session-a")
	return hydrationFixture{
		store: store, hauler: hauler, ownership: ownership,
		request: Request{
			SessionID: "session-a", Capsule: "brain", Generation: domain.GenerationRef{Generation: 42, ArchiveSHA256: digestText},
			Metadata:    domain.GenerationMetadata{SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: domain.GenerationRef{Generation: 42, ArchiveSHA256: digestText}, ObjectKey: store.meta.Key, MetadataKey: "brain/generations/42-" + digestText + ".json", Size: int64(len(body)), CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source", Verified: domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true}},
			SessionRoot: sessionRoot, StageRoot: filepath.Join(sessionRoot, "materialization-stage"), FinalRoot: finalRoot,
			HaulPath: filepath.Join(sessionRoot, "generation.tar.zst"), Token: strings.Repeat("a", 64),
		},
	}
}

func stringPhases(phases []Phase) []string {
	result := make([]string, len(phases))
	for i, phase := range phases {
		result[i] = string(phase)
	}
	return result
}

func fmtHash(value []byte) string {
	const hex = "0123456789abcdef"
	result := make([]byte, len(value)*2)
	for i, item := range value {
		result[i*2] = hex[item>>4]
		result[i*2+1] = hex[item&15]
	}
	return string(result)
}

type hydrationStore struct {
	body []byte
	meta ports.ObjectMeta
}

func (s *hydrationStore) Get(context.Context, string) (io.ReadCloser, ports.ObjectMeta, error) {
	return io.NopCloser(bytes.NewReader(s.body)), s.meta, nil
}
func (s *hydrationStore) Head(context.Context, string) (ports.ObjectMeta, error) { return s.meta, nil }
func (s *hydrationStore) PutImmutable(context.Context, string, ports.RestartableSource, string, int64) (ports.ObjectMeta, error) {
	return ports.ObjectMeta{}, errors.New("not used")
}
func (s *hydrationStore) PutConditional(context.Context, string, []byte, ports.WriteCondition) (ports.ObjectMeta, error) {
	return ports.ObjectMeta{}, errors.New("not used")
}
func (s *hydrationStore) DeleteConditional(context.Context, string, ports.Revision) error {
	return errors.New("not used")
}
func (s *hydrationStore) List(context.Context, string, string) ([]ports.ObjectMeta, string, error) {
	return nil, "", errors.New("not used")
}

type hydrationHauler struct {
	inner        string
	loadCount    int
	extractCount int
}

type countingArchiveExtractor struct {
	delegate     ArchiveExtractor
	extractCount int
}

func (e *countingArchiveExtractor) Extract(ctx context.Context, source, destination string) error {
	e.extractCount++
	return e.delegate.Extract(ctx, source, destination)
}

func (e *countingArchiveExtractor) ExtractWithProvenance(ctx context.Context, source, destination string, provenance func(archive.ExtractionRoot) ([]byte, error)) error {
	e.extractCount++
	extractor, ok := e.delegate.(ProvenanceArchiveExtractor)
	if !ok {
		return errors.New("delegate does not support provenance")
	}
	return extractor.ExtractWithProvenance(ctx, source, destination, provenance)
}

func (h *hydrationHauler) Load(context.Context, string, []string) (ports.Result, error) {
	h.loadCount++
	return ports.Result{}, nil
}

func (h *hydrationHauler) Extract(_ context.Context, _ string, _ string, output string) (ports.Result, error) {
	h.extractCount++
	if err := os.MkdirAll(output, 0o700); err != nil {
		return ports.Result{}, err
	}
	body, err := os.ReadFile(h.inner)
	if err != nil {
		return ports.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(output, "brain.tar.zst"), body, 0o600); err != nil {
		return ports.Result{}, err
	}
	return ports.Result{}, nil
}

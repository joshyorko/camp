package hydration

import (
	"bytes"
	"context"
	"crypto/sha256"
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

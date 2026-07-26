package checkpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/domain"
)

type fakeArchiver struct {
	root       string
	manifestOK bool
	called     bool
}

func (a *fakeArchiver) Create(_ context.Context, root, destination string) (archiveadapter.ArchiveInfo, error) {
	a.called = true
	a.root = root
	_, manifestErr := os.Stat(filepath.Join(root, ".camp", "hauler-manifest.yaml"))
	_, inventoryErr := os.Stat(filepath.Join(root, ".camp", "images.json"))
	a.manifestOK = manifestErr == nil && inventoryErr == nil
	if err := os.WriteFile(destination, []byte("inner"), 0o600); err != nil {
		return archiveadapter.ArchiveInfo{}, err
	}
	return archiveadapter.ArchiveInfo{Path: destination, SHA256: "inner-sha", Size: 5}, nil
}

type fakeAssembler struct{ archiveCalled *bool }

func (a fakeAssembler) Assemble(_ context.Context, _, _, output string) (hauler.GenerationArtifact, error) {
	if !*a.archiveCalled {
		panic("generation assembly ran before root archive")
	}
	if err := os.WriteFile(output, []byte("haul"), 0o600); err != nil {
		return hauler.GenerationArtifact{}, err
	}
	return hauler.GenerationArtifact{Path: output, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 4, Validated: true}, nil
}

func TestBuilderIsOnlyOrderedPathToValidatedGenerationMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	archiver := &fakeArchiver{}
	builder := NewBuilder(archiver, fakeAssembler{archiveCalled: &archiver.called})
	parent := domain.GenerationRef{Generation: 42, ArchiveSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	result, err := builder.Build(context.Background(), BuildRequest{
		Capsule: "second-brain", Root: root, Inventory: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(100, 0), Images: []domain.Image{}},
		Lineage: domain.Lineage{Branch: "main"}, Generation: 43, Parent: &parent, SessionID: "session-a", CreatedAt: time.Unix(101, 0),
		Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if !archiver.manifestOK || !result.Artifact.Validated || !result.Metadata.Verified.LocalHaulLoadable || result.Metadata.Generation.Generation != 43 || result.Metadata.Generation.ArchiveSHA256 != result.Artifact.SHA256 || result.Metadata.Parent == nil || *result.Metadata.Parent != parent {
		t.Fatalf("result = %#v archiver=%#v", result, archiver)
	}
	if result.Metadata.ObjectKey == "" || result.Metadata.MetadataKey == "" {
		t.Fatalf("canonical generation keys missing: %#v", result.Metadata)
	}
}

func TestBuilderRejectsSymlinkedCheckpointDirectoriesBeforeMutation(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"root", ".camp", ".camp/build"} {
		target := target
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			externalRoot := filepath.Join(base, "external")
			externalCamp := filepath.Join(externalRoot, ".camp")
			externalBuild := filepath.Join(externalCamp, "build")
			if err := os.MkdirAll(externalBuild, 0o700); err != nil {
				t.Fatal(err)
			}
			sentinels := map[string]string{
				filepath.Join(externalCamp, "images.json"):                   "external inventory\n",
				filepath.Join(externalCamp, "hauler-manifest.yaml"):          "external manifest\n",
				filepath.Join(externalBuild, "second-brain.tar.zst"):         "external inner\n",
				filepath.Join(externalBuild, "second-brain-haul-43.tar.zst"): "external haul\n",
			}
			for path, body := range sentinels {
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			root := filepath.Join(base, "root")
			switch target {
			case "root":
				if err := os.Symlink(externalRoot, root); err != nil {
					t.Fatal(err)
				}
			case ".camp":
				if err := os.Mkdir(root, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalCamp, filepath.Join(root, ".camp")); err != nil {
					t.Fatal(err)
				}
			case ".camp/build":
				if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(externalBuild, filepath.Join(root, ".camp", "build")); err != nil {
					t.Fatal(err)
				}
			}

			archiver := &fakeArchiver{}
			builder := NewBuilder(archiver, fakeAssembler{archiveCalled: &archiver.called})
			_, err := builder.Build(context.Background(), BuildRequest{
				Capsule: "second-brain", Root: root,
				Inventory: domain.ImageInventory{GeneratedAt: time.Unix(100, 0), Images: []domain.Image{}},
				Lineage:   domain.Lineage{Branch: "main"}, Generation: 43,
				SessionID: "session-a", CreatedAt: time.Unix(101, 0),
			})
			if err == nil {
				t.Fatal("Build() accepted a symlinked checkpoint directory")
			}
			if archiver.called {
				t.Fatal("Build() called the archiver after rejecting a symlinked checkpoint directory")
			}
			for path, want := range sentinels {
				body, readErr := os.ReadFile(path)
				if readErr != nil || string(body) != want {
					t.Fatalf("external sentinel %q = %q, %v; want %q", path, body, readErr, want)
				}
			}
		})
	}
}

func TestCommitDocumentsRecoversCrashBetweenPairRenamesWithoutPublishingMixedCut(t *testing.T) {
	t.Parallel()
	campDirectory := filepath.Join(t.TempDir(), ".camp")
	if err := commitDocuments(campDirectory, []byte("old inventory\n"), []byte("old manifest\n")); err != nil {
		t.Fatal(err)
	}
	cut := errors.New("cut after first rename")
	err := commitDocumentsWithFault(campDirectory, []byte("new inventory\n"), []byte("new manifest\n"), func(point string) error {
		if point == "after-first-rename" {
			return cut
		}
		return nil
	})
	if !errors.Is(err, cut) {
		t.Fatalf("commitDocumentsWithFault() error = %v, want cut", err)
	}
	if _, err := os.Stat(filepath.Join(campDirectory, ".content-transaction.json")); err != nil {
		t.Fatalf("durable transaction marker missing after cut: %v", err)
	}
	if err := commitDocuments(campDirectory, []byte("new inventory\n"), []byte("new manifest\n")); err != nil {
		t.Fatalf("commitDocuments(recovery) error = %v", err)
	}
	for name, want := range map[string]string{"images.json": "new inventory\n", "hauler-manifest.yaml": "new manifest\n"} {
		body, err := os.ReadFile(filepath.Join(campDirectory, name))
		if err != nil || string(body) != want {
			t.Fatalf("%s = %q, %v; want %q", name, body, err, want)
		}
	}
	if _, err := os.Stat(filepath.Join(campDirectory, ".content-transaction.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transaction marker remains after recovery: %v", err)
	}
}

func TestCommitDocumentsRejectsCorruptPendingPairAndCleansKnownLegacyPartials(t *testing.T) {
	t.Parallel()
	campDirectory := filepath.Join(t.TempDir(), ".camp")
	if err := os.MkdirAll(campDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(campDirectory, ".images.json.partial"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitDocuments(campDirectory, []byte("inventory\n"), []byte("manifest\n")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(campDirectory, ".images.json.partial")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("known legacy partial remains: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campDirectory, ".content-transaction.json"), []byte(`{"id":"bad","documents":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := commitDocuments(campDirectory, []byte("next inventory\n"), []byte("next manifest\n")); err == nil {
		t.Fatal("commitDocuments() accepted corrupt pending transaction")
	}
	body, err := os.ReadFile(filepath.Join(campDirectory, "images.json"))
	if err != nil || string(body) != "inventory\n" {
		t.Fatalf("corrupt transaction changed committed content: %q, %v", body, err)
	}
}

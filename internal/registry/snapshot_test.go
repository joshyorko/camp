package registry

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type cutBarrier struct {
	overlay string
	cuts    int
}

func (b *cutBarrier) WithCut(_ context.Context, _ SnapshotRequest, seal func() error) error {
	b.cuts++
	if err := seal(); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(b.overlay, "post-cut"), []byte("next"), 0o600)
}

type staticCatalog struct {
	references []ports.RegistryReference
	err        error
}

func (c staticCatalog) List(context.Context, string) ([]ports.RegistryReference, error) {
	return append([]ports.RegistryReference(nil), c.references...), c.err
}

type failingBarrier struct{ err error }

func (b failingBarrier) WithCut(context.Context, SnapshotRequest, func() error) error { return b.err }

func (c staticCatalog) Resolve(context.Context, string, string, string) (string, error) {
	panic("not used")
}

func TestSnapshotterCleansPartialCutAndNeverReplacesExistingFinal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("outside", filepath.Join(overlay, "unsafe")); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "sealed")
	barrier := &cutBarrier{overlay: overlay}
	_, err := NewSnapshotter(barrier).Seal(context.Background(), SnapshotRequest{OverlayRoot: overlay, SnapshotRoot: destination, CatalogEndpoint: "http://127.0.0.1:5000"})
	if err == nil {
		t.Fatal("Seal() accepted a symlinked registry entry")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed cut left final snapshot: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".sealed.partial-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("failed cut left partials: %v, %#v", err, matches)
	}
	if err := os.Remove(filepath.Join(overlay, "unsafe")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = NewSnapshotter(barrier).Seal(context.Background(), SnapshotRequest{OverlayRoot: overlay, SnapshotRoot: destination, CatalogEndpoint: "http://127.0.0.1:5000"})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("Seal(existing final) error = %v, want os.ErrExist", err)
	}
	barrierErr := errors.New("barrier unavailable")
	_, err = NewSnapshotter(failingBarrier{err: barrierErr}).Seal(context.Background(), SnapshotRequest{OverlayRoot: overlay, SnapshotRoot: filepath.Join(root, "other"), CatalogEndpoint: "http://127.0.0.1:5000"})
	if !errors.Is(err, barrierErr) {
		t.Fatalf("Seal(barrier failure) error = %v", err)
	}
}

func TestSnapshotterNextCutRetainsPriorPostCutWrites(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "generation-n"), []byte("n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstBarrier := &cutBarrier{overlay: overlay}
	snapshotter := NewSnapshotter(firstBarrier)
	if _, err := snapshotter.Seal(context.Background(), SnapshotRequest{OverlayRoot: overlay, SnapshotRoot: filepath.Join(root, "sealed-n"), CatalogEndpoint: "http://127.0.0.1:5000"}); err != nil {
		t.Fatal(err)
	}
	secondBarrier := &cutBarrier{overlay: overlay}
	snapshotter = NewSnapshotter(secondBarrier)
	if _, err := snapshotter.Seal(context.Background(), SnapshotRequest{OverlayRoot: overlay, SnapshotRoot: filepath.Join(root, "sealed-n-plus-1"), CatalogEndpoint: "http://127.0.0.1:5000"}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "sealed-n-plus-1", "post-cut")); err != nil || string(body) != "next" {
		t.Fatalf("N+1 cut lost N post-cut write: %q, %v", body, err)
	}
}

func TestSnapshotterSealsOneCutAndLeavesPostCutWritesInMutableOverlay(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	sealed := filepath.Join(root, "sealed")
	if err := os.MkdirAll(filepath.Join(overlay, "docker", "registry"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlay, "docker", "registry", "before"), []byte("current"), 0o640); err != nil {
		t.Fatal(err)
	}
	references := []ports.RegistryReference{}
	barrier := &cutBarrier{overlay: overlay}
	result, err := NewSnapshotter(barrier).Seal(context.Background(), SnapshotRequest{
		OverlayRoot: overlay, SnapshotRoot: sealed, CatalogEndpoint: "http://127.0.0.1:5000",
	})
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if barrier.cuts != 1 || result.Root != sealed || !reflect.DeepEqual(result.References, references) {
		t.Fatalf("result = %#v cuts=%d", result, barrier.cuts)
	}
	if body, err := os.ReadFile(filepath.Join(sealed, "docker", "registry", "before")); err != nil || string(body) != "current" {
		t.Fatalf("sealed pre-cut bytes = %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(sealed, "post-cut")); !os.IsNotExist(err) {
		t.Fatalf("post-cut write leaked into sealed snapshot: %v", err)
	}
	if body, err := os.ReadFile(filepath.Join(overlay, "post-cut")); err != nil || string(body) != "next" {
		t.Fatalf("post-cut write was not retained in overlay: %q, %v", body, err)
	}
}

func TestSnapshotterDerivesDirectPushReferencesFromTheSealedCut(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, link := range []string{
		filepath.Join(overlay, "docker", "registry", "v2", "repositories", "manual", "direct", "_manifests", "tags", "v1", "current", "link"),
		filepath.Join(overlay, "docker", "registry", "v2", "repositories", "hauler", "camp-session-seed", "_manifests", "tags", "latest", "current", "link"),
	} {
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(link, []byte(digest+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	barrier := &cutBarrier{overlay: overlay}
	result, err := NewSnapshotter(barrier).Seal(context.Background(), SnapshotRequest{
		OverlayRoot: overlay, SnapshotRoot: filepath.Join(root, "sealed"), CatalogEndpoint: "http://127.0.0.1:5000",
	})
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	want := []ports.RegistryReference{{Repository: "manual/direct", Tag: "v1", ManifestDigest: digest}}
	if !reflect.DeepEqual(result.References, want) {
		t.Fatalf("references = %#v, want %#v", result.References, want)
	}
}

func TestInventoryFromCatalogCapturesExplicitRegistryPush(t *testing.T) {
	t.Parallel()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	inventory, err := InventoryFromCatalog("127.0.0.1:5000", []ports.RegistryReference{{
		Repository: "manual/direct", Tag: "v1", ManifestDigest: digest,
	}}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("InventoryFromCatalog() error = %v", err)
	}
	want := []domain.Image{{
		OriginalTags:           []string{"127.0.0.1:5000/manual/direct:v1"},
		CapturedReference:      "127.0.0.1:5000/manual/direct:v1",
		CapturedManifestDigest: digest,
		Source:                 domain.ImageSourceRegistry,
		CreatedAt:              time.Unix(100, 0).UTC(),
	}}
	if !reflect.DeepEqual(inventory.Images, want) {
		t.Fatalf("inventory images = %#v, want %#v", inventory.Images, want)
	}
}

func TestInventoryFromCatalogRejectsTagDigestDriftAndIsDeterministicAcrossDuplicateTags(t *testing.T) {
	t.Parallel()
	digestA := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	_, err := InventoryFromCatalog("127.0.0.1:5000", []ports.RegistryReference{
		{Repository: "manual/tool", Tag: "latest", ManifestDigest: digestA},
		{Repository: "manual/tool", Tag: "latest", ManifestDigest: digestB},
	}, time.Unix(100, 0))
	if !errors.Is(err, ErrRegistryDigestMismatch) {
		t.Fatalf("InventoryFromCatalog(drift) error = %v, want ErrRegistryDigestMismatch", err)
	}
	references := []ports.RegistryReference{
		{Repository: "z/tool", Tag: "stable", ManifestDigest: digestB},
		{Repository: "a/tool", Tag: "v1", ManifestDigest: digestA},
		{Repository: "a/tool", Tag: "v1", ManifestDigest: digestA},
	}
	first, err := InventoryFromCatalog("127.0.0.1:5000", references, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	second, err := InventoryFromCatalog("127.0.0.1:5000", []ports.RegistryReference{references[2], references[0], references[1]}, time.Unix(100, 0))
	if err != nil || !reflect.DeepEqual(first, second) || len(first.Images) != 2 {
		t.Fatalf("deterministic merge = %#v / %#v, %v", first, second, err)
	}
}

func TestInventoryFromCatalogCoalescesExplicitTagsForOneDigest(t *testing.T) {
	t.Parallel()
	digestA := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	inventory, err := InventoryFromCatalog("127.0.0.1:5000", []ports.RegistryReference{
		{Repository: "camp-acceptance", Tag: "named", ManifestDigest: digestA},
		{Repository: "camp-acceptance", Tag: "stable", ManifestDigest: digestA},
		{Repository: "manual/tool", Tag: "latest", ManifestDigest: digestB},
	}, time.Unix(100, 0).UTC())
	if err != nil {
		t.Fatalf("InventoryFromCatalog() error = %v", err)
	}
	if len(inventory.Images) != 2 || !reflect.DeepEqual(inventory.Images[0].OriginalTags, []string{"127.0.0.1:5000/camp-acceptance:named", "127.0.0.1:5000/camp-acceptance:stable"}) {
		t.Fatalf("catalog aliases = %#v", inventory.Images)
	}
	if inventory.Images[1].CapturedReference != "127.0.0.1:5000/manual/tool:latest" || !reflect.DeepEqual(inventory.Images[1].OriginalTags, []string{"127.0.0.1:5000/manual/tool:latest"}) || inventory.Images[1].CapturedManifestDigest != digestB || inventory.Images[1].Source != domain.ImageSourceRegistry {
		t.Fatalf("catalog inventory = %#v", inventory)
	}
}

func TestExcludeInternalArtifactsRemovesSeedAndHaulerFileProjection(t *testing.T) {
	t.Parallel()
	inventory := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{
		{CapturedReference: "127.0.0.1:5000/hauler/camp-session-seed:latest", Source: domain.ImageSourceRegistry},
		{CapturedReference: "127.0.0.1:5000/hauler/default.tar.zst:sha256-deadbeef", Source: domain.ImageSourceRegistry},
		{CapturedReference: "127.0.0.1:5000/camp/user:latest", Source: domain.ImageSourceRegistry},
	}}
	filtered := ExcludeInternalArtifacts(inventory)
	if len(filtered.Images) != 1 || filtered.Images[0].CapturedReference != "127.0.0.1:5000/camp/user:latest" {
		t.Fatalf("filtered inventory = %#v", filtered.Images)
	}
}

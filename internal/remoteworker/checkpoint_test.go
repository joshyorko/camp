package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/registry"
)

type fakeCheckpointRuntime struct {
	events   []string
	receipt  CheckpointReceipt
	observed bool
	errAt    string
}

func (f *fakeCheckpointRuntime) call(name string) error {
	f.events = append(f.events, name)
	if f.errAt == name {
		return errors.New(name)
	}
	return nil
}

func (f *fakeCheckpointRuntime) Verify(context.Context, Request) error {
	return f.call("verify")
}

func (f *fakeCheckpointRuntime) Observe(context.Context, Request) (CheckpointReceipt, bool, error) {
	err := f.call("observe")
	return f.receipt, f.observed, err
}

func (f *fakeCheckpointRuntime) Quiesce(context.Context, Request) (ServiceCheckpointEvidence, error) {
	return ServiceCheckpointEvidence{Token: "services-cut", Services: []domain.ServiceUnitRecord{{Name: "registry"}, {Name: "fileserver"}}}, f.call("quiesce")
}

func (f *fakeCheckpointRuntime) ReleaseBarrier(context.Context, Request, ServiceCheckpointEvidence) error {
	return f.call("release")
}

func (f *fakeCheckpointRuntime) CutRegistry(context.Context, Request, ServiceCheckpointEvidence) (registry.Snapshot, error) {
	return registry.Snapshot{Root: "/workspaces/brain/.camp/checkpoints/attempt-1/registry"}, f.call("cut")
}

func (f *fakeCheckpointRuntime) Inventory(context.Context, Request, registry.Snapshot) (domain.ImageInventory, error) {
	return domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{CapturedReference: "127.0.0.1:5000/app:v1"}}}, f.call("inventory")
}

func (f *fakeCheckpointRuntime) ArchiveRoot(context.Context, Request, registry.Snapshot) (archiveadapter.ArchiveInfo, error) {
	return archiveadapter.ArchiveInfo{Path: "/workspaces/brain/.camp/checkpoints/attempt-1/root.tar.zst", SHA256: testSHA("a"), Size: 10}, f.call("archive")
}

func (f *fakeCheckpointRuntime) BuildStore(context.Context, Request, archiveadapter.ArchiveInfo, domain.ImageInventory) (haulkit.StoreIdentity, error) {
	return haulkit.StoreIdentity{HaulerVersion: "v2.0.1", IndexSHA256: testSHA("b"), Entries: []haulkit.StoreEntry{{Reference: "hauler/brain.tar.zst:latest", Type: "file", Digest: testSHA("a"), Size: 10}}}, f.call("store")
}

func (f *fakeCheckpointRuntime) BuildKit(context.Context, Request, archiveadapter.ArchiveInfo, haulkit.StoreIdentity) (haulkit.Artifact, error) {
	return haulkit.Artifact{
		ManifestPath:   "/workspaces/brain/.camp/transfer/attempt-1/camp-hauler-kit.json",
		ManifestSHA256: testSHA("c"),
		ArchivePath:    "/workspaces/brain/.camp/transfer/attempt-1/camp-hauler-kit.tar.zst",
		SHA256:         testSHA("d"), Size: 20,
		Chunks: []haulkit.ChunkIdentity{{Index: 0, Name: "camp-hauler-kit.tar.zst.part-000000", SHA256: testSHA("d"), Size: 20}},
	}, f.call("kit")
}

func (f *fakeCheckpointRuntime) Publish(context.Context, Request, CheckpointReceipt) (CheckpointReceipt, error) {
	err := f.call("publish")
	return f.receipt, err
}

func (f *fakeCheckpointRuntime) Resume(context.Context, Request, ServiceCheckpointEvidence) error {
	return f.call("resume")
}

func TestCheckpointPreparesOneImmutableAllowListedKitInOrder(t *testing.T) {
	runtime := &fakeCheckpointRuntime{}
	request := checkpointRequest(false)
	receipt, err := checkpoint(context.Background(), request, runtime)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents := []string{"verify", "observe", "quiesce", "cut", "inventory", "release", "archive", "store", "kit", "publish", "resume"}
	if !reflect.DeepEqual(runtime.events, wantEvents) {
		t.Fatalf("events = %#v", runtime.events)
	}
	if receipt.AttemptID != "attempt-1" || receipt.Registry.Root == "" || len(receipt.Images.Images) != 1 ||
		receipt.Root.SHA256 != testSHA("a") || receipt.Store.IndexSHA256 != testSHA("b") {
		t.Fatalf("receipt = %#v", receipt)
	}
	wantAllow := []string{
		"attempt-1/camp-hauler-kit.json",
		"attempt-1/camp-hauler-kit.tar.zst.part-000000",
	}
	if !reflect.DeepEqual(receipt.AllowList, wantAllow) {
		t.Fatalf("allow list = %#v", receipt.AllowList)
	}
}

func TestCheckpointCloseKeepsExactServicesQuiesced(t *testing.T) {
	runtime := &fakeCheckpointRuntime{}
	if _, err := checkpoint(context.Background(), checkpointRequest(true), runtime); err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(runtime.events, []string{"verify", "observe", "quiesce", "cut", "inventory", "release", "archive", "store", "kit", "publish", "resume"}) {
		t.Fatal("close resumed services")
	}
	want := []string{"verify", "observe", "quiesce", "cut", "inventory", "release", "archive", "store", "kit", "publish"}
	if !reflect.DeepEqual(runtime.events, want) {
		t.Fatalf("events = %#v", runtime.events)
	}
}

func TestCheckpointFailureAfterBarrierNeverResumesOrPublishes(t *testing.T) {
	runtime := &fakeCheckpointRuntime{errAt: "store"}
	if _, err := checkpoint(context.Background(), checkpointRequest(false), runtime); err == nil {
		t.Fatal("expected error")
	}
	want := []string{"verify", "observe", "quiesce", "cut", "inventory", "release", "archive", "store"}
	if !reflect.DeepEqual(runtime.events, want) {
		t.Fatalf("events = %#v", runtime.events)
	}
}

func TestCheckpointReleasesBarrierOnCutAndInventoryFailure(t *testing.T) {
	for _, stage := range []string{"cut", "inventory"} {
		runtime := &fakeCheckpointRuntime{errAt: stage}
		if _, err := checkpoint(context.Background(), checkpointRequest(false), runtime); err == nil {
			t.Fatalf("%s failure was accepted", stage)
		}
		if runtime.events[len(runtime.events)-1] != "release" {
			t.Fatalf("%s events = %#v", stage, runtime.events)
		}
	}
}

func TestRemoteServiceStartLockBlocksUntilCheckpointCutAuthorityReleases(t *testing.T) {
	root := t.TempDir()
	first, err := lockRemoteServices(root)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan *os.File, 1)
	failed := make(chan error, 1)
	go func() {
		lock, err := lockRemoteServices(root)
		if err != nil {
			failed <- err
			return
		}
		acquired <- lock
	}()
	select {
	case lock := <-acquired:
		unlockRemoteServices(lock)
		t.Fatal("concurrent service start acquired authority before cut release")
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(50 * time.Millisecond):
	}
	unlockRemoteServices(first)
	select {
	case lock := <-acquired:
		unlockRemoteServices(lock)
	case err := <-failed:
		t.Fatal(err)
	case <-time.After(time.Second):
		t.Fatal("concurrent service start did not acquire authority after cut release")
	}
}

func TestCheckpointAdoptsPublishedAttemptWithoutConstructingAnotherKit(t *testing.T) {
	seed := &fakeCheckpointRuntime{}
	adopted, err := checkpoint(context.Background(), checkpointRequest(true), seed)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeCheckpointRuntime{receipt: adopted, observed: true}
	got, err := checkpoint(context.Background(), checkpointRequest(false), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if got.AttemptID != adopted.AttemptID {
		t.Fatalf("receipt = %#v", got)
	}
	if want := []string{"verify", "observe", "resume"}; !reflect.DeepEqual(runtime.events, want) {
		t.Fatalf("events = %#v", runtime.events)
	}
}

func TestCheckpointRejectsDirectoryWideOrArchiveAllowList(t *testing.T) {
	for _, allow := range [][]string{
		{"attempt-1"},
		{"attempt-1/"},
		{"attempt-1/camp-hauler-kit.tar.zst"},
		{"../camp-hauler-kit.json"},
		{"attempt-1/camp-hauler-kit.json", "attempt-1/other"},
	} {
		if err := validateCheckpointAllowList("attempt-1", []haulkit.ChunkIdentity{{Index: 0, Name: "camp-hauler-kit.tar.zst.part-000000", SHA256: testSHA("d"), Size: 20}}, allow); err == nil {
			t.Fatalf("accepted allow list %#v", allow)
		}
	}
}

func TestCheckpointExportPublishesOnlyImmutableManifestAndChunks(t *testing.T) {
	workspace, receipt := checkpointExportFixture(t)
	if err := publishCheckpointExport(workspace, receipt); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(workspace, ".camp", "transfer", "export")
	entries, err := os.ReadDir(filepath.Join(root, receipt.AttemptID))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	want := []string{"camp-hauler-kit.json", "camp-hauler-kit.tar.zst.part-000000"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("export entries = %#v", names)
	}
	if _, err := os.Lstat(filepath.Join(root, receipt.AttemptID, "camp-hauler-kit.tar.zst")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("complete archive was exposed: %v", err)
	}
	if err := validateCheckpointExport(workspace, receipt); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointExportRejectsExtraLinkReplacementAndHardlinkDrift(t *testing.T) {
	for _, mutate := range []func(*testing.T, string, CheckpointReceipt){
		func(t *testing.T, root string, receipt CheckpointReceipt) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, receipt.AttemptID, "extra"), []byte("extra"), 0o400); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, root string, receipt CheckpointReceipt) {
			t.Helper()
			path := filepath.Join(root, receipt.AttemptID, receipt.Kit.Chunks[0].Name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(receipt.Kit.ManifestPath, path); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, root string, receipt CheckpointReceipt) {
			t.Helper()
			path := filepath.Join(root, receipt.AttemptID, receipt.Kit.Chunks[0].Name)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("replacement"), 0o400); err != nil {
				t.Fatal(err)
			}
		},
		func(t *testing.T, root string, receipt CheckpointReceipt) {
			t.Helper()
			path := filepath.Join(root, receipt.AttemptID, receipt.Kit.Chunks[0].Name)
			if err := os.Link(path, filepath.Join(t.TempDir(), "alias")); err != nil {
				t.Fatal(err)
			}
		},
	} {
		workspace, receipt := checkpointExportFixture(t)
		if err := publishCheckpointExport(workspace, receipt); err != nil {
			t.Fatal(err)
		}
		root := filepath.Join(workspace, ".camp", "transfer", "export")
		mutate(t, root, receipt)
		if err := validateCheckpointExport(workspace, receipt); err == nil {
			t.Fatal("accepted drifted checkpoint export")
		}
	}
}

func TestCheckpointExportUnknownOutcomeAdoptsExactAttemptAndRejectsSecondAttempt(t *testing.T) {
	workspace, receipt := checkpointExportFixture(t)
	if err := publishCheckpointExport(workspace, receipt); err != nil {
		t.Fatal(err)
	}
	if err := publishCheckpointExport(workspace, receipt); err != nil {
		t.Fatalf("exact replay was not adopted: %v", err)
	}
	other := receipt
	other.AttemptID = "attempt-2"
	if err := publishCheckpointExport(workspace, other); err == nil {
		t.Fatal("published a second export beside the durable attempt")
	}
}

func TestCheckpointExportObserverRejectsNamedReplacementDuringRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "chunk")
	body := []byte("chunk")
	if err := os.WriteFile(path, body, 0o400); err != nil {
		t.Fatal(err)
	}
	err := func() error {
		_, err := observeCheckpointFileWithHook(path, digestCheckpointBytes(body), int64(len(body)), func() {
			replacement := filepath.Join(root, "replacement")
			if writeErr := os.WriteFile(replacement, body, 0o400); writeErr != nil {
				t.Fatal(writeErr)
			}
			if renameErr := os.Rename(replacement, path); renameErr != nil {
				t.Fatal(renameErr)
			}
		})
		return err
	}()
	if err == nil {
		t.Fatal("accepted a replaced named export after reading the original inode")
	}
}

func checkpointExportFixture(t *testing.T) (string, CheckpointReceipt) {
	t.Helper()
	workspace := t.TempDir()
	source := t.TempDir()
	chunks := filepath.Join(source, "chunks")
	if err := os.Mkdir(chunks, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestBody := []byte("manifest")
	chunkBody := []byte("chunk")
	archiveBody := []byte("archive")
	manifestPath := filepath.Join(source, "camp-hauler-kit.json")
	chunkName := "camp-hauler-kit.tar.zst.part-000000"
	chunkPath := filepath.Join(chunks, chunkName)
	archivePath := filepath.Join(source, "camp-hauler-kit.tar.zst")
	for path, body := range map[string][]byte{manifestPath: manifestBody, chunkPath: chunkBody, archivePath: archiveBody} {
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return workspace, CheckpointReceipt{
		SchemaVersion: ProtocolSchemaVersion, Status: "prepared", SessionID: "session-1", AttemptID: "attempt-1",
		Kit: haulkit.Artifact{
			ManifestPath: manifestPath, ManifestSHA256: digestCheckpointBytes(manifestBody),
			ArchivePath: archivePath, SHA256: digestCheckpointBytes(archiveBody), Size: int64(len(archiveBody)),
			Chunks: []haulkit.ChunkIdentity{{Index: 0, Name: chunkName, SHA256: digestCheckpointBytes(chunkBody), Size: int64(len(chunkBody))}},
		},
		AllowList: []string{"attempt-1/camp-hauler-kit.json", "attempt-1/" + chunkName},
	}
}

func digestCheckpointBytes(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func checkpointRequest(close bool) Request {
	request := validRequest()
	request.Operation = OperationCheckpoint
	request.Checkpoint = &CheckpointRequest{
		AttemptID: "attempt-1", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Generation: 1, Close: close,
	}
	return request
}

func testSHA(value string) string {
	result := ""
	for len(result) < 64 {
		result += value
	}
	return result[:64]
}

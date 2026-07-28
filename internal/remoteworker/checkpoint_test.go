package remoteworker

import (
	"context"
	"errors"
	"reflect"
	"testing"

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
	wantEvents := []string{"verify", "observe", "quiesce", "cut", "inventory", "archive", "store", "kit", "publish", "resume"}
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
	if reflect.DeepEqual(runtime.events, []string{"verify", "observe", "quiesce", "cut", "inventory", "archive", "store", "kit", "publish", "resume"}) {
		t.Fatal("close resumed services")
	}
	want := []string{"verify", "observe", "quiesce", "cut", "inventory", "archive", "store", "kit", "publish"}
	if !reflect.DeepEqual(runtime.events, want) {
		t.Fatalf("events = %#v", runtime.events)
	}
}

func TestCheckpointFailureAfterBarrierNeverResumesOrPublishes(t *testing.T) {
	runtime := &fakeCheckpointRuntime{errAt: "store"}
	if _, err := checkpoint(context.Background(), checkpointRequest(false), runtime); err == nil {
		t.Fatal("expected error")
	}
	want := []string{"verify", "observe", "quiesce", "cut", "inventory", "archive", "store"}
	if !reflect.DeepEqual(runtime.events, want) {
		t.Fatalf("events = %#v", runtime.events)
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

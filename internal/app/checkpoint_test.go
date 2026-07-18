package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/checkpoint"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/images"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	registryadapter "github.com/joshyorko/camp/internal/registry"
	"github.com/joshyorko/camp/internal/workspace"
)

func sha256Bytes(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

type fakeCheckpointBuilder struct {
	root      string
	inventory domain.ImageInventory
}

func (b *fakeCheckpointBuilder) Build(_ context.Context, request checkpoint.BuildRequest) (checkpoint.BuildResult, error) {
	b.root = request.Root
	b.inventory = request.Inventory
	path := filepath.Join(request.Root, ".camp", "build", "generation.tar.zst")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return checkpoint.BuildResult{}, err
	}
	body := []byte("generation-43")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return checkpoint.BuildResult{}, err
	}
	ref := domain.GenerationRef{Generation: request.Generation, ArchiveSHA256: sha256Bytes(body)}
	objectKey, _ := coordination.GenerationObjectKey(request.Capsule, request.Lineage, ref)
	metadataKey, _ := coordination.GenerationMetadataKey(request.Capsule, request.Lineage, ref)
	return checkpoint.BuildResult{
		Artifact: hauler.GenerationArtifact{Path: path, SHA256: ref.ArchiveSHA256, Size: int64(len(body)), Validated: true},
		Metadata: domain.GenerationMetadata{SchemaVersion: domain.SchemaVersion, Capsule: request.Capsule, Lineage: request.Lineage, Generation: ref, Parent: request.Parent, ObjectKey: objectKey, MetadataKey: metadataKey, Size: int64(len(body)), CreatedAt: request.CreatedAt, Tools: request.Tools, SessionID: request.SessionID, Verified: domain.Verification{LocalHaulLoadable: true}},
	}, nil
}

type fakeCheckpointCapturer struct {
	calls     int
	request   images.CaptureRequest
	inventory domain.ImageInventory
	err       error
}

func (c *fakeCheckpointCapturer) Capture(_ context.Context, request images.CaptureRequest) (domain.ImageInventory, error) {
	c.calls++
	c.request = request
	return c.inventory, c.err
}

type fakeRegistrySealer struct {
	calls   int
	request registryadapter.SnapshotRequest
	result  registryadapter.Snapshot
	err     error
}

func (s *fakeRegistrySealer) Seal(_ context.Context, request registryadapter.SnapshotRequest) (registryadapter.Snapshot, error) {
	s.calls++
	s.request = request
	result := s.result
	if result.Root == "" {
		result.Root = request.SnapshotRoot
	}
	return result, s.err
}

type fakeServingRefresher struct {
	calls   int
	request ServingRefreshRequest
	after   func(context.Context, ServingRefreshRequest) error
	err     error
}

func (r *fakeServingRefresher) Refresh(ctx context.Context, request ServingRefreshRequest) error {
	r.calls++
	r.request = request
	if r.after != nil {
		return r.after(ctx, request)
	}
	return r.err
}

type checkpointFakes struct {
	capture *fakeCheckpointCapturer
	seal    *fakeRegistrySealer
	refresh *fakeServingRefresher
}

func newCheckpointFakes(now time.Time) *checkpointFakes {
	return &checkpointFakes{
		capture: &fakeCheckpointCapturer{inventory: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: now, Images: []domain.Image{}}},
		seal:    &fakeRegistrySealer{}, refresh: &fakeServingRefresher{},
	}
}

func (f *checkpointFakes) pipeline() CheckpointPipeline {
	return CheckpointPipeline{Capturer: f.capture, Sealer: f.seal, Refresher: f.refresh}
}

func prepareCheckpointRuntime(t *testing.T, snapshot *domain.JournalSnapshot, sandbox string) {
	t.Helper()
	overlay := filepath.Join(sandbox, "registry-overlay")
	if err := os.MkdirAll(overlay, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot.Workspace.Context = "default"
	snapshot.Workspace.ID = "camp-" + snapshot.SessionID
	snapshot.Services = []domain.ServiceUnitRecord{{
		Name: "registry", Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 45001, GuestPort: 5000},
		Child:        domain.ProcessRecord{Argv: []string{"/opt/hauler", "store", "--store", filepath.Join(sandbox, "store"), "serve", "registry", "--directory", overlay, "--port", "5000", "--readonly=false"}},
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
	}}
}

type fakeLockValidator struct{ calls int }

func (v *fakeLockValidator) Validate(context.Context, ports.OperationToken) error {
	v.calls++
	return nil
}

type fakeLeaseValidator struct{ calls int }

func (v *fakeLeaseValidator) Revalidate(context.Context, coordination.LeaseToken, time.Time) error {
	v.calls++
	return nil
}

type fakeMirror struct {
	calls  int
	mode   ports.MirrorMode
	root   string
	result *ports.MirrorResult
	err    error
}

func localCheckpointTransports(transport ports.WorkspaceTransport) CheckpointTransports {
	return CheckpointTransports{Local: transport}
}

func (m *fakeMirror) ReturnToStaging(_ context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	m.calls++
	if m.result != nil || m.err != nil {
		if m.result == nil {
			return ports.MirrorResult{}, m.err
		}
		return *m.result, m.err
	}
	mode := m.mode
	if mode == "" {
		mode = ports.MirrorLocalNoop
	}
	root := m.root
	if root == "" {
		root = request.StagingRoot
	}
	method := "local-noop"
	if mode != ports.MirrorLocalNoop {
		method = "rsync"
	}
	return ports.MirrorResult{Mode: mode, Root: root, AttemptID: request.AttemptID, Method: method}, nil
}

func TestCheckpointPublisherUploadsCASesAndAdvancesBaselineOnlyThroughFact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	pointers := coordination.NewPointerRepository(store)
	generations := coordination.NewGenerationRepository(store)
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	opened := domain.GenerationRef{Generation: 42, ArchiveSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	objectKey, _ := coordination.GenerationObjectKey("brain", lineage, opened)
	observed, err := pointers.Create(ctx, domain.LatestPointer{SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, Generation: opened, ObjectKey: objectKey, Size: 10, CreatedAt: now.Add(-time.Hour), Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"}, SessionID: "prior"})
	if err != nil {
		t.Fatal(err)
	}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", OpenedGeneration: &opened, CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: lineage, Mode: domain.SessionReadWrite,
		OpenedGeneration: &opened, CurrentBase: &opened, CurrentPointer: &observed.Pointer, ExpectedPointerRevision: string(observed.Revision), State: domain.SessionOpen,
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root},
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, _ := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	builder := &fakeCheckpointBuilder{}
	lockValidator := &fakeLockValidator{}
	leaseValidator := &fakeLeaseValidator{}
	mirror := &fakeMirror{}
	fakes := newCheckpointFakes(now)
	digest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	fakes.capture.inventory.Images = []domain.Image{{
		OriginalTags: []string{"example.test/app:v1"}, CapturedReference: "127.0.0.1:45001/camp/app:captured", CapturedManifestDigest: digest,
		Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, Source: domain.ImageSourceRegistry,
	}}
	fakes.seal.result = registryadapter.Snapshot{Root: filepath.Join(root, ".camp", "build", "registry-cut-43"), References: []ports.RegistryReference{{
		Repository: "manual/tool", Tag: "latest", ManifestDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
	}}}
	refreshedChild := domain.ProcessIdentity{PID: 902, BootID: "boot-refreshed", StartTicks: 92}
	fakes.refresh.after = func(ctx context.Context, request ServingRefreshRequest) error {
		refreshed, _, err := log.Load(ctx, request.SessionID)
		if err != nil {
			return err
		}
		refreshed.Services[0].Child.Identity = refreshedChild
		intent := checkpointIntent(request.SessionID, "ServiceRestart", 8, now, nil)
		if err := log.RecordIntent(ctx, intent); err != nil {
			return err
		}
		return log.RecordFact(ctx, checkpointFact(intent, now), refreshed)
	}
	publisher := NewCheckpointPublisher(log, lockValidator, leaseValidator, localCheckpointTransports(mirror), fakes.pipeline(), builder, generations, pointers, fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !result.Published || result.Generation.Generation != 43 || result.RefreshError != "" || result.RecoveryCommand != "camp recover "+snapshot.SessionID || lockValidator.calls != 1 || leaseValidator.calls != 2 || mirror.calls != 1 || fakes.capture.calls != 1 || fakes.seal.calls != 1 || fakes.refresh.calls != 1 {
		t.Fatalf("result=%#v calls lock=%d lease=%d mirror=%d", result, lockValidator.calls, leaseValidator.calls, mirror.calls)
	}
	if len(builder.inventory.Images) != 2 || builder.inventory.Images[1].CapturedReference != "127.0.0.1:45001/manual/tool:latest" {
		t.Fatalf("builder bypassed merged sealed inventory: %#v", builder.inventory)
	}
	if fakes.refresh.request.Generation != result.Generation || fakes.refresh.request.HaulPath == "" || fakes.refresh.request.RegistrySnapshotRoot != fakes.seal.result.Root {
		t.Fatalf("refresh request = %#v", fakes.refresh.request)
	}
	current, err := pointers.Read(ctx, "brain", lineage)
	if err != nil || current.Pointer.Generation != result.Generation {
		t.Fatalf("pointer = %#v, %v", current, err)
	}
	metadata, _, err := generations.ReadMetadata(ctx, snapshot.Capsule, snapshot.Lineage, result.Generation)
	if err != nil || !metadata.Verified.LocalHaulLoadable || !metadata.Verified.RemoteBytesVerified {
		t.Fatalf("generation metadata = %#v, %v", metadata, err)
	}
	archive, err := store.Head(ctx, metadata.ObjectKey)
	if err != nil || archive.SHA256 != result.Generation.ArchiveSHA256 || archive.Size != metadata.Size {
		t.Fatalf("generation archive = %#v, %v", archive, err)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.OpenedGeneration == nil || *loaded.OpenedGeneration != opened || loaded.CurrentBase == nil || *loaded.CurrentBase != result.Generation || loaded.CurrentPointer == nil || loaded.CurrentPointer.Generation != result.Generation {
		t.Fatalf("loaded baseline = %#v pending=%#v error=%v", loaded, pending, err)
	}
	if loaded.Services[0].Child.Identity != refreshedChild {
		t.Fatalf("loaded service child = %#v, want refreshed identity %#v", loaded.Services[0].Child.Identity, refreshedChild)
	}
}

func TestCheckpointPublisherCreatesFirstMainPointerAtGenerationOne(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-first", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lineage, Mode: domain.SessionReadWrite,
		Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"},
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	pointers := coordination.NewPointerRepository(store)
	fakes := newCheckpointFakes(now)
	fakes.refresh.err = errors.New("serving refresh unavailable")
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(&fakeMirror{}), fakes.pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(store), pointers, fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Generation.Generation != 1 || result.Pointer.Pointer.Parent != nil || result.Pointer.Pointer.Tools != snapshot.Tools || result.RefreshError != "serving refresh unavailable" || !result.Published {
		t.Fatalf("first publication = %#v", result)
	}
	current, err := pointers.Read(ctx, snapshot.Capsule, lineage)
	if err != nil || current.Pointer.Generation != result.Generation || current.Pointer.Parent != nil {
		t.Fatalf("first pointer = %#v, %v", current, err)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 1 || pending[0].Intent.Transition != "ServingContentRefreshed" {
		t.Fatalf("refresh recovery state pending=%#v error=%v", pending, err)
	}
}

func TestCheckpointPublisherRejectsMismatchedLeaseBeforeExternalEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	base := domain.GenerationRef{Generation: 42, ArchiveSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "different-session", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", OpenedGeneration: &base, CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: lineage, Mode: domain.SessionReadWrite,
		OpenedGeneration: &base, CurrentBase: &base, Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"},
		Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root}, State: domain.SessionOpen,
	}
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	leaseValidator := &fakeLeaseValidator{}
	mirror := &fakeMirror{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, leaseValidator, localCheckpointTransports(mirror), newCheckpointFakes(now).pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(store), coordination.NewPointerRepository(store), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); err == nil {
		t.Fatal("Publish() accepted a lease owned by another session")
	}
	if leaseValidator.calls != 0 || mirror.calls != 0 {
		t.Fatalf("external calls lease=%d mirror=%d, want zero", leaseValidator.calls, mirror.calls)
	}
}

func TestCheckpointPublisherRejectsUnexpectedLocalMirrorModeBeforeBuild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lineage, Mode: domain.SessionReadWrite,
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	builder := &fakeCheckpointBuilder{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(&fakeMirror{mode: "remote"}), newCheckpointFakes(now).pipeline(), builder, coordination.NewGenerationRepository(store), coordination.NewPointerRepository(store), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); err == nil {
		t.Fatal("Publish() accepted a non-local mirror result for a local workspace")
	}
	if builder.root != "" {
		t.Fatalf("builder ran against %q after mirror-mode mismatch", builder.root)
	}
}

func TestCheckpointPublisherBuildsFromReturnedRemoteMirrorRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	stagingRoot := filepath.Join(sandbox, "staging")
	remoteCut := filepath.Join(sandbox, "remote-cut")
	for _, root := range []string{stagingRoot, remoteCut} {
		if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-remote", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lineage, Mode: domain.SessionReadWrite,
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{
			StagingRoot: stagingRoot, Provider: "ssh", LocalProvider: false,
		}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	builder := &fakeCheckpointBuilder{}
	publisher := NewCheckpointPublisher(
		log, &fakeLockValidator{}, &fakeLeaseValidator{},
		CheckpointTransports{
			Local: &fakeMirror{mode: "wrong-local-mode"},
			Remote: &fakeMirror{result: &ports.MirrorResult{
				Mode: workspace.MirrorDevPodSSH, Root: remoteCut, AttemptID: "session-remote-checkpoint-1-rsync",
				Method: "rsync", RemoteRoot: "/workspaces/brain", Exclusions: []string{"/.camp/build/***", "/.camp/runtime/***"},
			}},
		},
		newCheckpointFakes(now).pipeline(), builder,
		coordination.NewGenerationRepository(store), coordination.NewPointerRepository(store), fixedAppClock{now: now},
	)
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if !result.Published || builder.root != remoteCut {
		t.Fatalf("result=%#v builder root=%q, want returned remote root %q", result, builder.root, remoteCut)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.Workspace.Mirror.State != domain.MirrorCompleted || loaded.Workspace.Mirror.Root != remoteCut || loaded.Workspace.Mirror.Method != "rsync" {
		t.Fatalf("durable mirror = %#v pending=%#v error=%v", loaded.Workspace.Mirror, pending, err)
	}
}

func TestCheckpointPublisherRecordsAmbiguousMirrorAndBlocksBlindRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-ambiguous", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default", Provider: "ssh", StagingRoot: sandbox}, State: domain.SessionOpen}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	unknown := &workspace.MirrorOutcomeUnknown{Result: ports.MirrorResult{Mode: workspace.MirrorDevPodSSH, Root: filepath.Join(sandbox, "attempt"), AttemptID: "session-ambiguous-checkpoint-1-rsync", Method: "rsync", RemoteRoot: "/workspaces/brain"}, Err: errors.New("connection lost")}
	mirror := &fakeMirror{err: unknown}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, CheckpointTransports{Remote: mirror}, newCheckpointFakes(now).pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(store), coordination.NewPointerRepository(store), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); !errors.Is(err, unknown) {
		t.Fatalf("first Publish() error = %v", err)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.Workspace.Mirror.State != domain.MirrorAmbiguous || loaded.Workspace.Mirror.Root != unknown.Result.Root {
		t.Fatalf("ambiguous mirror = %#v pending=%#v error=%v", loaded.Workspace.Mirror, pending, err)
	}
	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); err == nil || mirror.calls != 1 {
		t.Fatalf("blind retry error=%v mirror calls=%d", err, mirror.calls)
	}
}

func TestCheckpointPublisherProcessDeathAfterMirrorIntentDoesNotStartAnotherTransfer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log, snapshot := newCloseJournal(t, domain.SessionReadWrite, domain.Materialization{Mode: domain.MaterializationCreated, CleanupPermitted: true})
	intent := checkpointIntent(snapshot.SessionID, "WorkspaceMirrored", 1, time.Unix(200, 0).UTC(), ports.MirrorRequest{Provider: "ssh", WorkspaceID: "camp-brain", Context: "default", StagingRoot: "/controller", AttemptID: snapshot.SessionID + "-checkpoint-1"})
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(t.TempDir(), "backend"))
	if err != nil {
		t.Fatal(err)
	}
	mirror := &fakeMirror{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, CheckpointTransports{Remote: mirror}, newCheckpointFakes(time.Unix(200, 0).UTC()).pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(backend), coordination.NewPointerRepository(backend), fixedAppClock{now: time.Unix(200, 0).UTC()})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); err == nil || mirror.calls != 0 {
		t.Fatalf("Publish() error=%v mirror calls=%d, want pending recovery without transfer", err, mirror.calls)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 1 || pending[0].Intent.ID != intent.ID {
		t.Fatalf("pending mirror = %#v error=%v", pending, err)
	}
}

func TestCheckpointPublisherPreservesUploadedGenerationAndBaselineOnCASConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	pointers := coordination.NewPointerRepository(store)
	generations := coordination.NewGenerationRepository(store)
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	opened := domain.GenerationRef{Generation: 42, ArchiveSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	openedKey, _ := coordination.GenerationObjectKey("brain", lineage, opened)
	observed, err := pointers.Create(ctx, domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, Generation: opened, ObjectKey: openedKey,
		Size: 10, CreatedAt: now.Add(-time.Hour), Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"}, SessionID: "prior",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-conflict", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", OpenedGeneration: &opened, CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lineage, Mode: domain.SessionReadWrite,
		OpenedGeneration: &opened, CurrentBase: &opened, CurrentPointer: &observed.Pointer, ExpectedPointerRevision: string(observed.Revision),
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	intervening := domain.GenerationRef{Generation: 43, ArchiveSHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
	interveningKey, _ := coordination.GenerationObjectKey(snapshot.Capsule, lineage, intervening)
	if _, err := pointers.CompareAndSwap(ctx, observed, domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: snapshot.Capsule, Lineage: lineage, Generation: intervening, Parent: &opened,
		ObjectKey: interveningKey, Size: 11, CreatedAt: now.Add(time.Second), Tools: observed.Pointer.Tools, SessionID: "other-session",
	}); err != nil {
		t.Fatal(err)
	}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(&fakeMirror{}), newCheckpointFakes(now).pipeline(), &fakeCheckpointBuilder{}, generations, pointers, fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if !errors.Is(err, coordination.ErrPointerChanged) {
		t.Fatalf("Publish() error = %v, want ErrPointerChanged", err)
	}
	orphan := domain.GenerationRef{Generation: 43, ArchiveSHA256: sha256Bytes([]byte("generation-43"))}
	if result.Published || result.Generation != orphan || result.RecoveryCommand != "camp recover "+snapshot.SessionID {
		t.Fatalf("conflict result = %#v", result)
	}
	if _, _, err := generations.ReadMetadata(ctx, snapshot.Capsule, lineage, orphan); err != nil {
		t.Fatalf("uploaded orphan metadata missing: %v", err)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || loaded.CurrentBase == nil || *loaded.CurrentBase != opened || loaded.CurrentPointer == nil || loaded.CurrentPointer.Generation != opened || len(pending) != 1 || pending[0].Intent.Transition != "PointerCommitted" {
		t.Fatalf("conflict journal = %#v pending=%#v error=%v", loaded, pending, err)
	}
}

func TestCheckpointPublisherSnapshotFailureCannotBuildUploadOrMovePointer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lineage := domain.Lineage{Branch: "main"}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-seal-failure", Capsule: "brain", Lineage: lineage,
		Machine: "machine-a", CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lineage, Mode: domain.SessionReadWrite,
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	fakes := newCheckpointFakes(now)
	sealFailure := errors.New("registry barrier failed")
	fakes.seal.err = sealFailure
	builder := &fakeCheckpointBuilder{}
	pointers := coordination.NewPointerRepository(store)
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(&fakeMirror{}), fakes.pipeline(), builder, coordination.NewGenerationRepository(store), pointers, fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	_, err = publisher.Publish(ctx, token, snapshot.SessionID)
	if !errors.Is(err, sealFailure) {
		t.Fatalf("Publish() error = %v, want seal failure", err)
	}
	if builder.root != "" || fakes.refresh.calls != 0 {
		t.Fatalf("pipeline continued after failed cut: builder=%q refresh=%d", builder.root, fakes.refresh.calls)
	}
	if _, err := pointers.Read(ctx, snapshot.Capsule, snapshot.Lineage); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("pointer moved after failed cut: %v", err)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 1 || pending[0].Intent.Transition != "RegistrySnapshotSealed" {
		t.Fatalf("failed-cut recovery state pending=%#v error=%v", pending, err)
	}
}

type fixedAppClock struct{ now time.Time }

func (c fixedAppClock) Now() time.Time                       { return c.now }
func (c fixedAppClock) NewTicker(time.Duration) ports.Ticker { panic("not used") }

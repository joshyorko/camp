package app

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	archive "github.com/joshyorko/camp/internal/adapters/archive"
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
	calls     int
	root      string
	inventory domain.ImageInventory
}

func (b *fakeCheckpointBuilder) Build(_ context.Context, request checkpoint.BuildRequest) (checkpoint.BuildResult, error) {
	b.calls++
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

type hostileArchiveBuilder struct {
	calls int
}

func (b *hostileArchiveBuilder) Build(_ context.Context, request checkpoint.BuildRequest) (checkpoint.BuildResult, error) {
	b.calls++
	buildDirectory := filepath.Join(request.Root, ".camp", "build")
	if err := os.MkdirAll(buildDirectory, 0o700); err != nil {
		return checkpoint.BuildResult{}, err
	}
	archivePath := filepath.Join(buildDirectory, "generation-unsafe.tar.zst")
	if err := writeHostileTarArchive(archivePath); err != nil {
		return checkpoint.BuildResult{}, err
	}
	if err := archive.NewTarZstd().Extract(context.Background(), archivePath, filepath.Join(buildDirectory, "extracted")); err != nil {
		return checkpoint.BuildResult{}, err
	}
	return checkpoint.BuildResult{}, errors.New("hostile build did not fail")
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
	after   func(context.Context, registryadapter.SnapshotRequest) error
	err     error
}

func (s *fakeRegistrySealer) Seal(ctx context.Context, request registryadapter.SnapshotRequest) (registryadapter.Snapshot, error) {
	s.calls++
	s.request = request
	result := s.result
	if result.Root == "" {
		result.Root = request.SnapshotRoot
	}
	if s.err == nil && s.after != nil {
		if err := s.after(ctx, request); err != nil {
			return result, err
		}
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
		Name: "registry", LaunchToken: "registry-initial", Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 45001, GuestPort: 5000},
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

type crashBeforeCheckpointFactJournal struct {
	ports.Journal
	transition string
	crashed    bool
}

func (j *crashBeforeCheckpointFactJournal) RecordFact(ctx context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if !j.crashed && fact.Transition == j.transition {
		j.crashed = true
		return errors.New("simulated process death before checkpoint fact")
	}
	return j.Journal.RecordFact(ctx, fact, snapshot)
}

type fakeMirror struct {
	calls    int
	requests []ports.MirrorRequest
	mode     ports.MirrorMode
	root     string
	result   *ports.MirrorResult
	err      error
}

func localCheckpointTransports(transport ports.WorkspaceTransport) CheckpointTransports {
	return CheckpointTransports{Local: transport}
}

func (m *fakeMirror) ReturnToStaging(_ context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	m.calls++
	m.requests = append(m.requests, request)
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
		return ports.MirrorResult{Mode: mode, Root: root, AttemptID: request.AttemptID + "-rsync", Method: method}, nil
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
	fakes.seal.result = registryadapter.Snapshot{Root: filepath.Join(root, ".camp", "build", "registry-cut-43"), References: []ports.RegistryReference{
		{Repository: "manual/tool", Tag: "latest", ManifestDigest: "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		{Repository: "hauler/brain.tar.zst", Tag: "sha256-internal", ManifestDigest: "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"},
	}}
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
	if len(builder.inventory.Images) != 1 || builder.inventory.Images[0].CapturedReference != "127.0.0.1:45001/manual/tool:latest" {
		t.Fatalf("builder inventory was not derived only from the sealed registry cut: %#v", builder.inventory)
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
	fakes.refresh.err = nil
	recovered, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !recovered.Published || recovered.Generation != result.Generation {
		t.Fatalf("resume pending serving refresh result=%#v error=%v", recovered, err)
	}
	if fakes.capture.calls != 1 || fakes.seal.calls != 1 || fakes.refresh.calls != 2 {
		t.Fatalf("resume repeated checkpoint effects capture=%d seal=%d refresh=%d", fakes.capture.calls, fakes.seal.calls, fakes.refresh.calls)
	}
	_, pending, err = log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("refresh recovery remained pending=%#v error=%v", pending, err)
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

func TestCheckpointPublisherRejectsMissingPersistedTransportBeforeMirrorIntent(t *testing.T) {
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
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-missing-local", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite,
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
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, CheckpointTransports{Remote: &fakeMirror{}}, newCheckpointFakes(now).pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(store), coordination.NewPointerRepository(store), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); !errors.Is(err, workspace.ErrTransportUnavailable) {
		t.Fatalf("Publish() error = %v, want ErrTransportUnavailable", err)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v error=%v, want no false mirror intent", pending, err)
	}
}

func TestCheckpointPublisherRejectsRemoteIdentityMismatchBeforeMirrorIntent(t *testing.T) {
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
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-remote-identity", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite,
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{ID: "persisted-workspace", Context: "persisted-context", StagingRoot: root, Provider: "ssh"}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	remote := workspace.NewRemote(workspace.RemoteConfig{WorkspaceID: "different-workspace", Context: snapshot.Workspace.Context}, nil, nil, nil, nil)
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, CheckpointTransports{Remote: remote}, newCheckpointFakes(now).pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(store), coordination.NewPointerRepository(store), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); !errors.Is(err, workspace.ErrRemoteIdentityMismatch) {
		t.Fatalf("Publish() error = %v, want ErrRemoteIdentityMismatch", err)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %#v error=%v, want no mirror intent", pending, err)
	}
}

func TestCheckpointPublisherRejectsHostileArchiveBeforePointerPublication(t *testing.T) {
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
	lineage := domain.Lineage{Branch: "main"}
	opened := domain.GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("b", 64)}
	openedKey, _ := coordination.GenerationObjectKey("brain", lineage, opened)
	pointers := coordination.NewPointerRepository(store)
	openedPointer, err := pointers.Create(ctx, domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, Generation: opened, ObjectKey: openedKey, Size: 1024,
		CreatedAt: time.Unix(99, 0).UTC(), Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"}, SessionID: "prior",
	})
	if err != nil {
		t.Fatal(err)
	}
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-hostile", Capsule: "brain", Lineage: lineage, Machine: "machine",
		OpenedGeneration: &opened, CreatedAt: time.Unix(100, 0).UTC(), HeartbeatAt: time.Unix(100, 0).UTC(), ExpiresAt: time.Unix(101, 0).UTC(),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lineage, Mode: domain.SessionReadWrite,
		OpenedGeneration: &opened, CurrentBase: &opened, CurrentPointer: &openedPointer.Pointer,
		ExpectedPointerRevision: string(openedPointer.Revision), Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"},
		Workspace: domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root}, State: domain.SessionOpen,
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	fakes := newCheckpointFakes(time.Unix(100, 0).UTC())
	builder := &hostileArchiveBuilder{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(&fakeMirror{}), fakes.pipeline(), builder, coordination.NewGenerationRepository(store), pointers, fixedAppClock{now: time.Unix(100, 0).UTC()})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}
	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); !errors.Is(err, archive.ErrUnsafeArchive) {
		t.Fatalf("Publish() error = %v, want ErrUnsafeArchive", err)
	}
	if builder.calls != 1 {
		t.Fatalf("builder calls = %d, want 1", builder.calls)
	}
	current, err := pointers.Read(ctx, snapshot.Capsule, snapshot.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != openedPointer.Revision || current.Pointer.Generation != opened {
		t.Fatalf("pointer changed after hostile build failure: %#v", current)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Intent.Transition != "RootSnapshotStable" {
		t.Fatalf("pending = %#v", pending)
	}
	if loaded.CurrentPointer == nil || loaded.CurrentPointer.Generation != opened || loaded.CurrentPointer.ObjectKey != openedKey {
		t.Fatalf("journal pointer = %#v", loaded.CurrentPointer)
	}
	if fakes.seal.calls != 1 || fakes.capture.calls != 1 {
		t.Fatalf("earlier effects repeated seal=%d capture=%d", fakes.seal.calls, fakes.capture.calls)
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

func TestCheckpointPublisherRetriesAmbiguousMirrorWithNextLogicalAttempt(t *testing.T) {
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
	mirror.err = nil
	mirror.result = &ports.MirrorResult{
		Mode: workspace.MirrorDevPodSSH, Root: sandbox, AttemptID: "session-ambiguous-checkpoint-2-rsync",
		Method: "rsync", RemoteRoot: "/workspaces/brain",
	}
	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !result.Published || mirror.calls != 2 {
		t.Fatalf("retry result=%#v error=%v mirror calls=%d", result, err, mirror.calls)
	}
	loaded, pending, err = log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.Workspace.Mirror.State != domain.MirrorCompleted || loaded.Workspace.Mirror.LogicalAttempt != 2 || loaded.Workspace.Mirror.AttemptID != mirror.result.AttemptID {
		t.Fatalf("recovered mirror = %#v pending=%#v error=%v", loaded.Workspace.Mirror, pending, err)
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

func TestCheckpointPublisherSimulatedProcessDeathMatrix(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		transition   string
		mirrorCalls  int
		captureCalls int
		sealCalls    int
		buildCalls   int
		refreshCalls int
	}{
		{transition: "WorkspaceImagesInventoried", mirrorCalls: 1, captureCalls: 2, sealCalls: 1, buildCalls: 1, refreshCalls: 1},
		{transition: "RegistrySnapshotSealed", mirrorCalls: 1, captureCalls: 1, sealCalls: 2, buildCalls: 1, refreshCalls: 1},
		{transition: "RootSnapshotStable", mirrorCalls: 1, captureCalls: 1, sealCalls: 1, buildCalls: 2, refreshCalls: 1},
		{transition: "GenerationUploaded", mirrorCalls: 1, captureCalls: 1, sealCalls: 1, buildCalls: 1, refreshCalls: 1},
		{transition: "PointerCommitted", mirrorCalls: 1, captureCalls: 1, sealCalls: 1, buildCalls: 1, refreshCalls: 1},
		{transition: "ServingContentRefreshed", mirrorCalls: 1, captureCalls: 1, sealCalls: 1, buildCalls: 1, refreshCalls: 2},
	} {
		test := test
		t.Run(test.transition, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			sandbox := t.TempDir()
			root := filepath.Join(sandbox, "root")
			if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
				t.Fatal(err)
			}
			backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0).UTC()
			lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-crash-" + test.transition, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
			snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, State: domain.SessionOpen, Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default", Provider: "docker", LocalProvider: true, LocalFolder: root, StagingRoot: root}}
			prepareCheckpointRuntime(t, &snapshot, sandbox)
			store, err := journal.NewStore(filepath.Join(sandbox, "journal"))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Create(ctx, snapshot); err != nil {
				t.Fatal(err)
			}
			crashing := &crashBeforeCheckpointFactJournal{Journal: store, transition: test.transition}
			mirror := &fakeMirror{}
			fakes := newCheckpointFakes(now)
			builder := &fakeCheckpointBuilder{}
			publisher := NewCheckpointPublisher(crashing, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(mirror), fakes.pipeline(), builder, coordination.NewGenerationRepository(backend), coordination.NewPointerRepository(backend), fixedAppClock{now: now})
			token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

			first, firstErr := publisher.Publish(ctx, token, snapshot.SessionID)
			if test.transition == "ServingContentRefreshed" {
				if firstErr != nil || !first.Published || first.RefreshError == "" {
					t.Fatalf("first Publish() result=%#v error=%v, want published result with refresh recovery", first, firstErr)
				}
			} else if firstErr == nil {
				t.Fatalf("first Publish() result=%#v, want simulated process death", first)
			}
			result, err := publisher.Publish(ctx, token, snapshot.SessionID)
			if err != nil || !result.Published || result.RefreshError != "" {
				t.Fatalf("retry Publish() result=%#v error=%v", result, err)
			}
			if mirror.calls != test.mirrorCalls || fakes.capture.calls != test.captureCalls || fakes.seal.calls != test.sealCalls || builder.calls != test.buildCalls || fakes.refresh.calls != test.refreshCalls {
				t.Fatalf("effects mirror=%d capture=%d seal=%d build=%d refresh=%d", mirror.calls, fakes.capture.calls, fakes.seal.calls, builder.calls, fakes.refresh.calls)
			}
			loaded, pending, err := store.Load(ctx, snapshot.SessionID)
			if err != nil || len(pending) != 0 || loaded.CurrentBase == nil || *loaded.CurrentBase != result.Generation {
				t.Fatalf("recovered checkpoint=%#v pending=%#v error=%v", loaded.Checkpoint, pending, err)
			}
		})
	}
}

func TestCheckpointPublisherAdoptsExactPendingImageCaptureWithoutRepeatingMirror(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-image-cut", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	attemptID := lease.SessionID + "-checkpoint-1"
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, State: domain.SessionOpen,
		Lease:     domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"},
		Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default", Provider: "docker", LocalProvider: true, LocalFolder: root, StagingRoot: root, Mirror: domain.MirrorAttemptRecord{LogicalAttempt: 1, AttemptID: attemptID, State: domain.MirrorCompleted, Root: root, Method: "local-noop"}},
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	runtime, err := checkpointRegistryRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	request := images.CaptureRequest{Scope: images.EngineScope{Context: snapshot.Workspace.Context, WorkspaceID: snapshot.Workspace.ID}, Capsule: snapshot.Capsule, RegistryAuthority: runtime.authority, RegistryEndpoint: runtime.endpoint, Previous: registryadapter.ExcludeInternalArtifacts(snapshot.Images)}
	intent := checkpointAttemptIntent(snapshot.SessionID, attemptID, "WorkspaceImagesInventoried", 2, now, request)
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	mirror := &fakeMirror{}
	fakes := newCheckpointFakes(now)
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(mirror), fakes.pipeline(), &fakeCheckpointBuilder{}, coordination.NewGenerationRepository(backend), coordination.NewPointerRepository(backend), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !result.Published {
		t.Fatalf("resume pending image capture result=%#v error=%v", result, err)
	}
	if mirror.calls != 0 || fakes.capture.calls != 1 || fakes.seal.calls != 1 {
		t.Fatalf("resume effects mirror=%d capture=%d seal=%d", mirror.calls, fakes.capture.calls, fakes.seal.calls)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after image recovery=%#v error=%v", pending, err)
	}
}

func TestCheckpointPublisherAdoptsExactPendingImmutableUploadWithoutRebuilding(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-upload-cut", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, State: domain.SessionOpen, Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default", Provider: "docker", LocalProvider: true, LocalFolder: root, StagingRoot: root, Mirror: domain.MirrorAttemptRecord{LogicalAttempt: 1, AttemptID: lease.SessionID + "-checkpoint-1", State: domain.MirrorCompleted, Root: root, Method: "local-noop"}}, RegistryCutRoot: filepath.Join(root, ".camp", "build", "registry-cut-1")}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	seedBuilder := &fakeCheckpointBuilder{}
	built, err := seedBuilder.Build(ctx, checkpoint.BuildRequest{Capsule: snapshot.Capsule, Root: root, Lineage: snapshot.Lineage, Generation: 1, SessionID: snapshot.SessionID, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Checkpoint = domain.Checkpoint{State: domain.CheckpointVerified, Generation: &built.Metadata.Generation, LocalHaulPath: built.Artifact.Path, ObjectKey: built.Metadata.ObjectKey}
	if _, err := coordination.NewGenerationRepository(backend).PutAndVerify(ctx, built.Metadata, restartableFile(built.Artifact.Path)); err != nil {
		t.Fatal(err)
	}
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	intent := checkpointAttemptIntent(snapshot.SessionID, snapshot.Workspace.Mirror.AttemptID, "GenerationUploaded", 5, now, struct {
		ObjectKey string `json:"objectKey"`
		SHA256    string `json:"sha256"`
		Size      int64  `json:"size"`
	}{ObjectKey: built.Metadata.ObjectKey, SHA256: built.Artifact.SHA256, Size: built.Artifact.Size})
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	mirror := &fakeMirror{}
	fakes := newCheckpointFakes(now)
	builder := &fakeCheckpointBuilder{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(mirror), fakes.pipeline(), builder, coordination.NewGenerationRepository(backend), coordination.NewPointerRepository(backend), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !result.Published {
		t.Fatalf("resume pending upload result=%#v error=%v", result, err)
	}
	if mirror.calls != 0 || fakes.capture.calls != 0 || fakes.seal.calls != 0 || builder.calls != 0 {
		t.Fatalf("earlier effects repeated mirror=%d capture=%d seal=%d build=%d", mirror.calls, fakes.capture.calls, fakes.seal.calls, builder.calls)
	}
	metadata, _, err := coordination.NewGenerationRepository(backend).ReadMetadata(ctx, snapshot.Capsule, snapshot.Lineage, built.Metadata.Generation)
	if err != nil || !metadata.Verified.RemoteBytesVerified {
		t.Fatalf("recovered generation metadata=%#v error=%v", metadata, err)
	}
}

func TestCheckpointPublisherAdoptsExactCommittedPointerAndAdvancesBaseline(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-pointer-cut", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, State: domain.SessionOpen, Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default", Provider: "docker", LocalProvider: true, LocalFolder: root, StagingRoot: root, Mirror: domain.MirrorAttemptRecord{LogicalAttempt: 1, AttemptID: lease.SessionID + "-checkpoint-1", State: domain.MirrorCompleted, Root: root, Method: "local-noop"}}, RegistryCutRoot: filepath.Join(root, ".camp", "build", "registry-cut-1")}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	builder := &fakeCheckpointBuilder{}
	built, err := builder.Build(ctx, checkpoint.BuildRequest{Capsule: snapshot.Capsule, Root: root, Lineage: snapshot.Lineage, Generation: 1, SessionID: snapshot.SessionID, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	generations := coordination.NewGenerationRepository(backend)
	if _, err := generations.PutAndVerify(ctx, built.Metadata, restartableFile(built.Artifact.Path)); err != nil {
		t.Fatal(err)
	}
	snapshot.Checkpoint = domain.Checkpoint{State: domain.CheckpointUploaded, Generation: &built.Metadata.Generation, LocalHaulPath: built.Artifact.Path, ObjectKey: built.Metadata.ObjectKey}
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	next := domain.LatestPointer{SchemaVersion: domain.SchemaVersion, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage, Generation: built.Metadata.Generation, ObjectKey: built.Metadata.ObjectKey, Size: built.Metadata.Size, CreatedAt: built.Metadata.CreatedAt, SessionID: snapshot.SessionID}
	intent := checkpointAttemptIntent(snapshot.SessionID, snapshot.Workspace.Mirror.AttemptID, "PointerCommitted", 6, now, next)
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	pointers := coordination.NewPointerRepository(backend)
	committed, err := pointers.Create(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	mirror := &fakeMirror{}
	fakes := newCheckpointFakes(now)
	rebuild := &fakeCheckpointBuilder{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(mirror), fakes.pipeline(), rebuild, generations, pointers, fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !result.Published || result.Pointer.Revision != committed.Revision {
		t.Fatalf("resume committed pointer result=%#v error=%v", result, err)
	}
	if mirror.calls != 0 || fakes.capture.calls != 0 || fakes.seal.calls != 0 || rebuild.calls != 0 {
		t.Fatalf("earlier effects repeated mirror=%d capture=%d seal=%d build=%d", mirror.calls, fakes.capture.calls, fakes.seal.calls, rebuild.calls)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.CurrentBase == nil || *loaded.CurrentBase != built.Metadata.Generation || loaded.ExpectedPointerRevision != string(committed.Revision) {
		t.Fatalf("recovered pointer baseline=%#v pending=%#v error=%v", loaded, pending, err)
	}
}

func TestCheckpointPublisherRejectsPendingPointerDriftFromVerifiedGeneration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-pointer-drift", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, State: domain.SessionOpen, Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}, Workspace: domain.WorkspaceRecord{ID: "camp-brain", Context: "default", Provider: "docker", LocalProvider: true, LocalFolder: root, StagingRoot: root, Mirror: domain.MirrorAttemptRecord{LogicalAttempt: 1, AttemptID: lease.SessionID + "-checkpoint-1", State: domain.MirrorCompleted, Root: root, Method: "local-noop"}}, RegistryCutRoot: filepath.Join(root, ".camp", "build", "registry-cut-1")}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	builder := &fakeCheckpointBuilder{}
	built, err := builder.Build(ctx, checkpoint.BuildRequest{Capsule: snapshot.Capsule, Root: root, Lineage: snapshot.Lineage, Generation: 1, SessionID: snapshot.SessionID, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	generations := coordination.NewGenerationRepository(backend)
	if _, err := generations.PutAndVerify(ctx, built.Metadata, restartableFile(built.Artifact.Path)); err != nil {
		t.Fatal(err)
	}
	snapshot.Checkpoint = domain.Checkpoint{State: domain.CheckpointUploaded, Generation: &built.Metadata.Generation, LocalHaulPath: built.Artifact.Path, ObjectKey: built.Metadata.ObjectKey}
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	drifted := domain.LatestPointer{SchemaVersion: domain.SchemaVersion, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage, Generation: built.Metadata.Generation, ObjectKey: built.Metadata.ObjectKey, Size: built.Metadata.Size + 1, CreatedAt: built.Metadata.CreatedAt, Tools: built.Metadata.Tools, SessionID: snapshot.SessionID}
	intent := checkpointAttemptIntent(snapshot.SessionID, snapshot.Workspace.Mirror.AttemptID, "PointerCommitted", 6, now, drifted)
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	pointers := coordination.NewPointerRepository(backend)
	mirror := &fakeMirror{}
	fakes := newCheckpointFakes(now)
	rebuild := &fakeCheckpointBuilder{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(mirror), fakes.pipeline(), rebuild, generations, pointers, fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	if _, err := publisher.Publish(ctx, token, snapshot.SessionID); err == nil || !strings.Contains(err.Error(), "drifted from the verified generation") {
		t.Fatalf("Publish() error = %v, want verified-generation drift rejection", err)
	}
	if mirror.calls != 0 || fakes.capture.calls != 0 || fakes.seal.calls != 0 || rebuild.calls != 0 {
		t.Fatalf("earlier effects repeated mirror=%d capture=%d seal=%d build=%d", mirror.calls, fakes.capture.calls, fakes.seal.calls, rebuild.calls)
	}
	if _, err := pointers.Read(ctx, snapshot.Capsule, snapshot.Lineage); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("pointer moved after drifted recovery: %v", err)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 1 || pending[0].Intent.ID != intent.ID {
		t.Fatalf("pending pointer = %#v error=%v", pending, err)
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
	fakes.seal.err = nil
	fakes.seal.after = func(ctx context.Context, _ registryadapter.SnapshotRequest) error {
		current, _, err := log.Load(ctx, snapshot.SessionID)
		if err != nil {
			return err
		}
		current.Services[0].Helper.Identity = domain.ProcessIdentity{PID: 202, BootID: "boot", StartTicks: 2}
		current.Services[0].Child.Identity = domain.ProcessIdentity{PID: 303, BootID: "boot-child", StartTicks: 3}
		nested := ports.IntentRecord{ID: "registry-seal-restart", SessionID: snapshot.SessionID, Transition: "ServiceRestart", Attempt: 1, Timestamp: now}
		if err := log.RecordIntent(ctx, nested); err != nil {
			return err
		}
		return log.RecordFact(ctx, checkpointFact(nested, now), current)
	}
	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !result.Published {
		t.Fatalf("resume pending registry seal result=%#v error=%v", result, err)
	}
	if fakes.capture.calls != 1 || fakes.seal.calls != 2 {
		t.Fatalf("resume effects capture=%d seal=%d, want image capture once and exact seal retry", fakes.capture.calls, fakes.seal.calls)
	}
	loaded, _, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || loaded.Services[0].Helper.Identity != (domain.ProcessIdentity{PID: 202, BootID: "boot", StartTicks: 2}) || loaded.Services[0].Child.Identity != (domain.ProcessIdentity{PID: 303, BootID: "boot-child", StartTicks: 3}) {
		t.Fatalf("registry seal replay lost nested restart identities: services=%#v error=%v", loaded.Services, err)
	}
}

func TestCheckpointPublisherResumesExactPendingRootSnapshotWithoutRepeatingEarlierEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp", "build"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-build-retry", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Machine: "machine-a", CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}
	attemptID := lease.SessionID + "-checkpoint-1"
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage, Mode: domain.SessionReadWrite, State: domain.SessionOpen,
		Lease: domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"},
		Workspace: domain.WorkspaceRecord{
			ID: "camp-brain", Context: "default", Provider: "docker", LocalProvider: true, LocalFolder: root, StagingRoot: root,
			Mirror: domain.MirrorAttemptRecord{LogicalAttempt: 1, AttemptID: attemptID, State: domain.MirrorCompleted, Root: root, Method: "local-noop"},
		},
		Images: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: now, Images: []domain.Image{
			{CapturedReference: "127.0.0.1:45001/hauler/camp-session-seed:latest", CapturedManifestDigest: "sha256:" + strings.Repeat("a", 64), Source: domain.ImageSourceRegistry},
			{CapturedReference: "127.0.0.1:45001/manual/direct:v1", CapturedManifestDigest: "sha256:" + strings.Repeat("b", 64), Source: domain.ImageSourceRegistry},
		}},
		RegistryCutRoot: filepath.Join(root, ".camp", "build", "registry-cut-1"),
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	log, err := journal.NewStore(filepath.Join(sandbox, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	buildIntent := checkpointAttemptIntent(snapshot.SessionID, attemptID, "RootSnapshotStable", 4, now, struct {
		Root       string `json:"root"`
		Generation uint64 `json:"generation"`
	}{Root: root, Generation: 1})
	if err := log.RecordIntent(ctx, buildIntent); err != nil {
		t.Fatal(err)
	}
	mirror := &fakeMirror{}
	fakes := newCheckpointFakes(now)
	builder := &fakeCheckpointBuilder{}
	publisher := NewCheckpointPublisher(log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(mirror), fakes.pipeline(), builder, coordination.NewGenerationRepository(backend), coordination.NewPointerRepository(backend), fixedAppClock{now: now})
	token := ports.OperationToken{ID: "lock", Owner: ports.OperationOwner{SessionID: snapshot.SessionID, Operation: "sync"}}

	result, err := publisher.Publish(ctx, token, snapshot.SessionID)
	if err != nil || !result.Published {
		t.Fatalf("resume pending root snapshot result=%#v error=%v", result, err)
	}
	if mirror.calls != 0 || fakes.capture.calls != 0 || fakes.seal.calls != 0 {
		t.Fatalf("earlier effects repeated: mirror=%d capture=%d seal=%d", mirror.calls, fakes.capture.calls, fakes.seal.calls)
	}
	if len(builder.inventory.Images) != 1 || builder.inventory.Images[0].CapturedReference != "127.0.0.1:45001/manual/direct:v1" {
		t.Fatalf("resumed build inventory = %#v", builder.inventory)
	}
}

func TestNormalizeRegistrySealSnapshotAcceptsJSONEquivalentImageInventory(t *testing.T) {
	t.Parallel()
	expected := domain.JournalSnapshot{Images: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		OriginalTags: []string{"example.test/app:v1"}, OriginalRepoDigests: nil, CapturedReference: "127.0.0.1:5000/camp/app:captured",
	}}}}
	durable := expected
	durable.Images.Images = append([]domain.Image(nil), expected.Images.Images...)
	durable.Images.Images[0].OriginalRepoDigests = []string{}
	if reflect.DeepEqual(expected.Images, durable.Images) {
		t.Fatal("test fixture must differ in Go representation")
	}
	normalized := normalizeRegistrySealSnapshot(durable, expected)
	if !reflect.DeepEqual(normalized, expected) {
		t.Fatalf("normalized snapshot = %#v, want %#v", normalized, expected)
	}
}

func TestPrepareRegistrySealAllowsOnlyItsExactPendingRestartChild(t *testing.T) {
	t.Parallel()
	sandbox := t.TempDir()
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-seal-restart", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
	root := filepath.Join(sandbox, "root")
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage,
		Workspace: domain.WorkspaceRecord{Context: "default", ID: "camp-brain", Mirror: domain.MirrorAttemptRecord{LogicalAttempt: 1, AttemptID: lease.SessionID + "-checkpoint-1", State: domain.MirrorCompleted, Root: root}},
	}
	prepareCheckpointRuntime(t, &snapshot, sandbox)
	runtime, err := checkpointRegistryRuntime(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	request := registryadapter.SnapshotRequest{SessionID: snapshot.SessionID, OverlayRoot: runtime.overlay, SnapshotRoot: filepath.Join(root, ".camp", "build", "registry-cut-1"), CatalogEndpoint: runtime.endpoint, RegistryLaunchToken: runtime.launchToken}
	seal := checkpointAttemptIntent(snapshot.SessionID, snapshot.Workspace.Mirror.AttemptID, "RegistrySnapshotSealed", 3, now, request)
	restart := ports.IntentRecord{ID: "registry-" + runtime.launchToken + "-restart", SessionID: snapshot.SessionID, Transition: "ServiceRestart"}
	start := ports.IntentRecord{ID: "registry-" + runtime.launchToken, SessionID: snapshot.SessionID, Transition: "ServiceStart"}
	pending := []ports.PendingIntent{{Intent: seal}, {Intent: restart}, {Intent: start}}

	prepared, err := (&CheckpointPublisher{}).prepareRegistrySeal(context.Background(), snapshot, pending, coordination.LeaseToken{Lease: lease}, 1, now)
	if err != nil || prepared.intent.ID != seal.ID {
		t.Fatalf("prepareRegistrySeal() prepared=%#v error=%v", prepared, err)
	}
}

func writeHostileTarArchive(path string) error {
	attack := []tar.Header{{Name: "../escape", Typeflag: tar.TypeReg, Mode: 0o644}}
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	encoder, err := zstd.NewWriter(file)
	if err != nil {
		return err
	}
	writer := tar.NewWriter(encoder)
	for _, header := range attack {
		header.Size = int64(len("x"))
		if err := writer.WriteHeader(&header); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, "x"); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

type fixedAppClock struct{ now time.Time }

func (c fixedAppClock) Now() time.Time                       { return c.now }
func (c fixedAppClock) NewTicker(time.Duration) ports.Ticker { panic("not used") }

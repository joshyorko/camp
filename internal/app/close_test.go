package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

type fakeCloseEffects struct {
	events *[]string
	failAt string
}

func (e *fakeCloseEffects) effect(name string) error {
	*e.events = append(*e.events, name)
	if e.failAt == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (e *fakeCloseEffects) CloseWorkspace(context.Context, domain.JournalSnapshot, bool) error {
	return e.effect("workspace")
}
func (e *fakeCloseEffects) StopForwarders(context.Context, domain.JournalSnapshot) error {
	return e.effect("forwarders")
}
func (e *fakeCloseEffects) StopServices(context.Context, domain.JournalSnapshot) error {
	return e.effect("services")
}
func (e *fakeCloseEffects) StopSupervisor(context.Context, domain.JournalSnapshot) error {
	return e.effect("supervisor")
}
func (e *fakeCloseEffects) ReleaseLease(context.Context, domain.JournalSnapshot) error {
	return e.effect("lease")
}
func (e *fakeCloseEffects) RemoveMaterialization(_ context.Context, snapshot domain.JournalSnapshot) (bool, error) {
	err := e.effect("materialization")
	return snapshot.Materialization.Mode == domain.MaterializationCreated, err
}

func newCloseJournal(t *testing.T, mode domain.SessionMode, materialization domain.Materialization) (*journal.Store, domain.JournalSnapshot) {
	t.Helper()
	now := time.Unix(100, 0).UTC()
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-close", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Mode: mode, State: domain.SessionOpen, Materialization: materialization, Cleanup: domain.Cleanup{State: domain.CleanupPending}, CreatedAt: now, UpdatedAt: now,
	}
	if mode == domain.SessionReadWrite {
		lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage, Machine: "machine", CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Hour)}
		snapshot.Lease = domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"}
	}
	store, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	return store, snapshot
}

func TestCloseKeepsOneLockAcrossFinalPublicationAndOrderedCleanup(t *testing.T) {
	t.Parallel()
	log, snapshot := newCloseJournal(t, domain.SessionReadWrite, domain.Materialization{Mode: domain.MaterializationCreated, CleanupPermitted: true})
	events := []string{}
	locker := &fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}}
	generation := domain.GenerationRef{Generation: 43, ArchiveSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	publisher := &fakeCheckpointPublisher{events: &events, result: CheckpointResult{Published: true, Generation: generation}}
	effects := &fakeCloseEffects{events: &events}
	result, err := NewClose(log, locker, publisher, effects, fixedAppClock{now: time.Unix(200, 0)}).Run(context.Background(), CloseRequest{SessionID: snapshot.SessionID})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.PublicationSucceeded || !result.CleanupSucceeded || result.Generation != generation {
		t.Fatalf("result = %#v", result)
	}
	want := []string{"lock:close", "publish:session-close:close", "workspace", "forwarders", "services", "supervisor", "lease", "materialization", "unlock:close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	loaded, pending, err := log.Load(context.Background(), snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.State != domain.SessionClosed || loaded.Cleanup.State != domain.CleanupSucceeded || !loaded.Checkpoint.PublicationSucceeded {
		t.Fatalf("closed snapshot = %#v pending=%#v error=%v", loaded, pending, err)
	}
}

func TestCloseComposesWithRealCheckpointPublisherWithoutSelfCreatedPendingIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	lease := domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-real-close", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		Machine: "machine-a", CreatedAt: now.Add(-time.Minute), HeartbeatAt: now, ExpiresAt: now.Add(time.Hour),
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.SessionID, Capsule: lease.Capsule, Lineage: lease.Lineage,
		Mode: domain.SessionReadWrite, State: domain.SessionOpen,
		Lease:           domain.LeaseRecord{Lease: &lease, Revision: "lease-r1"},
		Workspace:       domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root},
		Materialization: domain.Materialization{Mode: domain.MaterializationCreated, CleanupPermitted: true},
		Cleanup:         domain.Cleanup{State: domain.CleanupPending}, CreatedAt: now, UpdatedAt: now,
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
	publisher := NewCheckpointPublisher(
		log, &fakeLockValidator{}, &fakeLeaseValidator{}, localCheckpointTransports(&fakeMirror{}), fakes.pipeline(), &fakeCheckpointBuilder{},
		coordination.NewGenerationRepository(backend), coordination.NewPointerRepository(backend), fixedAppClock{now: now},
	)
	events := []string{}
	result, err := NewClose(
		log,
		&fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}},
		publisher,
		&fakeCloseEffects{events: &events},
		fixedAppClock{now: now},
	).Run(ctx, CloseRequest{SessionID: snapshot.SessionID})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.PublicationSucceeded || !result.CleanupSucceeded || result.Generation.Generation != 1 {
		t.Fatalf("Run() result = %#v", result)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.State != domain.SessionClosed {
		t.Fatalf("closed snapshot = %#v pending=%#v error=%v", loaded, pending, err)
	}
}

func TestBranchSessionAdvancesFromSourceThroughTwoSyncsAndClose(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sandbox := t.TempDir()
	root := filepath.Join(sandbox, "root")
	if err := os.MkdirAll(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend, err := filebackend.New(filepath.Join(sandbox, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0).UTC()
	pointers := coordination.NewPointerRepository(backend)
	mainLineage := domain.Lineage{Branch: "main"}
	sourceRef := domain.GenerationRef{Generation: 42, ArchiveSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
	sourceKey, err := coordination.GenerationObjectKey("brain", mainLineage, sourceRef)
	if err != nil {
		t.Fatal(err)
	}
	source, err := pointers.Create(ctx, domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: mainLineage, Generation: sourceRef,
		ObjectKey: sourceKey, Size: 42, CreatedAt: now.Add(-time.Hour), Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"}, SessionID: "source-session",
	})
	if err != nil {
		t.Fatal(err)
	}
	branch := domain.Lineage{Branch: "feature-safe"}
	leases := coordination.NewLeaseRepository(backend)
	lease, err := leases.AcquireBranchFrom(ctx, "brain", branch, coordination.LeaseOwner{SessionID: "branch-session", Machine: "machine-a"}, source, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: lease.Lease.SessionID, Capsule: "brain", Lineage: branch,
		Mode: domain.SessionReadWrite, Tools: source.Pointer.Tools, OpenedGeneration: &sourceRef, CurrentBase: &sourceRef,
		Lease:           domain.LeaseRecord{Lease: &lease.Lease, Revision: string(lease.Revision)},
		Workspace:       domain.WorkspaceRecord{StagingRoot: root, Provider: "docker", LocalProvider: true, LocalFolder: root},
		Materialization: domain.Materialization{Mode: domain.MaterializationCreated, CleanupPermitted: true},
		State:           domain.SessionOpen, Cleanup: domain.Cleanup{State: domain.CleanupPending}, CreatedAt: now, UpdatedAt: now,
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
	publisher := NewCheckpointPublisher(
		log, &fakeLockValidator{}, leases, localCheckpointTransports(&fakeMirror{}), fakes.pipeline(), &fakeCheckpointBuilder{},
		coordination.NewGenerationRepository(backend), pointers, fixedAppClock{now: now},
	)
	events := []string{}
	locker := &fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}}
	first, err := NewSync(log, locker, publisher).Run(ctx, snapshot.SessionID)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	second, err := NewSync(log, locker, publisher).Run(ctx, snapshot.SessionID)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	closed, err := NewClose(log, locker, publisher, &fakeCloseEffects{events: &events}, fixedAppClock{now: now}).Run(ctx, CloseRequest{SessionID: snapshot.SessionID})
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if first.Generation.Generation != 43 || second.Generation.Generation != 44 || closed.Generation.Generation != 45 {
		t.Fatalf("generation progression = %d -> %d -> %d", first.Generation.Generation, second.Generation.Generation, closed.Generation.Generation)
	}
	branchPointer, err := pointers.Read(ctx, snapshot.Capsule, branch)
	if err != nil || branchPointer.Pointer.Generation.Generation != 45 || branchPointer.Pointer.Parent == nil || branchPointer.Pointer.Parent.Generation != 44 {
		t.Fatalf("branch pointer = %#v, %v", branchPointer, err)
	}
	mainPointer, err := pointers.Read(ctx, snapshot.Capsule, mainLineage)
	if err != nil || mainPointer.Pointer.Generation != sourceRef {
		t.Fatalf("main pointer changed = %#v, %v", mainPointer, err)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.OpenedGeneration == nil || *loaded.OpenedGeneration != sourceRef || loaded.CurrentBase == nil || loaded.CurrentBase.Generation != 45 || loaded.State != domain.SessionClosed {
		t.Fatalf("branch journal = %#v pending=%#v error=%v", loaded, pending, err)
	}
}

func TestClosePreservesPublicationWhenCleanupFailsAndLeavesCleanupOnlyRecovery(t *testing.T) {
	t.Parallel()
	log, snapshot := newCloseJournal(t, domain.SessionReadWrite, domain.Materialization{Mode: domain.MaterializationCreated, CleanupPermitted: true})
	events := []string{}
	generation := domain.GenerationRef{Generation: 1, ArchiveSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	result, err := NewClose(log, &fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}}, &fakeCheckpointPublisher{events: &events, result: CheckpointResult{Published: true, Generation: generation}}, &fakeCloseEffects{events: &events, failAt: "services"}, fixedAppClock{now: time.Unix(200, 0)}).Run(context.Background(), CloseRequest{SessionID: snapshot.SessionID})
	if err == nil || !result.PublicationSucceeded || result.CleanupSucceeded || result.RecoveryCommand != "camp recover "+snapshot.SessionID {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	want := []string{"lock:close", "publish:session-close:close", "workspace", "forwarders", "services", "unlock:close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	loaded, pending, loadErr := log.Load(context.Background(), snapshot.SessionID)
	if loadErr != nil || !loaded.Checkpoint.PublicationSucceeded || loaded.Cleanup.State != domain.CleanupFailed || len(pending) != 1 || pending[0].Intent.Transition != "ServicesStopped" {
		t.Fatalf("failed cleanup snapshot = %#v pending=%#v error=%v", loaded, pending, loadErr)
	}
}

func TestCloseReadonlyNeverPublishesOrReleasesLeaseButStillPreservesAdoptedRoot(t *testing.T) {
	t.Parallel()
	log, snapshot := newCloseJournal(t, domain.SessionReadOnly, domain.Materialization{Mode: domain.MaterializationAdopted, CleanupPermitted: false})
	events := []string{}
	result, err := NewClose(log, &fakeOperationLocker{events: &events, token: ports.OperationToken{ID: "lock"}}, &fakeCheckpointPublisher{events: &events}, &fakeCloseEffects{events: &events}, fixedAppClock{now: time.Unix(200, 0)}).Run(context.Background(), CloseRequest{SessionID: snapshot.SessionID, KeepWorkspace: true})
	if err != nil || result.PublicationSucceeded || !result.CleanupSucceeded {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	want := []string{"lock:close", "workspace", "forwarders", "services", "supervisor", "materialization", "unlock:close"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

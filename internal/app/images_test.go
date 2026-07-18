package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	imageops "github.com/joshyorko/camp/internal/images"
	"github.com/joshyorko/camp/internal/ports"
)

func TestImageOperationsListReturnsDeterministicLineageReadModel(t *testing.T) {
	t.Parallel()
	snapshot := imageSessionSnapshot()
	snapshot.State = domain.SessionClosed
	snapshot.Images.Images = []domain.Image{
		{CapturedReference: "camp.test/z:captured", CapturedManifestDigest: "sha256:bbb", OriginalTags: []string{"z:2", "z:1"}},
		{CapturedReference: "camp.test/a:captured", CapturedManifestDigest: "sha256:aaa", OriginalTags: []string{"a:1"}},
	}
	usecase := NewImageOperations(&fakeImageJournal{snapshot: snapshot}, &imageLocker{}, &serveGuard{}, &fakeServeController{}, &fakeImageCapturer{}, &fakeImageRestorer{}, fixedAppClock{now: time.Unix(200, 0)})

	result, err := usecase.List(context.Background(), SessionSelector{SessionID: snapshot.SessionID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.SessionID != snapshot.SessionID || result.Capsule != snapshot.Capsule || result.Branch != snapshot.Lineage.Branch || result.BaseGeneration != 7 || result.BaseDigest != "sha256:base" {
		t.Fatalf("List() lineage = %#v", result)
	}
	if got := []string{result.Images[0].CapturedReference, result.Images[1].CapturedReference}; !reflect.DeepEqual(got, []string{"camp.test/a:captured", "camp.test/z:captured"}) {
		t.Fatalf("List() images = %#v", got)
	}
	if !reflect.DeepEqual(result.Images[1].OriginalTags, []string{"z:1", "z:2"}) {
		t.Fatalf("List() tags = %#v", result.Images[1].OriginalTags)
	}
}

func TestImageOperationsCaptureGuardsObservesAndPersistsInventoryAroundEffect(t *testing.T) {
	t.Parallel()
	snapshot := imageSessionSnapshot()
	order := []string{}
	journal := &fakeImageJournal{snapshot: snapshot, order: &order}
	locker := &imageLocker{order: &order}
	guard := &imageGuard{order: &order}
	controller := &imageController{order: &order}
	wantInventory := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(300, 0), Images: []domain.Image{{CapturedReference: "127.0.0.1:5000/camp/app:captured", CapturedManifestDigest: "sha256:new"}}}
	capturer := &fakeImageCapturer{order: &order, result: wantInventory}
	usecase := NewImageOperations(journal, locker, guard, controller, capturer, &fakeImageRestorer{}, fixedAppClock{now: time.Unix(200, 0)})

	result, err := usecase.Capture(context.Background(), SessionSelector{SessionID: snapshot.SessionID}, nil)
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if !reflect.DeepEqual(result.Images, imageReadModels(wantInventory.Images)) || !reflect.DeepEqual(journal.snapshot.Images, wantInventory) {
		t.Fatalf("Capture() result=%#v persisted=%#v", result, journal.snapshot.Images)
	}
	wantOrder := []string{"lock", "load", "guard", "observe", "intent:ImagesCaptured", "capture", "fact:ImagesCaptured", "unlock"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("Capture() order = %#v, want %#v", order, wantOrder)
	}
	if capturer.request.Scope.Context != snapshot.Workspace.Context || capturer.request.Scope.WorkspaceID != snapshot.Workspace.ID || capturer.request.Capsule != snapshot.Capsule || capturer.request.Previous.GeneratedAt != snapshot.Images.GeneratedAt {
		t.Fatalf("Capture() request = %#v", capturer.request)
	}
}

func TestImageOperationsRestoreUsesOnlyPersistedLineageInventory(t *testing.T) {
	t.Parallel()
	snapshot := imageSessionSnapshot()
	order := []string{}
	journal := &fakeImageJournal{snapshot: snapshot, order: &order}
	restorer := &fakeImageRestorer{order: &order, result: imageops.RestoreResult{Restored: 1, Tags: 2}}
	usecase := NewImageOperations(journal, &imageLocker{order: &order}, &imageGuard{order: &order}, &imageController{order: &order}, &fakeImageCapturer{}, restorer, fixedAppClock{now: time.Unix(200, 0)})

	result, err := usecase.Restore(context.Background(), SessionSelector{SessionID: snapshot.SessionID})
	if err != nil || result.Restored != 1 || result.Tags != 2 {
		t.Fatalf("Restore() = %#v, %v", result, err)
	}
	wantOrder := []string{"lock", "load", "guard", "observe", "intent:ImagesRestored", "restore", "fact:ImagesRestored", "unlock"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("Restore() order = %#v, want %#v", order, wantOrder)
	}
	if !reflect.DeepEqual(restorer.request.Inventory, snapshot.Images) || restorer.request.Scope.WorkspaceID != snapshot.Workspace.ID {
		t.Fatalf("Restore() request = %#v", restorer.request)
	}
}

func TestImageOperationsCaptureReconcilesItsExactPendingIntent(t *testing.T) {
	t.Parallel()
	snapshot := imageSessionSnapshot()
	order := []string{}
	pending := ports.IntentRecord{ID: "pending-capture", SessionID: snapshot.SessionID, Transition: "ImagesCaptured", Attempt: 1, Timestamp: time.Unix(150, 0)}
	journal := &fakeImageJournal{snapshot: snapshot, order: &order, pending: []ports.PendingIntent{{Intent: pending}}}
	inventory := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(300, 0), Images: []domain.Image{{CapturedReference: "127.0.0.1:5000/camp/app:captured", CapturedManifestDigest: "sha256:new"}}}
	usecase := NewImageOperations(journal, &imageLocker{order: &order}, &imageGuard{order: &order}, &imageController{order: &order}, &fakeImageCapturer{order: &order, result: inventory}, &fakeImageRestorer{}, fixedAppClock{now: time.Unix(200, 0)})

	if _, err := usecase.Capture(context.Background(), SessionSelector{SessionID: snapshot.SessionID}, nil); err != nil {
		t.Fatalf("Capture(reconcile) error = %v", err)
	}
	wantOrder := []string{"lock", "load", "guard", "observe", "capture", "fact:ImagesCaptured", "unlock"}
	if !reflect.DeepEqual(order, wantOrder) || journal.lastFact.IntentID != pending.ID {
		t.Fatalf("Capture(reconcile) order=%#v fact=%#v", order, journal.lastFact)
	}
}

func TestImageOperationsCaptureReturnsCanonicalIdentityDriftError(t *testing.T) {
	t.Parallel()
	listed := imageSessionSnapshot()
	loaded := listed
	loaded.Lineage.Branch = "drifted"
	order := []string{}
	journal := &fakeImageJournal{snapshot: loaded, listed: &listed, order: &order}
	usecase := NewImageOperations(journal, &imageLocker{order: &order}, &imageGuard{order: &order}, &imageController{order: &order}, &fakeImageCapturer{order: &order}, &fakeImageRestorer{}, fixedAppClock{now: time.Unix(200, 0)})

	if _, err := usecase.Capture(context.Background(), SessionSelector{SessionID: listed.SessionID}, nil); !errors.Is(err, ErrRecoveryIdentityChanged) {
		t.Fatalf("Capture() error = %v, want ErrRecoveryIdentityChanged", err)
	}
	if want := []string{"lock", "load", "unlock"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("Capture() order = %#v, want %#v", order, want)
	}
}

func imageSessionSnapshot() domain.JournalSnapshot {
	snapshot := readModelSnapshot()
	snapshot.SchemaVersion = domain.SchemaVersion
	snapshot.Workspace = domain.WorkspaceRecord{ID: "brain.devpod", Context: "docker", Provider: "docker"}
	snapshot.CurrentBase = &domain.GenerationRef{Generation: 7, ArchiveSHA256: "sha256:base"}
	snapshot.Images = domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(100, 0), Images: []domain.Image{{CapturedReference: "127.0.0.1:5000/camp/old:captured", CapturedManifestDigest: "sha256:old"}}}
	snapshot.Services = []domain.ServiceUnitRecord{{Name: "registry", DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady, Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: 5000}, Child: domain.ProcessRecord{Argv: []string{"/opt/hauler", "store", "serve", "registry", "--directory", "/overlay"}}}}
	return snapshot
}

type fakeImageJournal struct {
	snapshot domain.JournalSnapshot
	listed   *domain.JournalSnapshot
	order    *[]string
	pending  []ports.PendingIntent
	lastFact ports.FactRecord
}

func (f *fakeImageJournal) Create(context.Context, domain.JournalSnapshot) error { return nil }
func (f *fakeImageJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	if f.listed != nil {
		return []domain.JournalSnapshot{*f.listed}, nil
	}
	return []domain.JournalSnapshot{f.snapshot}, nil
}
func (f *fakeImageJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	if f.order != nil {
		*f.order = append(*f.order, "load")
	}
	return f.snapshot, f.pending, nil
}
func (f *fakeImageJournal) RecordIntent(_ context.Context, intent ports.IntentRecord) error {
	if f.order != nil {
		*f.order = append(*f.order, "intent:"+intent.Transition)
	}
	return nil
}
func (f *fakeImageJournal) RecordFact(_ context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	if f.order != nil {
		*f.order = append(*f.order, "fact:"+fact.Transition)
	}
	f.snapshot = snapshot
	f.lastFact = fact
	return nil
}

type imageLocker struct{ order *[]string }

func (l *imageLocker) Acquire(_ context.Context, owner ports.OperationOwner) (ports.OperationToken, error) {
	if l.order != nil {
		*l.order = append(*l.order, "lock")
	}
	return ports.OperationToken{Owner: owner}, nil
}
func (l *imageLocker) Release(context.Context, ports.OperationToken) error {
	if l.order != nil {
		*l.order = append(*l.order, "unlock")
	}
	return nil
}

type imageGuard struct{ order *[]string }

func (g *imageGuard) Revalidate(context.Context, domain.JournalSnapshot, []ports.PendingIntent) error {
	if g.order != nil {
		*g.order = append(*g.order, "guard")
	}
	return nil
}

type imageController struct{ order *[]string }

func (c *imageController) Observe(_ context.Context, record domain.ServiceUnitRecord) (supervisor.UnitObservation, error) {
	if c.order != nil {
		*c.order = append(*c.order, "observe")
	}
	return supervisor.UnitObservation{State: supervisor.UnitLive, Record: record}, nil
}

type fakeImageCapturer struct {
	order   *[]string
	request imageops.CaptureRequest
	result  domain.ImageInventory
}

func (f *fakeImageCapturer) Capture(_ context.Context, request imageops.CaptureRequest) (domain.ImageInventory, error) {
	if f.order != nil {
		*f.order = append(*f.order, "capture")
	}
	f.request = request
	return f.result, nil
}

type fakeImageRestorer struct {
	order   *[]string
	request imageops.RestoreRequest
	result  imageops.RestoreResult
}

func (f *fakeImageRestorer) Restore(_ context.Context, request imageops.RestoreRequest) (imageops.RestoreResult, error) {
	if f.order != nil {
		*f.order = append(*f.order, "restore")
	}
	f.request = request
	return f.result, nil
}

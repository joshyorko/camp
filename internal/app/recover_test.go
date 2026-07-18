package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestRecoverObservesRevalidatesAndObservesAgainBeforeLifecycleEffect(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.State = domain.SessionRecovering
	snapshot.Services = []domain.ServiceUnitRecord{{Name: "registry", DesiredState: domain.RuntimeDesiredRunning}}
	evidence := SessionEvidence{Services: map[string]ServiceEvidence{"registry": {Helper: ProcessIdentityAbsent, Child: ProcessIdentityAbsent}}}
	order := []string{}
	journal := &fakeRecoveryJournal{snapshot: snapshot, order: &order}
	observer := &orderedRecoveryObserver{order: &order, evidence: []SessionEvidence{evidence, evidence, evidence}}
	guard := &fakeRecoveryGuard{order: &order}
	lifecycle := &fakeRecoveryReconciler{order: &order, result: func() domain.JournalSnapshot {
		next := snapshot
		next.State = domain.SessionOpen
		return next
	}()}
	cleanup := &fakeRecoveryReconciler{}
	usecase := NewRecover(journal, observer, guard, lifecycle, cleanup)

	result, err := usecase.Run(context.Background(), SessionSelector{SessionID: snapshot.SessionID})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Action != RecoveryActionLifecycle || result.Session.State != string(domain.SessionOpen) {
		t.Fatalf("Run() = %#v", result)
	}
	wantOrder := []string{"observe", "load", "guard", "observe", "lifecycle", "observe"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("operation order = %#v, want %#v", order, wantOrder)
	}
	if cleanup.calls != 0 {
		t.Fatalf("cleanup reconciler calls = %d", cleanup.calls)
	}
}

func TestRecoverRoutesFailedCleanupWithoutRepeatingPublication(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.Cleanup = domain.Cleanup{State: domain.CleanupFailed, LastErr: "stop failed"}
	snapshot.Checkpoint.PublicationSucceeded = true
	journal := &fakeRecoveryJournal{snapshot: snapshot, pending: []ports.PendingIntent{{Intent: ports.IntentRecord{Transition: "ServicesStopped"}}}}
	observer := &orderedRecoveryObserver{evidence: []SessionEvidence{{}, {}, {}}}
	lifecycle := &fakeRecoveryReconciler{}
	cleanup := &fakeRecoveryReconciler{result: func() domain.JournalSnapshot {
		next := snapshot
		next.State = domain.SessionClosed
		next.Cleanup = domain.Cleanup{State: domain.CleanupSucceeded}
		return next
	}()}
	usecase := NewRecover(journal, observer, &fakeRecoveryGuard{}, lifecycle, cleanup)

	result, err := usecase.Run(context.Background(), SessionSelector{SessionID: snapshot.SessionID})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Action != RecoveryActionCleanup || result.Session.Publication.Condition != PublicationPublished || result.Session.Cleanup.Condition != CleanupSucceeded {
		t.Fatalf("Run() = %#v", result)
	}
	if lifecycle.calls != 0 || cleanup.calls != 1 {
		t.Fatalf("reconciler calls lifecycle=%d cleanup=%d", lifecycle.calls, cleanup.calls)
	}
}

func TestRecoverRejectsSessionIdentityDriftBeforeEffect(t *testing.T) {
	t.Parallel()
	listed := readModelSnapshot()
	listed.State = domain.SessionRecovering
	loaded := listed
	loaded.Workspace.ID = "different-workspace"
	journal := &fakeRecoveryJournal{snapshot: loaded, listed: &listed}
	reconciler := &fakeRecoveryReconciler{}
	usecase := NewRecover(journal, &orderedRecoveryObserver{evidence: []SessionEvidence{{}}}, &fakeRecoveryGuard{}, reconciler, reconciler)

	if _, err := usecase.Run(context.Background(), SessionSelector{SessionID: listed.SessionID}); !errors.Is(err, ErrRecoveryIdentityChanged) {
		t.Fatalf("Run() error = %v, want ErrRecoveryIdentityChanged", err)
	}
	if reconciler.calls != 0 {
		t.Fatalf("reconciler calls = %d", reconciler.calls)
	}
}

func TestRecoverRejectsChangedObservationBeforeEffect(t *testing.T) {
	t.Parallel()
	snapshot := readModelSnapshot()
	snapshot.State = domain.SessionRecovering
	snapshot.Services = []domain.ServiceUnitRecord{{Name: "registry"}}
	first := SessionEvidence{Services: map[string]ServiceEvidence{"registry": {Helper: ProcessIdentityMatch, Child: ProcessIdentityMatch}}}
	changed := SessionEvidence{Services: map[string]ServiceEvidence{"registry": {Helper: ProcessIdentityReused, Child: ProcessIdentityMatch}}}
	reconciler := &fakeRecoveryReconciler{}
	usecase := NewRecover(&fakeRecoveryJournal{snapshot: snapshot}, &orderedRecoveryObserver{evidence: []SessionEvidence{first, changed}}, &fakeRecoveryGuard{}, reconciler, reconciler)

	if _, err := usecase.Run(context.Background(), SessionSelector{SessionID: snapshot.SessionID}); !errors.Is(err, ErrRecoveryObservationChanged) {
		t.Fatalf("Run() error = %v, want ErrRecoveryObservationChanged", err)
	}
	if reconciler.calls != 0 {
		t.Fatalf("reconciler calls = %d", reconciler.calls)
	}
}

func TestRecoverySafetyGuardRevalidatesOwnershipAndWriterLease(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 18, 15, 0, 0, 0, time.UTC)
	snapshot := readModelSnapshot()
	snapshot.SchemaVersion = domain.SchemaVersion
	snapshot.Materialization.OwnershipMarker = "owned"
	snapshot.Lease = domain.LeaseRecord{
		Lease: &domain.WriterLease{
			SchemaVersion: domain.SchemaVersion, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage,
			SessionID: snapshot.SessionID, Machine: "controller", CreatedAt: now.Add(-time.Hour),
			HeartbeatAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour),
		},
		Revision: "lease-revision",
	}
	ownership := &recoveryOwnershipFake{}
	leases := &recoveryLeaseValidatorFake{}
	guard := NewRecoverySafetyGuard(ownership, leases, fixedAppClock{now: now})

	if err := guard.Revalidate(context.Background(), snapshot, nil); err != nil {
		t.Fatalf("Revalidate() error = %v", err)
	}
	if ownership.materialization != snapshot.Materialization {
		t.Fatalf("ownership materialization = %#v", ownership.materialization)
	}
	if leases.token.Lease != *snapshot.Lease.Lease || leases.token.Revision != ports.Revision(snapshot.Lease.Revision) || !leases.now.Equal(now) {
		t.Fatalf("lease validation = %#v at %v", leases.token, leases.now)
	}
}

type fakeRecoveryJournal struct {
	snapshot domain.JournalSnapshot
	listed   *domain.JournalSnapshot
	pending  []ports.PendingIntent
	order    *[]string
}

func (f *fakeRecoveryJournal) Create(context.Context, domain.JournalSnapshot) error   { return nil }
func (f *fakeRecoveryJournal) RecordIntent(context.Context, ports.IntentRecord) error { return nil }
func (f *fakeRecoveryJournal) RecordFact(context.Context, ports.FactRecord, domain.JournalSnapshot) error {
	return nil
}
func (f *fakeRecoveryJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	if f.order != nil {
		*f.order = append(*f.order, "load")
	}
	return f.snapshot, f.pending, nil
}
func (f *fakeRecoveryJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	if f.listed != nil {
		return []domain.JournalSnapshot{*f.listed}, nil
	}
	return []domain.JournalSnapshot{f.snapshot}, nil
}

type orderedRecoveryObserver struct {
	order    *[]string
	evidence []SessionEvidence
	calls    int
}

func (f *orderedRecoveryObserver) Observe(context.Context, domain.JournalSnapshot) (SessionEvidence, error) {
	if f.order != nil {
		*f.order = append(*f.order, "observe")
	}
	index := f.calls
	f.calls++
	if index >= len(f.evidence) {
		return SessionEvidence{}, errors.New("unexpected observation")
	}
	return f.evidence[index], nil
}

type fakeRecoveryGuard struct{ order *[]string }

func (f *fakeRecoveryGuard) Revalidate(context.Context, domain.JournalSnapshot, []ports.PendingIntent) error {
	if f.order != nil {
		*f.order = append(*f.order, "guard")
	}
	return nil
}

type fakeRecoveryReconciler struct {
	order  *[]string
	result domain.JournalSnapshot
	calls  int
}

type recoveryOwnershipFake struct{ materialization domain.Materialization }

func (f *recoveryOwnershipFake) Revalidate(materialization domain.Materialization) error {
	f.materialization = materialization
	return nil
}

type recoveryLeaseValidatorFake struct {
	token coordination.LeaseToken
	now   time.Time
}

func (f *recoveryLeaseValidatorFake) Revalidate(_ context.Context, token coordination.LeaseToken, now time.Time) error {
	f.token = token
	f.now = now
	return nil
}

func (f *fakeRecoveryReconciler) Reconcile(context.Context, string) (domain.JournalSnapshot, error) {
	f.calls++
	if f.order != nil {
		*f.order = append(*f.order, "lifecycle")
	}
	return f.result, nil
}

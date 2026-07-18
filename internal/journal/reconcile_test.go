package journal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestReconcileRequiresExplicitObserverAndRecordsObservedFact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-a", State: domain.SessionOpening, Lease: domain.LeaseRecord{Revision: "r1"}}
	if err := store.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	intent := ports.IntentRecord{ID: "start-registry", SessionID: snapshot.SessionID, Transition: "ServiceStart", Attempt: 1, Timestamp: time.Unix(10, 0).UTC()}
	if err := store.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}

	if _, err := Reconcile(ctx, store, snapshot.SessionID, nil); !errors.Is(err, ErrUnknownTransition) {
		t.Fatalf("Reconcile(nil) error = %v, want ErrUnknownTransition", err)
	}
	called := 0
	reconciled, err := Reconcile(ctx, store, snapshot.SessionID, map[string]Observer{
		"ServiceStart": func(ctx context.Context, current domain.JournalSnapshot, pending ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
			called++
			concurrent, _, err := store.Load(ctx, snapshot.SessionID)
			if err != nil {
				return ports.FactRecord{}, current, err
			}
			concurrent.Lease.Revision = "r2"
			leaseIntent := ports.IntentRecord{ID: "lease-renew", SessionID: snapshot.SessionID, Transition: "LeaseRenewed", Attempt: 1, Timestamp: time.Unix(10, 0).UTC()}
			if err := store.RecordIntent(ctx, leaseIntent); err != nil {
				return ports.FactRecord{}, current, err
			}
			leaseFact := ports.FactRecord{IntentID: leaseIntent.ID, SessionID: snapshot.SessionID, Transition: leaseIntent.Transition, Timestamp: leaseIntent.Timestamp}
			if err := store.RecordFact(ctx, leaseFact, concurrent); err != nil {
				return ports.FactRecord{}, current, err
			}
			current.State = domain.SessionOpen
			return ports.FactRecord{IntentID: pending.ID, SessionID: pending.SessionID, Transition: pending.Transition, Timestamp: time.Unix(11, 0).UTC()}, current, nil
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if called != 1 || reconciled.State != domain.SessionOpen || reconciled.Lease.Revision != "r2" {
		t.Fatalf("called=%d snapshot=%#v", called, reconciled)
	}
	_, pending, err := store.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("Load() pending = %#v, error = %v", pending, err)
	}
}

func TestReconcileReturnsPointerBaselineProducedByFactReducer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	opened := domain.GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("b", 64)}
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
		OpenedGeneration: &opened, CurrentBase: &opened, State: domain.SessionOpen,
	}
	if err := store.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	intent := ports.IntentRecord{ID: "pointer-43", SessionID: snapshot.SessionID, Transition: "PointerCommitted", Attempt: 1, Timestamp: time.Unix(10, 0).UTC()}
	if err := store.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	next := domain.GenerationRef{Generation: 43, ArchiveSHA256: strings.Repeat("c", 64)}
	pointer := domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage, Generation: next, Parent: &opened,
		ObjectKey: snapshot.Capsule + "/generations/43-" + next.ArchiveSHA256 + ".tar.zst", Size: 10, CreatedAt: time.Unix(11, 0).UTC(), SessionID: snapshot.SessionID,
	}
	reconciled, err := Reconcile(ctx, store, snapshot.SessionID, map[string]Observer{
		"PointerCommitted": func(_ context.Context, current domain.JournalSnapshot, pending ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
			return ports.FactRecord{
				IntentID: pending.ID, SessionID: pending.SessionID, Transition: pending.Transition, Timestamp: time.Unix(11, 0).UTC(),
				Pointer: &ports.PointerCommit{Pointer: pointer, Revision: "revision-43"},
			}, current, nil
		},
	})
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciled.CurrentBase == nil || *reconciled.CurrentBase != next || reconciled.CurrentPointer == nil || reconciled.CurrentPointer.Generation != next || reconciled.ExpectedPointerRevision != "revision-43" {
		t.Fatalf("reconciled baseline = %#v", reconciled)
	}
}

package coordination_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestLeaseRepositoryScopesLifecycleToLineage(t *testing.T) {
	store := newObjectStore(t)
	pointers := coordination.NewPointerRepository(store)
	leases := coordination.NewLeaseRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	mainRef := generationRef(42, "a")
	mainPointer, err := pointers.Create(ctx, pointerFixture("second-brain", domain.Lineage{Branch: "main"}, mainRef, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	branchRef := generationRef(42, "b")
	branchPointer, err := pointers.Create(ctx, pointerFixture("second-brain", domain.Lineage{Branch: "feature-safe"}, branchRef, &mainRef, now))
	if err != nil {
		t.Fatal(err)
	}

	mainToken, err := leases.Acquire(ctx, "second-brain", mainPointer.Pointer.Lineage, coordination.LeaseOwner{SessionID: "main-session", Machine: "bluefin"}, &mainPointer, now, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	branchToken, err := leases.Acquire(ctx, "second-brain", branchPointer.Pointer.Lineage, coordination.LeaseOwner{SessionID: "branch-session", Machine: "homelab"}, &branchPointer, now, 2*time.Minute)
	if err != nil {
		t.Fatalf("branch lease was blocked by main: %v", err)
	}
	if mainToken.Lease.OpenedGeneration == nil || *mainToken.Lease.OpenedGeneration != mainRef {
		t.Fatalf("main opened generation = %#v", mainToken.Lease.OpenedGeneration)
	}
	if _, err := leases.Acquire(ctx, "second-brain", mainPointer.Pointer.Lineage, coordination.LeaseOwner{SessionID: "other", Machine: "other"}, &mainPointer, now.Add(time.Minute), 2*time.Minute); !errors.Is(err, coordination.ErrLeaseHeld) {
		t.Fatalf("second live acquire error = %v, want lease held", err)
	}
	if err := leases.Revalidate(ctx, mainToken, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	renewed, err := leases.Renew(ctx, mainToken, now.Add(time.Minute), 3*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Revision == mainToken.Revision || !renewed.Lease.ExpiresAt.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("renewed token = %#v", renewed)
	}
	if err := leases.Revalidate(ctx, mainToken, now.Add(time.Minute)); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatalf("stale token revalidation error = %v, want lease lost", err)
	}
	if err := leases.Release(ctx, mainToken); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatalf("stale token release error = %v, want lease lost", err)
	}
	if err := leases.Release(ctx, renewed); err != nil {
		t.Fatal(err)
	}
	mainLeaseKey, _ := mainPointer.Pointer.Lineage.LeaseKey("second-brain")
	if _, err := store.Head(ctx, mainLeaseKey); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("released main lease still exists: %v", err)
	}
	if err := leases.Revalidate(ctx, branchToken, now.Add(time.Minute)); err != nil {
		t.Fatalf("branch lease changed with main lifecycle: %v", err)
	}
}

func TestLeaseRepositoryOnlyTakesOverAnExpiredLeaseWithCAS(t *testing.T) {
	store := newObjectStore(t)
	leases := coordination.NewLeaseRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	lineage := domain.Lineage{Branch: "main"}
	first, err := leases.Acquire(ctx, "second-brain", lineage, coordination.LeaseOwner{SessionID: "first", Machine: "bluefin"}, nil, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	second, err := leases.Acquire(ctx, "second-brain", lineage, coordination.LeaseOwner{SessionID: "second", Machine: "homelab"}, nil, now.Add(time.Minute), 2*time.Minute)
	if err != nil {
		t.Fatalf("expired takeover: %v", err)
	}
	if second.Lease.SessionID != "second" || second.Revision == first.Revision {
		t.Fatalf("takeover token = %#v", second)
	}
	if err := leases.Revalidate(ctx, first, now.Add(time.Minute)); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatalf("old owner error = %v, want lease lost", err)
	}
	if err := leases.Revalidate(ctx, second, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseRepositoryRevalidatesTimeValuesFromTimeNow(t *testing.T) {
	store := newObjectStore(t)
	leases := coordination.NewLeaseRepository(store)
	now := time.Now()
	token, err := leases.Acquire(context.Background(), "second-brain", domain.Lineage{Branch: "main"}, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, nil, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := leases.Revalidate(context.Background(), token, now.Add(time.Second)); err != nil {
		t.Fatalf("revalidate lease containing monotonic time: %v", err)
	}
}

func TestLeaseRepositoryRejectsHeartbeatClockRollback(t *testing.T) {
	store := newObjectStore(t)
	leases := coordination.NewLeaseRepository(store)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	token, err := leases.Acquire(context.Background(), "second-brain", domain.Lineage{Branch: "main"}, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, nil, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leases.Renew(context.Background(), token, now.Add(-time.Second), time.Minute); !errors.Is(err, coordination.ErrInvalidDocument) {
		t.Fatalf("clock rollback renewal error = %v, want invalid document", err)
	}
}

func TestLeaseRepositoryRejectsBackdatedRevalidation(t *testing.T) {
	store := newObjectStore(t)
	leases := coordination.NewLeaseRepository(store)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	token, err := leases.Acquire(context.Background(), "second-brain", domain.Lineage{Branch: "main"}, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, nil, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := leases.Revalidate(context.Background(), token, now.Add(-time.Second)); !errors.Is(err, coordination.ErrInvalidDocument) {
		t.Fatalf("backdated revalidation error = %v, want invalid document", err)
	}
}

func TestLeaseRepositoryValidatesTheObservedPointerBeforeAcquiring(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*coordination.PointerRecord)
	}{
		{name: "empty revision", mutate: func(record *coordination.PointerRecord) { record.Revision = "" }},
		{name: "invalid generation", mutate: func(record *coordination.PointerRecord) { record.Pointer.Generation.ArchiveSHA256 = "invalid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newObjectStore(t)
			leases := coordination.NewLeaseRepository(store)
			now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
			pointer := coordination.PointerRecord{
				Pointer:  pointerFixture("second-brain", domain.Lineage{Branch: "main"}, generationRef(42, "a"), nil, now),
				Revision: "pointer-revision",
			}
			test.mutate(&pointer)
			_, err := leases.Acquire(context.Background(), "second-brain", pointer.Pointer.Lineage, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, &pointer, now, time.Minute)
			if !errors.Is(err, coordination.ErrInvalidDocument) {
				t.Fatalf("Acquire error = %v, want invalid document", err)
			}
			leaseKey, _ := pointer.Pointer.Lineage.LeaseKey("second-brain")
			if _, headErr := store.Head(context.Background(), leaseKey); !errors.Is(headErr, ports.ErrNotFound) {
				t.Fatalf("invalid pointer created a lease: %v", headErr)
			}
		})
	}
}

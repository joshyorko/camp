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

type mutateAfterLeaseStore struct {
	ports.ObjectStore
	leaseKey       string
	afterLease     func(context.Context)
	deleteErr      error
	leaseRevision  ports.Revision
	deleteRevision ports.Revision
	deleteCalls    int
}

func (s *mutateAfterLeaseStore) PutConditional(ctx context.Context, key string, body []byte, condition ports.WriteCondition) (ports.ObjectMeta, error) {
	meta, err := s.ObjectStore.PutConditional(ctx, key, body, condition)
	if err == nil && key == s.leaseKey && s.leaseRevision == "" {
		s.leaseRevision = meta.Revision
		if s.afterLease != nil {
			s.afterLease(ctx)
		}
	}
	return meta, err
}

func (s *mutateAfterLeaseStore) DeleteConditional(ctx context.Context, key string, expected ports.Revision) error {
	if key == s.leaseKey {
		s.deleteCalls++
		s.deleteRevision = expected
		if s.deleteErr != nil {
			return s.deleteErr
		}
	}
	return s.ObjectStore.DeleteConditional(ctx, key, expected)
}

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
	branchRef := generationRef(43, "b")
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
	branchLeaseKey, _ := branchPointer.Pointer.Lineage.LeaseKey("second-brain")
	if mainLeaseKey != "second-brain/leases/writer.json" {
		t.Fatalf("main lease key = %q", mainLeaseKey)
	}
	if branchLeaseKey != "second-brain/branches/feature-safe/leases/writer.json" {
		t.Fatalf("branch lease key = %q", branchLeaseKey)
	}
	if _, err := store.Head(ctx, mainLeaseKey); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("released main lease still exists: %v", err)
	}
	if err := leases.Revalidate(ctx, branchToken, now.Add(time.Minute)); err != nil {
		t.Fatalf("branch lease changed with main lifecycle: %v", err)
	}
}

func TestLeaseRepositoryRevalidatesObservedPointerAfterWinningLeaseCAS(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, ports.ObjectStore) *coordination.PointerRecord
	}{
		{
			name: "stale revision",
			setup: func(t *testing.T, store ports.ObjectStore) *coordination.PointerRecord {
				repository := coordination.NewPointerRepository(store)
				record, err := repository.Create(context.Background(), pointerFixture("second-brain", domain.Lineage{Branch: "main"}, generationRef(42, "a"), nil, time.Now()))
				if err != nil {
					t.Fatal(err)
				}
				record.Revision = "stale-pointer-revision"
				return &record
			},
		},
		{
			name: "fabricated observation",
			setup: func(_ *testing.T, _ ports.ObjectStore) *coordination.PointerRecord {
				return &coordination.PointerRecord{
					Pointer:  pointerFixture("second-brain", domain.Lineage{Branch: "main"}, generationRef(42, "a"), nil, time.Now()),
					Revision: "fabricated-pointer-revision",
				}
			},
		},
		{
			name: "nil observation while pointer exists",
			setup: func(t *testing.T, store ports.ObjectStore) *coordination.PointerRecord {
				repository := coordination.NewPointerRepository(store)
				if _, err := repository.Create(context.Background(), pointerFixture("second-brain", domain.Lineage{Branch: "main"}, generationRef(42, "a"), nil, time.Now())); err != nil {
					t.Fatal(err)
				}
				return nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newObjectStore(t)
			lineage := domain.Lineage{Branch: "main"}
			observed := test.setup(t, store)
			leases := coordination.NewLeaseRepository(store)

			_, err := leases.Acquire(context.Background(), "second-brain", lineage, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, observed, time.Now(), time.Minute)
			if !errors.Is(err, coordination.ErrPointerChanged) {
				t.Fatalf("Acquire error = %v, want pointer changed", err)
			}
			leaseKey, _ := lineage.LeaseKey("second-brain")
			if _, headErr := store.Head(context.Background(), leaseKey); !errors.Is(headErr, ports.ErrNotFound) {
				t.Fatalf("failed acquisition left lease behind: %v", headErr)
			}
		})
	}
}

func TestLeaseRepositoryRevalidatesPointerMutationImmediatelyAfterLeaseCAS(t *testing.T) {
	store := newObjectStore(t)
	pointers := coordination.NewPointerRepository(store)
	lineage := domain.Lineage{Branch: "main"}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	openedRef := generationRef(42, "a")
	opened, err := pointers.Create(context.Background(), pointerFixture("second-brain", lineage, openedRef, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	leaseKey, _ := lineage.LeaseKey("second-brain")
	wrapped := &mutateAfterLeaseStore{ObjectStore: store, leaseKey: leaseKey}
	wrapped.afterLease = func(ctx context.Context) {
		nextRef := generationRef(43, "b")
		if _, err := pointers.CompareAndSwap(ctx, opened, pointerFixture("second-brain", lineage, nextRef, &openedRef, now.Add(time.Minute))); err != nil {
			t.Fatalf("mutate pointer after lease CAS: %v", err)
		}
	}

	_, err = coordination.NewLeaseRepository(wrapped).Acquire(context.Background(), "second-brain", lineage, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, &opened, now, time.Minute)
	if !errors.Is(err, coordination.ErrPointerChanged) {
		t.Fatalf("Acquire error = %v, want pointer changed", err)
	}
	if wrapped.deleteCalls != 1 || wrapped.deleteRevision != wrapped.leaseRevision {
		t.Fatalf("lease cleanup calls = %d at revision %q, want one at %q", wrapped.deleteCalls, wrapped.deleteRevision, wrapped.leaseRevision)
	}
	if _, headErr := store.Head(context.Background(), leaseKey); !errors.Is(headErr, ports.ErrNotFound) {
		t.Fatalf("pointer race left lease behind: %v", headErr)
	}
}

func TestLeaseRepositoryPreservesPostAcquireCleanupErrors(t *testing.T) {
	for _, test := range []struct {
		name      string
		deleteErr error
		wantTyped error
	}{
		{name: "lease conflict", deleteErr: ports.ErrConflict, wantTyped: coordination.ErrLeaseLost},
		{name: "lease disappeared", deleteErr: ports.ErrNotFound, wantTyped: coordination.ErrLeaseLost},
		{name: "ambiguous cleanup", deleteErr: ports.ErrAmbiguous, wantTyped: ports.ErrAmbiguous},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newObjectStore(t)
			pointers := coordination.NewPointerRepository(store)
			lineage := domain.Lineage{Branch: "main"}
			now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
			openedRef := generationRef(42, "a")
			opened, err := pointers.Create(context.Background(), pointerFixture("second-brain", lineage, openedRef, nil, now))
			if err != nil {
				t.Fatal(err)
			}
			leaseKey, _ := lineage.LeaseKey("second-brain")
			wrapped := &mutateAfterLeaseStore{ObjectStore: store, leaseKey: leaseKey, deleteErr: test.deleteErr}
			wrapped.afterLease = func(ctx context.Context) {
				nextRef := generationRef(43, "b")
				if _, err := pointers.CompareAndSwap(ctx, opened, pointerFixture("second-brain", lineage, nextRef, &openedRef, now.Add(time.Minute))); err != nil {
					t.Fatalf("mutate pointer after lease CAS: %v", err)
				}
			}

			_, err = coordination.NewLeaseRepository(wrapped).Acquire(context.Background(), "second-brain", lineage, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, &opened, now, time.Minute)
			if !errors.Is(err, coordination.ErrPointerChanged) || !errors.Is(err, test.wantTyped) {
				t.Fatalf("Acquire error = %v, want pointer changed and %v", err, test.wantTyped)
			}
			if wrapped.deleteCalls != 1 || wrapped.deleteRevision != wrapped.leaseRevision {
				t.Fatalf("lease cleanup calls = %d at revision %q, want one at %q", wrapped.deleteCalls, wrapped.deleteRevision, wrapped.leaseRevision)
			}
		})
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

func TestLeaseRepositoryRenewsExpiredExactTokenWhenNoWriterReplacedIt(t *testing.T) {
	store := newObjectStore(t)
	leases := coordination.NewLeaseRepository(store)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	token, err := leases.Acquire(context.Background(), "second-brain", domain.Lineage{Branch: "main"}, coordination.LeaseOwner{SessionID: "session", Machine: "bluefin"}, nil, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	renewed, err := leases.Renew(context.Background(), token, now.Add(2*time.Minute), 30*time.Minute)
	if err != nil {
		t.Fatalf("renew exact expired token: %v", err)
	}
	if renewed.Revision == token.Revision || !renewed.Lease.HeartbeatAt.Equal(now.Add(2*time.Minute)) || !renewed.Lease.ExpiresAt.Equal(now.Add(32*time.Minute)) {
		t.Fatalf("renewed token = %#v", renewed)
	}
	if err := leases.Revalidate(context.Background(), renewed, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("revalidate reclaimed token: %v", err)
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

func TestLeaseRepositoryAcquiresAbsentBranchFromValidatedSourcePointer(t *testing.T) {
	t.Parallel()
	store := newObjectStore(t)
	pointers := coordination.NewPointerRepository(store)
	leases := coordination.NewLeaseRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	sourceRef := generationRef(42, "a")
	source, err := pointers.Create(ctx, pointerFixture("second-brain", domain.Lineage{Branch: "main"}, sourceRef, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	branch := domain.Lineage{Branch: "feature-safe"}
	token, err := leases.AcquireBranchFrom(
		ctx,
		"second-brain",
		branch,
		coordination.LeaseOwner{SessionID: "branch-session", Machine: "bluefin"},
		source,
		now.Add(time.Minute),
		2*time.Minute,
	)
	if err != nil {
		t.Fatalf("AcquireBranchFrom() error = %v", err)
	}
	if token.Lease.Lineage != branch || token.Lease.OpenedGeneration == nil || *token.Lease.OpenedGeneration != sourceRef {
		t.Fatalf("branch lease = %#v", token.Lease)
	}
	if _, err := pointers.Read(ctx, "second-brain", branch); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("branch acquisition created or observed a pointer: %v", err)
	}
}

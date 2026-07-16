package coordination_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestPointerRepositoryUsesValidatedMainAndBranchPaths(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewPointerRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	source := generationRef(41, "a")

	main := pointerFixture("second-brain", domain.Lineage{Branch: "main"}, source, nil, now)
	mainRecord, err := repository.Create(ctx, main)
	if err != nil {
		t.Fatal(err)
	}
	branchGeneration := generationRef(42, "b")
	branch := pointerFixture("second-brain", domain.Lineage{Branch: "feature-safe"}, branchGeneration, &source, now.Add(time.Minute))
	branchRecord, err := repository.Create(ctx, branch)
	if err != nil {
		t.Fatal(err)
	}

	mainKey, _ := main.Lineage.PointerKey(main.Capsule)
	branchKey, _ := branch.Lineage.PointerKey(branch.Capsule)
	if mainKey != "second-brain/latest.json" {
		t.Fatalf("main pointer key = %q", mainKey)
	}
	if branchKey != "second-brain/branches/feature-safe/latest.json" {
		t.Fatalf("branch pointer key = %q", branchKey)
	}
	if _, err := store.Head(ctx, mainKey); err != nil {
		t.Fatalf("main pointer key %q: %v", mainKey, err)
	}
	if _, err := store.Head(ctx, branchKey); err != nil {
		t.Fatalf("branch pointer key %q: %v", branchKey, err)
	}
	readMain, err := repository.Read(ctx, main.Capsule, main.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	readBranch, err := repository.Read(ctx, branch.Capsule, branch.Lineage)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(readMain, mainRecord) || !reflect.DeepEqual(readBranch, branchRecord) {
		t.Fatalf("pointer round trip mismatch\nmain: %#v\nbranch: %#v", readMain, readBranch)
	}
	if _, err := repository.Create(ctx, main); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("duplicate main pointer error = %v, want conflict", err)
	}
}

func TestPointerRepositoryRequiresParentsOlderThanTheirChildren(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation uint64
		wantErr    bool
	}{
		{name: "older cross-lineage branch root", generation: 41},
		{name: "self parent", generation: 42, wantErr: true},
		{name: "forward parent", generation: 43, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newObjectStore(t)
			repository := coordination.NewPointerRepository(store)
			child := generationRef(42, "b")
			parent := generationRef(test.generation, "a")
			pointer := pointerFixture("second-brain", domain.Lineage{Branch: "feature-safe"}, child, &parent, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))

			_, err := repository.Create(context.Background(), pointer)
			if test.wantErr {
				if !errors.Is(err, coordination.ErrInvalidDocument) {
					t.Fatalf("Create error = %v, want invalid document", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create with older external-lineage parent: %v", err)
			}
		})
	}
}

func TestPointerRepositoryRejectsPointerOutsideItsGenerationLineage(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewPointerRepository(store)
	ctx := context.Background()
	pointer := pointerFixture("second-brain", domain.Lineage{Branch: "main"}, generationRef(42, "a"), nil, time.Now())
	pointer.ObjectKey = "another-capsule/generations/42-" + pointer.Generation.ArchiveSHA256 + ".tar.zst"
	body, err := json.Marshal(pointer)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := pointer.Lineage.PointerKey(pointer.Capsule)
	if _, err := store.PutConditional(ctx, key, body, ports.WriteCondition{MustBeAbsent: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Read(ctx, pointer.Capsule, pointer.Lineage); !errors.Is(err, coordination.ErrInvalidDocument) {
		t.Fatalf("unsafe pointer error = %v, want invalid document", err)
	}
}

func TestPointerRepositoryRevalidatesAndComparesAndSwaps(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewPointerRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	opened := generationRef(42, "a")
	first, err := repository.Create(ctx, pointerFixture("second-brain", domain.Lineage{Branch: "main"}, opened, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Revalidate(ctx, first); err != nil {
		t.Fatal(err)
	}

	nextRef := generationRef(43, "b")
	next := pointerFixture("second-brain", first.Pointer.Lineage, nextRef, &opened, now.Add(time.Minute))
	second, err := repository.CompareAndSwap(ctx, first, next)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision {
		t.Fatal("pointer CAS did not advance the opaque revision")
	}
	if err := repository.Revalidate(ctx, first); !errors.Is(err, coordination.ErrPointerChanged) {
		t.Fatalf("stale pointer revalidation error = %v, want pointer changed", err)
	}
	staleNextRef := generationRef(44, "c")
	staleNext := pointerFixture("second-brain", first.Pointer.Lineage, staleNextRef, &opened, now.Add(2*time.Minute))
	if _, err := repository.CompareAndSwap(ctx, first, staleNext); !errors.Is(err, coordination.ErrPointerChanged) {
		t.Fatalf("stale pointer CAS error = %v, want pointer changed", err)
	}
}

func TestPointerRepositoryRevalidatesTimeValuesFromTimeNow(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewPointerRepository(store)
	pointer := pointerFixture("second-brain", domain.Lineage{Branch: "main"}, generationRef(42, "a"), nil, time.Now())
	record, err := repository.Create(context.Background(), pointer)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Revalidate(context.Background(), record); err != nil {
		t.Fatalf("revalidate pointer containing monotonic time: %v", err)
	}
}

func TestPointerRepositoryRejectsNonForwardGenerationCAS(t *testing.T) {
	for _, nextRef := range []domain.GenerationRef{generationRef(42, "b"), generationRef(41, "c")} {
		t.Run(fillForGeneration(nextRef), func(t *testing.T) {
			store := newObjectStore(t)
			repository := coordination.NewPointerRepository(store)
			ctx := context.Background()
			now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
			currentRef := generationRef(42, "a")
			current, err := repository.Create(ctx, pointerFixture("second-brain", domain.Lineage{Branch: "main"}, currentRef, nil, now))
			if err != nil {
				t.Fatal(err)
			}
			next := pointerFixture("second-brain", current.Pointer.Lineage, nextRef, &currentRef, now.Add(time.Minute))
			if _, err := repository.CompareAndSwap(ctx, current, next); !errors.Is(err, coordination.ErrInvalidDocument) {
				t.Fatalf("CAS to generation %d error = %v, want invalid document", nextRef.Generation, err)
			}
			if err := repository.Revalidate(ctx, current); err != nil {
				t.Fatalf("rejected CAS changed pointer: %v", err)
			}
		})
	}
}

func TestPointerBaselineAdvancesCurrentWithoutErasingOpenedGeneration(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewPointerRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	openedRef := generationRef(42, "a")
	opened, err := repository.Create(ctx, pointerFixture("second-brain", domain.Lineage{Branch: "main"}, openedRef, nil, now))
	if err != nil {
		t.Fatal(err)
	}
	baseline := coordination.NewPointerBaseline(&opened)

	ref43 := generationRef(43, "b")
	published43, err := repository.CompareAndSwap(ctx, opened, pointerFixture("second-brain", opened.Pointer.Lineage, ref43, &openedRef, now.Add(time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	baseline = baseline.Advance(published43)
	ref44 := generationRef(44, "c")
	published44, err := repository.CompareAndSwap(ctx, published43, pointerFixture("second-brain", opened.Pointer.Lineage, ref44, &ref43, now.Add(2*time.Minute)))
	if err != nil {
		t.Fatal(err)
	}
	baseline = baseline.Advance(published44)

	if baseline.OpenedGeneration == nil || *baseline.OpenedGeneration != openedRef {
		t.Fatalf("opened generation = %#v, want %#v", baseline.OpenedGeneration, openedRef)
	}
	if baseline.CurrentGeneration == nil || *baseline.CurrentGeneration != ref44 {
		t.Fatalf("current generation = %#v, want %#v", baseline.CurrentGeneration, ref44)
	}
	if baseline.ExpectedRevision != published44.Revision {
		t.Fatalf("expected revision = %q, want %q", baseline.ExpectedRevision, published44.Revision)
	}
}

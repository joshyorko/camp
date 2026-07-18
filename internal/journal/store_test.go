package journal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestStoreFsyncsMode0600IntentAndReplaysFactAfterSnapshotRenameCrash(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	failRename := false
	store, err := NewStore(root, WithBeforeSnapshotRename(func() error {
		if failRename {
			return errors.New("injected rename crash")
		}
		return nil
	}))
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}

	opened := domain.GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("b", 64)}
	snapshot := domain.JournalSnapshot{
		SchemaVersion:    domain.SchemaVersion,
		SessionID:        "session-a",
		Capsule:          "second-brain",
		Lineage:          domain.Lineage{Branch: "main"},
		Mode:             domain.SessionReadWrite,
		OpenedGeneration: &opened,
		CurrentBase:      &opened,
		State:            domain.SessionOpen,
		CreatedAt:        time.Unix(100, 0).UTC(),
		UpdatedAt:        time.Unix(100, 0).UTC(),
	}
	if err := store.Create(ctx, snapshot); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	intent := ports.IntentRecord{
		ID:         "intent-pointer-43",
		SessionID:  snapshot.SessionID,
		Transition: "PointerCommitted",
		Attempt:    1,
		Timestamp:  time.Unix(101, 0).UTC(),
		Input:      []byte(`{"generation":43}`),
	}
	if err := store.RecordIntent(ctx, intent); err != nil {
		t.Fatalf("RecordIntent() error = %v", err)
	}

	loaded, pending, err := store.Load(ctx, snapshot.SessionID)
	if err != nil {
		t.Fatalf("Load() after intent error = %v", err)
	}
	if loaded.SessionID != snapshot.SessionID || len(pending) != 1 || pending[0].Intent.ID != intent.ID {
		t.Fatalf("Load() = %#v, %#v; want one pending intent", loaded, pending)
	}

	failRename = true
	next := domain.GenerationRef{Generation: 43, ArchiveSHA256: strings.Repeat("c", 64)}
	pointer := domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion,
		Capsule:       snapshot.Capsule,
		Lineage:       snapshot.Lineage,
		Generation:    next,
		Parent:        &opened,
		ObjectKey:     snapshot.Capsule + "/generations/43-" + next.ArchiveSHA256 + ".tar.zst",
		Size:          10,
		CreatedAt:     time.Unix(102, 0).UTC(),
		SessionID:     snapshot.SessionID,
	}
	fact := ports.FactRecord{
		IntentID:   intent.ID,
		SessionID:  snapshot.SessionID,
		Transition: "PointerCommitted",
		Timestamp:  time.Unix(102, 0).UTC(),
		Pointer: &ports.PointerCommit{
			Pointer:  pointer,
			Revision: "revision-43",
		},
	}
	if err := store.RecordFact(ctx, fact, snapshot); err == nil || err.Error() != "injected rename crash" {
		t.Fatalf("RecordFact() error = %v, want injected rename crash", err)
	}

	replayed, pending, err := store.Load(ctx, snapshot.SessionID)
	if err != nil {
		t.Fatalf("Load() after crash error = %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending = %#v, want none after durable fact", pending)
	}
	if replayed.OpenedGeneration == nil || *replayed.OpenedGeneration != opened {
		t.Fatalf("opened generation = %#v, want immutable %#v", replayed.OpenedGeneration, opened)
	}
	if replayed.CurrentBase == nil || *replayed.CurrentBase != next || replayed.CurrentPointer == nil || !reflect.DeepEqual(*replayed.CurrentPointer, pointer) || replayed.ExpectedPointerRevision != "revision-43" {
		t.Fatalf("replayed baseline = %#v revision %q", replayed.CurrentBase, replayed.ExpectedPointerRevision)
	}

	for _, path := range []string{
		filepath.Join(root, "sessions", snapshot.SessionID, "journal.jsonl"),
		filepath.Join(root, "sessions", snapshot.SessionID, "snapshot.json"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%q) error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode(%q) = %04o, want 0600", path, got)
		}
	}
}

func TestStoreRejectsPointerCommittedFactWithoutCompletePointer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}}
	if err := store.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	intent := ports.IntentRecord{ID: "pointer", SessionID: snapshot.SessionID, Transition: "PointerCommitted", Attempt: 1, Timestamp: time.Unix(100, 0).UTC()}
	if err := store.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	fact := ports.FactRecord{
		IntentID: intent.ID, SessionID: snapshot.SessionID, Transition: intent.Transition, Timestamp: time.Unix(101, 0).UTC(),
		Pointer: &ports.PointerCommit{Revision: "revision", Pointer: domain.LatestPointer{Generation: domain.GenerationRef{Generation: 43}}},
	}
	if err := store.RecordFact(ctx, fact, snapshot); err == nil {
		t.Fatal("RecordFact() accepted an incomplete committed pointer")
	}
}

func TestStoreComposesLeaseRenewalAndServingRefreshFromStaleSnapshots(t *testing.T) {
	for _, order := range [][]string{{"refresh", "lease"}, {"lease", "refresh"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			ctx := context.Background()
			store, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(100, 0).UTC()
			oldIdentity := domain.ProcessIdentity{PID: 11, BootID: "boot", StartTicks: 11}
			newIdentity := domain.ProcessIdentity{PID: 22, BootID: "boot", StartTicks: 22}
			base := domain.JournalSnapshot{
				SchemaVersion: domain.SchemaVersion, SessionID: "session-concurrent", State: domain.SessionOpen,
				Lease:    domain.LeaseRecord{Revision: "r1"},
				Services: []domain.ServiceUnitRecord{{Name: "registry", Child: domain.ProcessRecord{Identity: oldIdentity}}},
			}
			if err := store.Create(ctx, base); err != nil {
				t.Fatal(err)
			}
			intents := map[string]ports.IntentRecord{
				"refresh": {ID: "refresh", SessionID: base.SessionID, Transition: "ServingContentRefreshed", Attempt: 1, Timestamp: now},
				"lease":   {ID: "lease", SessionID: base.SessionID, Transition: "LeaseRenewed", Attempt: 1, Timestamp: now.Add(time.Second)},
			}
			for _, intent := range intents {
				if err := store.RecordIntent(ctx, intent); err != nil {
					t.Fatal(err)
				}
			}
			refreshCandidate := base
			refreshCandidate.Services = []domain.ServiceUnitRecord{{Name: "registry", Child: domain.ProcessRecord{Identity: newIdentity}}}
			leaseCandidate := base
			leaseCandidate.Lease.Revision = "r2"
			candidates := map[string]domain.JournalSnapshot{"refresh": refreshCandidate, "lease": leaseCandidate}
			for _, name := range order {
				intent := intents[name]
				fact := ports.FactRecord{IntentID: intent.ID, SessionID: base.SessionID, Transition: intent.Transition, Timestamp: intent.Timestamp}
				if err := store.RecordFact(ctx, fact, candidates[name]); err != nil {
					t.Fatalf("RecordFact(%s) error = %v", name, err)
				}
			}
			loaded, pending, err := store.Load(ctx, base.SessionID)
			if err != nil || len(pending) != 0 {
				t.Fatalf("Load() error = %v pending=%#v", err, pending)
			}
			if loaded.Lease.Revision != "r2" || loaded.Services[0].Child.Identity != newIdentity {
				t.Fatalf("composed snapshot lease=%q child=%#v", loaded.Lease.Revision, loaded.Services[0].Child.Identity)
			}
		})
	}
}

func TestStoreCreateIsCrashSafeAndIdempotentAtEveryDurableCut(t *testing.T) {
	t.Parallel()
	for _, cut := range []CreateStep{CreateAfterJournalSync, CreateAfterSnapshotSync, CreateAfterCommitSync} {
		cut := cut
		t.Run(string(cut), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			root := t.TempDir()
			now := time.Unix(100, 0).UTC()
			snapshot := domain.JournalSnapshot{
				SchemaVersion: domain.SchemaVersion, SessionID: "session-create", Capsule: "brain", Lineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, State: domain.SessionOpening, CreatedAt: now, UpdatedAt: now,
			}
			store, err := NewStore(root, WithCreateStepHook(func(step CreateStep) error {
				if step == cut {
					return errors.New("injected create crash")
				}
				return nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Create(ctx, snapshot); err == nil || err.Error() != "injected create crash" {
				t.Fatalf("Create() error = %v, want injected crash", err)
			}

			resumed, err := NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := resumed.Create(ctx, snapshot); err != nil {
				t.Fatalf("retry Create() after %s: %v", cut, err)
			}
			loaded, pending, err := resumed.Load(ctx, snapshot.SessionID)
			if err != nil || len(pending) != 0 || !reflect.DeepEqual(loaded, snapshot) {
				t.Fatalf("Load() = %#v pending=%#v error=%v", loaded, pending, err)
			}
			sessions, err := resumed.List(ctx)
			if err != nil || len(sessions) != 1 || sessions[0].SessionID != snapshot.SessionID {
				t.Fatalf("List() = %#v, %v", sessions, err)
			}
		})
	}
}

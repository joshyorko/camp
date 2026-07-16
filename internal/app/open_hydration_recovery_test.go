package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestOpenRemoteResumesPendingHydrationStageWithOriginalPlanWithoutDuplicateIntent(t *testing.T) {
	environment := newRemoteOpenTestEnvironment(t)
	archiveRoot := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(filepath.Join(archiveRoot, ".camp", "runtime", "MemoryD"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, ".camp", "capsule.yaml"), []byte("schemaVersion: 1\nid: brain\ndefaultBranch: main\ncreatedAt: 1970-01-01T00:00:01Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, "README.md"), []byte("remote\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(t.TempDir(), "inner.tar.zst")
	archiveInfo, err := archive.NewTarZstd().Create(context.Background(), archiveRoot, inner)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(inner)
	if err != nil {
		t.Fatal(err)
	}
	digestSum := sha256.Sum256(body)
	digest := hex.EncodeToString(digestSum[:])
	generation := domain.GenerationRef{Generation: 42, ArchiveSHA256: digest}
	pointer := coordination.PointerRecord{Pointer: domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: "brain/generations/42-" + digest + ".tar.zst", Size: archiveInfo.Size,
		CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
	}, Revision: "main-r1"}
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: pointer.Pointer.ObjectKey, MetadataKey: "brain/generations/42-" + digest + ".json", Size: archiveInfo.Size,
		CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
		Verified: domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
	}
	environment.open.deps.Pointers = &recordingOpenPointers{source: pointer}
	environment.open.deps.Generations = &recordingOpenGenerations{metadata: metadata}
	store := &openHydrationStore{body: body, metadata: ports.ObjectMeta{Key: pointer.Pointer.ObjectKey, Size: archiveInfo.Size, SHA256: digest}}
	hauler := &openHydrationHauler{inner: inner}
	crash := errors.New("crash after durable hydration stage creation")
	environment.open.deps.Hydrator = hydration.NewController(store, hauler, archive.NewTarZstd(), environment.ownership, hydration.Hooks{
		Cut: func(phase hydration.Phase) error {
			if phase == hydration.PhaseStageCreated {
				return crash
			}
			return nil
		},
	})
	const sessionID = "pending-hydration-stage-recovery"
	request := OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadOnly, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	}
	if _, err := environment.open.Run(context.Background(), request); !errors.Is(err, crash) {
		t.Fatalf("first Open() error = %v, want injected crash", err)
	}

	snapshot, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Recovery.Hydration == nil {
		t.Fatal("pending session has no durable hydration plan")
	}
	plan := *snapshot.Recovery.Hydration
	if len(pending) != 1 || pending[0].Intent.Transition != "HydrationMaterializationStageCreated" {
		t.Fatalf("pending intents = %#v, want original hydration stage intent", pending)
	}
	originalIntentID := pending[0].Intent.ID

	var replayRequests []hydration.Request
	var replayPhases []hydration.Phase
	replayHydrator := hydration.NewController(store, hauler, archive.NewTarZstd(), environment.ownership, hydration.Hooks{
		Before: func(_ context.Context, phase hydration.Phase, request hydration.Request) error {
			replayPhases = append(replayPhases, phase)
			replayRequests = append(replayRequests, request)
			return nil
		},
	})
	freshDependencies := environment.open.deps
	freshDependencies.Hydrator = replayHydrator
	freshOpen := NewOpen(freshDependencies)
	result, err := freshOpen.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("fresh Open() recovery error = %v", err)
	}
	if result.Snapshot.State != domain.SessionOpen {
		t.Fatalf("recovered session state = %q, want open", result.Snapshot.State)
	}
	if len(replayRequests) == 0 {
		t.Fatal("fresh Open() did not resume hydration")
	}
	for _, replay := range replayRequests {
		if replay.Token != plan.Token || replay.StageRoot != plan.StageRoot || replay.FinalRoot != plan.FinalRoot {
			t.Fatalf("replay hydration plan = token %q stage %q final %q, want original %#v", replay.Token, replay.StageRoot, replay.FinalRoot, plan)
		}
	}
	for _, phase := range replayPhases {
		if phase == hydration.PhaseStageCreated {
			t.Fatalf("durably completed hydration stage was repeated: phases=%v", replayPhases)
		}
	}
	if result.Snapshot.Materialization.OwnershipMarker != plan.Token || result.Snapshot.Materialization.CanonicalPath != plan.FinalRoot {
		t.Fatalf("recovered materialization = %#v, want original hydration plan %#v", result.Snapshot.Materialization, plan)
	}

	journalBody, err := os.ReadFile(filepath.Join(environment.paths.SessionRoot, sessionID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	intentCount, factCount := 0, 0
	for _, line := range bytes.Split(journalBody, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Kind   string              `json:"kind"`
			Intent *ports.IntentRecord `json:"intent"`
			Fact   *ports.FactRecord   `json:"fact"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Intent != nil && entry.Intent.Transition == "HydrationMaterializationStageCreated" {
			intentCount++
			if entry.Intent.ID != originalIntentID {
				t.Fatalf("replacement hydration stage intent = %q, want original %q", entry.Intent.ID, originalIntentID)
			}
		}
		if entry.Fact != nil && entry.Fact.Transition == "HydrationMaterializationStageCreated" {
			factCount++
			if entry.Fact.IntentID != originalIntentID {
				t.Fatalf("hydration stage fact closes %q, want original %q", entry.Fact.IntentID, originalIntentID)
			}
		}
	}
	if intentCount != 1 || factCount != 1 {
		t.Fatalf("hydration stage journal pairs = intents:%d facts:%d, want exactly one original pair", intentCount, factCount)
	}
	_, remaining, err := freshOpen.deps.Journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("pending intents after hydration recovery = %#v", remaining)
	}
}

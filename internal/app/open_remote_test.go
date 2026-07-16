package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

func TestOpenRemoteBranchUsesSourceGenerationAndReentryDoesNotRehydrate(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	request := OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD",
		EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker", Machine: "machine-a",
		Runtime: environment.runtime, Backend: environment.backend,
	}
	first, err := environment.open.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("remote Open() error = %v", err)
	}
	if first.Snapshot.OpenedGeneration == nil || first.Snapshot.OpenedGeneration.Generation != 42 || first.Snapshot.CurrentBase == nil || first.Snapshot.CurrentBase.Generation != 42 {
		t.Fatalf("opened checkpoint = %#v current=%#v", first.Snapshot.OpenedGeneration, first.Snapshot.CurrentBase)
	}
	if environment.leases.branchCalls != 1 || environment.leases.acquireCalls != 0 || environment.leases.owner.SessionID != first.Snapshot.SessionID {
		t.Fatalf("lease calls = branch:%d acquire:%d owner:%#v", environment.leases.branchCalls, environment.leases.acquireCalls, environment.leases.owner)
	}
	if environment.hydrator.calls != 1 || environment.hydrator.request.Generation.Generation != 42 || environment.hydrator.request.Token == "" || len(environment.hydrator.request.Token) != 64 {
		t.Fatalf("hydration request = %#v calls=%d", environment.hydrator.request, environment.hydrator.calls)
	}
	if first.Snapshot.Materialization.Mode != domain.MaterializationCreated || !first.Snapshot.Materialization.CleanupPermitted {
		t.Fatalf("remote materialization = %#v", first.Snapshot.Materialization)
	}
	if len(environment.devpod.ups) != 1 || environment.devpod.ups[0].CampEnvironment == nil || environment.devpod.ups[0].CampEnvironment.Checkpoint != "42" {
		t.Fatalf("DevPod environment = %#v", environment.devpod.ups)
	}
	if environment.devpod.ups[0].Context != "default" || environment.devpod.ups[0].Provider != "docker" {
		t.Fatalf("DevPod selection = %#v", environment.devpod.ups[0])
	}

	second, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("remote re-entry error = %v", err)
	}
	if second.Snapshot.SessionID != first.Snapshot.SessionID || environment.hydrator.calls != 1 || environment.leases.branchCalls != 1 || environment.leases.acquireCalls != 0 || len(environment.devpod.ups) != 1 {
		t.Fatalf("re-entry repeated lifecycle: snapshot=%#v hydrate=%d leases=%#v ups=%d", second.Snapshot, environment.hydrator.calls, environment.leases, len(environment.devpod.ups))
	}
}

func TestOpenJournalsRemoteLeaseAcquisitionIntentAndFact(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "lease-journal-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if result.Snapshot.Lease.Lease == nil || result.Snapshot.Lease.Revision == "" {
		t.Fatalf("lease was not persisted in snapshot: %#v", result.Snapshot.Lease)
	}
	body, err := os.ReadFile(filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"transition":"RemoteLeaseAcquisition"`)) {
		t.Fatalf("journal has no lease acquisition transition: %s", body)
	}
	var receipt struct {
		Machine          string                `json:"machine"`
		OpenedGeneration *domain.GenerationRef `json:"openedGeneration"`
		CreatedAt        time.Time             `json:"createdAt"`
		HeartbeatAt      time.Time             `json:"heartbeatAt"`
		ExpiresAt        time.Time             `json:"expiresAt"`
		BranchSource     bool                  `json:"branchSource"`
		ObservedRevision string                `json:"observedRevision"`
	}
	for _, line := range bytes.Split(body, []byte{'\n'}) {
		var entry struct {
			Kind string            `json:"kind"`
			Fact *ports.FactRecord `json:"fact"`
		}
		if len(line) == 0 || json.Unmarshal(line, &entry) != nil || entry.Kind != "fact" || entry.Fact == nil || entry.Fact.Transition != "RemoteLeaseAcquisition" {
			continue
		}
		if err := json.Unmarshal(entry.Fact.Output, &receipt); err != nil {
			t.Fatal(err)
		}
	}
	if receipt.Machine != "machine-a" || receipt.OpenedGeneration == nil || *receipt.OpenedGeneration != remoteOpenGeneration() ||
		!receipt.CreatedAt.Equal(time.Unix(100, 0).UTC()) || !receipt.HeartbeatAt.Equal(receipt.CreatedAt) || !receipt.ExpiresAt.Equal(receipt.CreatedAt.Add(30*time.Minute)) ||
		!receipt.BranchSource || receipt.ObservedRevision != "main-r1" {
		t.Fatalf("lease receipt = %#v", receipt)
	}
	loaded, pending, err := environment.open.deps.Journal.Load(context.Background(), result.Snapshot.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 || loaded.Lease.Lease == nil || loaded.Lease.Revision == "" {
		t.Fatalf("journal lease state = %#v pending=%#v", loaded.Lease, pending)
	}
}

func TestOpenRejectsMismatchedReturnedLeaseBeforeRecordingFact(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	environment.leases.mutate = func(token *coordination.LeaseToken) {
		token.Lease.OpenedGeneration = &domain.GenerationRef{Generation: 41, ArchiveSHA256: strings.Repeat("b", 64)}
	}
	const sessionID = "mismatched-returned-lease-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err == nil {
		t.Fatal("Open() accepted a lease token for the wrong opened generation")
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("mismatched lease snapshot=%#v pending=%#v error=%v", loaded.Lease, pending, loadErr)
	}
	if environment.hydrator.calls != 0 || len(environment.devpod.ups) != 0 {
		t.Fatalf("effects after mismatched lease: hydrate=%d up=%d", environment.hydrator.calls, len(environment.devpod.ups))
	}
}

func TestOpenReconcilesUnknownRemoteLeaseAcquisitionWithoutReacquiring(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	leases := &unknownOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	environment.open.deps.Leases = leases
	const sessionID = "lease-outcome-unknown-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	before, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("pre-reconciliation snapshot lease = %#v pending=%#v", before.Lease, pending)
	}

	reconciled, err := environment.open.Reconcile(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if reconciled.Lease.Lease == nil || reconciled.Lease.Lease.SessionID != sessionID || reconciled.Lease.Revision != "lease-r1" {
		t.Fatalf("reconciled lease = %#v", reconciled.Lease)
	}
	if reconciled.OpenedGeneration == nil || *reconciled.OpenedGeneration != remoteOpenGeneration() || reconciled.CurrentBase == nil || *reconciled.CurrentBase != remoteOpenGeneration() {
		t.Fatalf("reconciled baseline opened=%#v current=%#v", reconciled.OpenedGeneration, reconciled.CurrentBase)
	}
	if reconciled.CurrentPointer != nil || reconciled.ExpectedPointerRevision != "" || reconciled.Recovery.Source.Kind != domain.SourceDecisionRemote ||
		reconciled.Recovery.Source.Lineage == nil || *reconciled.Recovery.Source.Lineage != (domain.Lineage{Branch: "main"}) ||
		reconciled.Recovery.Source.Generation == nil || *reconciled.Recovery.Source.Generation != remoteOpenGeneration() {
		t.Fatalf("reconciled absent-branch source pointer=%#v revision=%q source=%#v", reconciled.CurrentPointer, reconciled.ExpectedPointerRevision, reconciled.Recovery.Source)
	}
	if leases.branchCalls != 1 || leases.readCalls != 1 {
		t.Fatalf("lease calls after reconciliation = branch:%d read:%d, want branch:1 read:1", leases.branchCalls, leases.readCalls)
	}
	_, pending, err = environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("post-reconciliation pending=%#v error=%v", pending, err)
	}
}

func TestOpenReconcileAbsentLeaseRemainsPendingWithoutReacquiring(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	leases := &absentOutcomeOpenLeases{}
	environment.open.deps.Leases = leases
	const sessionID = "lease-outcome-absent-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	_, err = environment.open.Reconcile(context.Background(), sessionID)
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("Reconcile() error = %v, want ErrNotFound", err)
	}
	if leases.branchCalls != 1 || leases.readCalls != 1 {
		t.Fatalf("lease calls after reconciliation = branch:%d read:%d, want branch:1 read:1", leases.branchCalls, leases.readCalls)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("absent snapshot lease=%#v pending=%#v error=%v", loaded.Lease, pending, loadErr)
	}
}

func TestOpenReconcileRejectsApproximateLeaseWithoutRecordingFact(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*coordination.LeaseToken)
	}{
		{name: "schema", mutate: func(token *coordination.LeaseToken) { token.Lease.SchemaVersion = 0 }},
		{name: "opened generation", mutate: func(token *coordination.LeaseToken) {
			token.Lease.OpenedGeneration = &domain.GenerationRef{Generation: 41, ArchiveSHA256: strings.Repeat("b", 64)}
		}},
		{name: "created time", mutate: func(token *coordination.LeaseToken) { token.Lease.CreatedAt = token.Lease.CreatedAt.Add(time.Second) }},
		{name: "heartbeat time", mutate: func(token *coordination.LeaseToken) {
			token.Lease.HeartbeatAt = token.Lease.HeartbeatAt.Add(time.Second)
		}},
		{name: "expiry terms", mutate: func(token *coordination.LeaseToken) { token.Lease.ExpiresAt = token.Lease.ExpiresAt.Add(time.Second) }},
		{name: "revision", mutate: func(token *coordination.LeaseToken) { token.Revision = "" }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := newRemoteOpenTestEnvironment(t)
			leases := &unknownOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC(), mutate: test.mutate}
			environment.open.deps.Leases = leases
			sessionID := "approximate-lease-" + strings.ReplaceAll(test.name, " ", "-")
			_, err := environment.open.Run(context.Background(), OpenRequest{
				SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
			})
			if !errors.Is(err, ports.ErrAmbiguous) {
				t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
			}
			if _, err = environment.open.Reconcile(context.Background(), sessionID); err == nil {
				t.Fatal("Reconcile() accepted an approximate lease")
			}
			loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
			if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || leases.branchCalls != 1 || leases.readCalls != 1 {
				t.Fatalf("snapshot lease=%#v pending=%#v branch=%d read=%d error=%v", loaded.Lease, pending, leases.branchCalls, leases.readCalls, loadErr)
			}
		})
	}
}

func TestOpenReconcileRejectsPointerDriftWithoutRecordingLeaseFact(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	pointers := &recordingOpenPointers{source: remoteOpenPointer()}
	leases := &unknownOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	environment.open.deps.Pointers = pointers
	environment.open.deps.Leases = leases
	const sessionID = "lease-pointer-drift-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	pointers.source.Revision = "main-r2"
	if _, err = environment.open.Reconcile(context.Background(), sessionID); !errors.Is(err, coordination.ErrPointerChanged) {
		t.Fatalf("Reconcile() error = %v, want ErrPointerChanged", err)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || leases.branchCalls != 1 || leases.readCalls != 0 {
		t.Fatalf("snapshot lease=%#v pending=%#v branch=%d read=%d error=%v", loaded.Lease, pending, leases.branchCalls, leases.readCalls, loadErr)
	}
}

func TestOpenReconcileReadOnlySnapshotNeverObservesOrAcquiresLease(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	pointers := &recordingOpenPointers{source: remoteOpenPointer()}
	leases := &absentOutcomeOpenLeases{}
	environment.open.deps.Pointers = pointers
	environment.open.deps.Leases = leases
	now := time.Unix(100, 0).UTC()
	const sessionID = "read-only-lease-intent-session"
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: sessionID, Capsule: "brain", Lineage: domain.Lineage{Branch: "feature"},
		Mode: domain.SessionReadOnly, State: domain.SessionOpening, CreatedAt: now, UpdatedAt: now,
	}
	if err := environment.open.deps.Journal.Create(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	source := remoteOpenPointer()
	input := openLeaseAcquisitionInput{
		Capsule: "brain", Lineage: snapshot.Lineage, Owner: coordination.LeaseOwner{SessionID: sessionID, Machine: "machine-a"},
		Observed: &source, Source: &source, ObservedRevision: string(source.Revision), BranchSource: true, Now: now, LeaseTTL: time.Minute,
	}
	intent := ports.IntentRecord{ID: transitionID(sessionID, "RemoteLeaseAcquisition"), SessionID: sessionID, Transition: "RemoteLeaseAcquisition", Attempt: 1, Timestamp: now, Input: safeJSON(input)}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.open.Reconcile(context.Background(), sessionID); !errors.Is(err, ErrOpenReadOnlyLease) {
		t.Fatalf("Reconcile() error = %v, want ErrOpenReadOnlyLease", err)
	}
	if len(pointers.calls) != 0 || leases.readCalls != 0 || leases.branchCalls != 0 {
		t.Fatalf("read-only reconciliation effects: pointers=%v lease-read=%d lease-acquire=%d", pointers.calls, leases.readCalls, leases.branchCalls)
	}
}

func TestOpenReconcileRejectsInconsistentBranchSourceIntent(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	pointers := &recordingOpenPointers{source: remoteOpenPointer()}
	now := time.Unix(100, 0).UTC()
	source := remoteOpenPointer()
	opened := source.Pointer.Generation
	const sessionID = "inconsistent-branch-source-session"
	lease := coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "feature"}, SessionID: sessionID, Machine: "machine-a",
		OpenedGeneration: &opened, CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(time.Minute),
	}, Revision: "lease-r1"}
	leases := &unknownOutcomeOpenLeases{token: lease, available: true}
	environment.open.deps.Pointers = pointers
	environment.open.deps.Leases = leases
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: sessionID, Capsule: "brain", Lineage: domain.Lineage{Branch: "feature"},
		Mode: domain.SessionReadWrite, State: domain.SessionOpening, CreatedAt: now, UpdatedAt: now,
	}
	if err := environment.open.deps.Journal.Create(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	observed := source
	observed.Revision = "different-observation-r1"
	input := openLeaseAcquisitionInput{
		Capsule: "brain", Lineage: snapshot.Lineage, Owner: coordination.LeaseOwner{SessionID: sessionID, Machine: "machine-a"},
		Observed: &observed, Source: &source, ObservedRevision: string(observed.Revision), BranchSource: true, Now: now, LeaseTTL: time.Minute,
	}
	intent := ports.IntentRecord{ID: transitionID(sessionID, "RemoteLeaseAcquisition"), SessionID: sessionID, Transition: "RemoteLeaseAcquisition", Attempt: 1, Timestamp: now, Input: safeJSON(input)}
	if err := environment.open.deps.Journal.RecordIntent(context.Background(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := environment.open.Reconcile(context.Background(), sessionID); err == nil {
		t.Fatal("Reconcile() accepted mismatched observed and source pointers")
	}
	_, pending, err := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v error=%v", pending, err)
	}
}

func TestOpenReconciliationRejectsAnotherSessionsLeaseWithoutReacquiring(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	leases := &conflictingOutcomeOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	environment.open.deps.Leases = leases
	const sessionID = "lease-outcome-conflict-session"
	_, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: sessionID, Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend,
	})
	if !errors.Is(err, ports.ErrAmbiguous) {
		t.Fatalf("Open() error = %v, want ErrAmbiguous", err)
	}
	_, err = environment.open.Reconcile(context.Background(), sessionID)
	if !errors.Is(err, coordination.ErrLeaseHeld) {
		t.Fatalf("Reconcile() error = %v, want ErrLeaseHeld", err)
	}
	if leases.branchCalls != 1 || leases.readCalls != 1 {
		t.Fatalf("lease calls after conflict = branch:%d read:%d, want branch:1 read:1", leases.branchCalls, leases.readCalls)
	}
	loaded, pending, loadErr := environment.open.deps.Journal.Load(context.Background(), sessionID)
	if loadErr != nil || loaded.Lease.Lease != nil || len(pending) != 1 || pending[0].Intent.Transition != "RemoteLeaseAcquisition" {
		t.Fatalf("conflict snapshot lease=%#v pending=%#v error=%v", loaded.Lease, pending, loadErr)
	}
}

func TestOpenRemoteJournalAndArtifactsDoNotPersistConfiguredCredentials(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	environment.runtime.AccessToken = "configured-secret"
	result, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "credential-free-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadOnly, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("credential-free Open() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "snapshot.json"),
		filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "journal.jsonl"),
	} {
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(body), "configured-secret") {
			t.Fatalf("credential persisted in %s", path)
		}
	}
}

func TestOpenRemoteUsesHydrationHooksForDurableMaterializationPhases(t *testing.T) {
	t.Parallel()
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
		ObjectKey: "brain/generations/42-" + digest + ".tar.zst", Size: archiveInfo.Size, CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
	}, Revision: "main-r1"}
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: pointer.Pointer.ObjectKey, MetadataKey: "brain/generations/42-" + digest + ".json", Size: archiveInfo.Size, CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
		Verified: domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
	}
	environment.open.deps.Pointers = &recordingOpenPointers{source: pointer}
	environment.open.deps.Generations = &recordingOpenGenerations{metadata: metadata}
	environment.open.deps.Leases = &recordingOpenLeases{generation: generation, now: time.Unix(100, 0).UTC()}
	environment.open.deps.Hydrator = hydration.NewController(
		&openHydrationStore{body: body, metadata: ports.ObjectMeta{Key: pointer.Pointer.ObjectKey, Size: archiveInfo.Size, SHA256: digest}},
		&openHydrationHauler{inner: inner}, archive.NewTarZstd(), environment.ownership, hydration.Hooks{},
	)
	result, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"}, Mode: domain.SessionReadOnly,
		RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if result.Snapshot.Materialization.Mode != domain.MaterializationCreated || result.Target.Relative != "MemoryD" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(result.Snapshot.Materialization.CanonicalPath, "README.md")); err != nil {
		t.Fatalf("hydrated root = %v", err)
	}
	journalBody, err := os.ReadFile(filepath.Join(environment.paths.SessionRoot, result.Snapshot.SessionID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"HydrationMaterializationStageCreated", "HydrationGenerationFetched", "HydrationGenerationLoaded", "HydrationMaterializationExtractComplete", "HydrationMaterializationRenameComplete", "HydrationMaterializationOwnershipFact"} {
		if !bytes.Contains(journalBody, []byte(phase)) {
			t.Fatalf("journal missing hydration phase %q: %s", phase, journalBody)
		}
	}
}

func TestOpenRejectsHydratorResultOutsidePlannedOwnership(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	outside := t.TempDir()
	environment.open.deps.Hydrator = &invalidOpenHydrator{root: outside}
	_, err := environment.open.Run(context.Background(), OpenRequest{
		Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"}, Mode: domain.SessionReadOnly,
		RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal, Context: "default", Provider: "docker",
		Runtime: environment.runtime, Backend: environment.backend,
	})
	if err == nil {
		t.Fatal("Open() accepted a hydrator result outside the planned destination")
	}
	if len(environment.devpod.ups) != 0 {
		t.Fatalf("DevPod started after invalid hydration result: %#v", environment.devpod.ups)
	}
}

type remoteOpenTestEnvironment struct {
	open      *Open
	paths     config.XDGPaths
	backend   config.FileBackend
	runtime   config.Runtime
	ownership *capsule.Ownership
	devpod    *openDevPod
	hydrator  *recordingOpenHydrator
	leases    *recordingOpenLeases
}

func newRemoteOpenTestEnvironment(t *testing.T) remoteOpenTestEnvironment {
	t.Helper()
	home := t.TempDir()
	paths, err := config.ResolveXDGPaths(config.XDGInput{Home: home, Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	backend, err := config.ResolveFileBackend("file://" + filepath.Join(home, "backend"))
	if err != nil {
		t.Fatal(err)
	}
	log, err := journal.NewStore(paths.DataRoot)
	if err != nil {
		t.Fatal(err)
	}
	ownership, err := capsule.NewOwnership(filepath.Dir(paths.DataRoot))
	if err != nil {
		t.Fatal(err)
	}
	events := []string{}
	devpod := &openDevPod{events: &events, folder: "/workspaces/root"}
	runtime := config.Runtime{Bootstrap: config.Bootstrap{Capsule: "brain", RegistryPort: 5000, FileserverPort: 8080}}
	hydrator := &recordingOpenHydrator{ownership: ownership, events: &events}
	leases := &recordingOpenLeases{generation: remoteOpenGeneration(), now: time.Unix(100, 0).UTC()}
	return remoteOpenTestEnvironment{
		paths: paths, backend: backend, runtime: runtime, ownership: ownership, devpod: devpod, hydrator: hydrator, leases: leases,
		open: NewOpen(OpenDependencies{
			Journal: log, Paths: paths, Backend: backend, Ownership: ownership,
			Initializer: &openInitializer{events: &events},
			Pointers:    &recordingOpenPointers{source: remoteOpenPointer()},
			Generations: &recordingOpenGenerations{metadata: remoteOpenMetadata()},
			Leases:      leases, Hydrator: hydrator, DevPod: devpod, Target: &openTargetResolver{events: &events},
			Clock: fixedAppClock{now: time.Unix(100, 0).UTC()},
		}),
	}
}

func remoteOpenGeneration() domain.GenerationRef {
	return domain.GenerationRef{Generation: 42, ArchiveSHA256: strings.Repeat("a", 64)}
}

func remoteOpenPointer() coordination.PointerRecord {
	generation := remoteOpenGeneration()
	return coordination.PointerRecord{Pointer: domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: "brain/generations/42-" + generation.ArchiveSHA256 + ".tar.zst", Size: 123,
		CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
	}, Revision: "main-r1"}
}

func remoteOpenMetadata() domain.GenerationMetadata {
	generation := remoteOpenGeneration()
	return domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: domain.Lineage{Branch: "main"}, Generation: generation,
		ObjectKey: "brain/generations/42-" + generation.ArchiveSHA256 + ".tar.zst", MetadataKey: "brain/generations/42-" + generation.ArchiveSHA256 + ".json",
		Size: 123, CreatedAt: time.Unix(42, 0).UTC(), SessionID: "source-session",
		Verified: domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
	}
}

type recordingOpenPointers struct {
	source coordination.PointerRecord
	calls  []string
}

func (r *recordingOpenPointers) Read(_ context.Context, _ string, lineage domain.Lineage) (coordination.PointerRecord, error) {
	r.calls = append(r.calls, lineage.Branch)
	if lineage.IsMain() {
		return r.source, nil
	}
	return coordination.PointerRecord{}, ports.ErrNotFound
}

func (r *recordingOpenPointers) Revalidate(ctx context.Context, observed coordination.PointerRecord) error {
	current, err := r.Read(ctx, observed.Pointer.Capsule, observed.Pointer.Lineage)
	if err != nil {
		return err
	}
	if current.Revision != observed.Revision || !bytes.Equal(safeJSON(current.Pointer), safeJSON(observed.Pointer)) {
		return coordination.ErrPointerChanged
	}
	return nil
}

type recordingOpenGenerations struct {
	metadata domain.GenerationMetadata
}

func (r *recordingOpenGenerations) ReadMetadata(context.Context, string, domain.Lineage, domain.GenerationRef) (domain.GenerationMetadata, ports.ObjectMeta, error) {
	return r.metadata, ports.ObjectMeta{Key: r.metadata.MetadataKey, Size: 1, SHA256: "metadata"}, nil
}

type recordingOpenLeases struct {
	generation   domain.GenerationRef
	now          time.Time
	owner        coordination.LeaseOwner
	mutate       func(*coordination.LeaseToken)
	branchCalls  int
	acquireCalls int
}

type unknownOutcomeOpenLeases struct {
	generation  domain.GenerationRef
	now         time.Time
	token       coordination.LeaseToken
	available   bool
	mutate      func(*coordination.LeaseToken)
	branchCalls int
	readCalls   int
}

type absentOutcomeOpenLeases struct {
	branchCalls int
	readCalls   int
}

type conflictingOutcomeOpenLeases struct {
	generation  domain.GenerationRef
	now         time.Time
	token       coordination.LeaseToken
	branchCalls int
	readCalls   int
}

func (r *conflictingOutcomeOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	r.readCalls++
	return r.token, nil
}

func (r *conflictingOutcomeOpenLeases) Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("unexpected main-lineage lease acquisition")
}

func (r *conflictingOutcomeOpenLeases) AcquireBranchFrom(_ context.Context, capsule string, lineage domain.Lineage, _ coordination.LeaseOwner, _ coordination.PointerRecord, _ time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	opened := r.generation
	r.token = coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: capsule, Lineage: lineage, SessionID: "other-session", Machine: "machine-b",
		OpenedGeneration: &opened, CreatedAt: r.now, HeartbeatAt: r.now, ExpiresAt: r.now.Add(ttl),
	}, Revision: "other-lease-r1"}
	return coordination.LeaseToken{}, ports.ErrAmbiguous
}

func (r *absentOutcomeOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	r.readCalls++
	return coordination.LeaseToken{}, ports.ErrNotFound
}

func (r *absentOutcomeOpenLeases) Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("unexpected main-lineage lease acquisition")
}

func (r *absentOutcomeOpenLeases) AcquireBranchFrom(context.Context, string, domain.Lineage, coordination.LeaseOwner, coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	if r.branchCalls == 1 {
		return coordination.LeaseToken{}, ports.ErrAmbiguous
	}
	return coordination.LeaseToken{}, errors.New("reconciliation repeated lease acquisition")
}

func (r *unknownOutcomeOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	r.readCalls++
	if !r.available {
		return coordination.LeaseToken{}, ports.ErrNotFound
	}
	return r.token, nil
}

func (r *unknownOutcomeOpenLeases) Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, errors.New("unexpected main-lineage lease acquisition")
}

func (r *unknownOutcomeOpenLeases) AcquireBranchFrom(_ context.Context, capsule string, lineage domain.Lineage, owner coordination.LeaseOwner, _ coordination.PointerRecord, _ time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	opened := r.generation
	r.token = coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: capsule, Lineage: lineage, SessionID: owner.SessionID, Machine: owner.Machine,
		OpenedGeneration: &opened, CreatedAt: r.now, HeartbeatAt: r.now, ExpiresAt: r.now.Add(ttl),
	}, Revision: "lease-r1"}
	r.available = true
	if r.mutate != nil {
		r.mutate(&r.token)
	}
	return coordination.LeaseToken{}, ports.ErrAmbiguous
}

func (r *recordingOpenLeases) Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error) {
	return coordination.LeaseToken{}, ports.ErrNotFound
}

func (r *recordingOpenLeases) Acquire(_ context.Context, _ string, _ domain.Lineage, owner coordination.LeaseOwner, _ *coordination.PointerRecord, now time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.acquireCalls++
	r.owner = owner
	return r.token(owner, domain.Lineage{Branch: "main"}, now, ttl), nil
}

func (r *recordingOpenLeases) AcquireBranchFrom(_ context.Context, _ string, lineage domain.Lineage, owner coordination.LeaseOwner, _ coordination.PointerRecord, now time.Time, ttl time.Duration) (coordination.LeaseToken, error) {
	r.branchCalls++
	r.owner = owner
	return r.token(owner, lineage, now, ttl), nil
}

func (r *recordingOpenLeases) token(owner coordination.LeaseOwner, lineage domain.Lineage, now time.Time, ttl time.Duration) coordination.LeaseToken {
	opened := r.generation
	token := coordination.LeaseToken{Lease: domain.WriterLease{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, SessionID: owner.SessionID, Machine: owner.Machine,
		OpenedGeneration: &opened, CreatedAt: now, HeartbeatAt: now, ExpiresAt: now.Add(ttl),
	}, Revision: "lease-r1"}
	if r.mutate != nil {
		r.mutate(&token)
	}
	return token
}

type recordingOpenHydrator struct {
	ownership *capsule.Ownership
	events    *[]string
	request   hydration.Request
	calls     int
}

type invalidOpenHydrator struct {
	root string
}

func (r *invalidOpenHydrator) Hydrate(context.Context, hydration.Request) (hydration.Result, error) {
	return hydration.Result{Materialization: domain.Materialization{
		SchemaVersion: domain.SchemaVersion, CanonicalPath: r.root, Mode: domain.MaterializationCreated,
		OwnershipMarker: strings.Repeat("b", 64), CleanupPermitted: true,
	}}, nil
}

type openHydrationStore struct {
	body     []byte
	metadata ports.ObjectMeta
}

func (s *openHydrationStore) Get(context.Context, string) (io.ReadCloser, ports.ObjectMeta, error) {
	return io.NopCloser(bytes.NewReader(s.body)), s.metadata, nil
}

type openHydrationHauler struct {
	inner string
}

func (h *openHydrationHauler) Load(context.Context, string, []string) (ports.Result, error) {
	return ports.Result{}, nil
}

func (h *openHydrationHauler) Extract(_ context.Context, _ string, _ string, output string) (ports.Result, error) {
	if err := os.MkdirAll(output, 0o700); err != nil {
		return ports.Result{}, err
	}
	body, err := os.ReadFile(h.inner)
	if err != nil {
		return ports.Result{}, err
	}
	if err := os.WriteFile(filepath.Join(output, "brain.tar.zst"), body, 0o600); err != nil {
		return ports.Result{}, err
	}
	return ports.Result{}, nil
}

func (r *recordingOpenHydrator) Hydrate(_ context.Context, request hydration.Request) (hydration.Result, error) {
	r.calls++
	r.request = request
	*r.events = append(*r.events, "hydrate")
	if err := os.MkdirAll(request.FinalRoot, 0o700); err != nil {
		return hydration.Result{}, err
	}
	materialization, err := r.ownership.MarkCreatedWithToken(request.FinalRoot, request.Token)
	if err != nil {
		return hydration.Result{}, err
	}
	return hydration.Result{Materialization: materialization, StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, Token: request.Token}, nil
}

var _ OpenPointerReader = (*recordingOpenPointers)(nil)
var _ OpenGenerationReader = (*recordingOpenGenerations)(nil)
var _ OpenLeaseManager = (*recordingOpenLeases)(nil)
var _ OpenHydrator = (*recordingOpenHydrator)(nil)
var _ OpenDevPod = (*openDevPod)(nil)
var _ OpenTargetResolver = (*openTargetResolver)(nil)

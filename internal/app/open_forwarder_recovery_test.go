package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestOpenReconcileRejectsPendingForwarderStartWithoutExactDurableEvidence(t *testing.T) {
	t.Parallel()
	env := newRemoteOpenTestEnvironment(t)
	events := []string{}
	env.open.deps.Forwarders = &openForwarders{events: &events}
	first, err := env.open.Run(context.Background(), OpenRequest{
		SessionID: "forwarder-crash-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
		Context: "default", Provider: "docker", Machine: "machine-a", Runtime: env.runtime, Backend: env.backend,
	})
	if err != nil {
		t.Fatalf("initial Open() error = %v", err)
	}
	runtimeRoot := first.Snapshot.Recovery.Session.RuntimeRoot
	registryEndpoint := endpoint(first.Snapshot.Recovery.Configuration.RegistryPort)
	pending := ports.IntentRecord{
		ID: "forwarder-crash-intent", SessionID: first.Snapshot.SessionID, Transition: "ForwarderStarted:registry",
		Timestamp: time.Unix(99, 0).UTC(), Input: safeJSON(domain.ForwardingRequest{
			Name: "registry", WorkspaceID: first.Snapshot.Workspace.ID, Context: first.Snapshot.Workspace.Context,
			LocalEndpoint: registryEndpoint, WorkspaceEndpoint: registryEndpoint,
			LogPath: filepath.Join(runtimeRoot, "registry-forward.log"), EvidencePath: filepath.Join(runtimeRoot, "registry-forward.json"),
		}),
	}
	crashed := first.Snapshot
	crashed.State = domain.SessionOpening
	crashed.Recovery.Forwarding = nil
	journal := &fakeOpenReconcileJournal{
		snapshot: crashed,
		pending:  []ports.PendingIntent{{Intent: pending}},
	}
	env.open.deps.Journal = journal

	reconciled, err := env.open.Reconcile(context.Background(), first.Snapshot.SessionID)
	if err != nil {
		t.Fatalf("Reconcile() error = %v, want exact durable forwarder evidence to reconcile cleanly", err)
	}
	forwardStarts, forwardObservations := 0, 0
	for _, event := range events {
		if event == "forward:registry" {
			forwardStarts++
		}
		if event == "observe-forward:registry" {
			forwardObservations++
		}
	}
	if forwardStarts != 1 || forwardObservations != 1 {
		t.Fatalf("registry forwarder starts = %d observations = %d, want 1 and 1", forwardStarts, forwardObservations)
	}
	if len(reconciled.Recovery.Forwarding) != 1 || reconciled.Recovery.Forwarding[0].Name != "registry" || journal.fact.IntentID != pending.ID {
		t.Fatalf("reconciled forwarding = %#v fact = %#v", reconciled.Recovery.Forwarding, journal.fact)
	}
}

type fakeOpenReconcileJournal struct {
	snapshot domain.JournalSnapshot
	pending  []ports.PendingIntent
	fact     ports.FactRecord
}

func (f *fakeOpenReconcileJournal) Create(context.Context, domain.JournalSnapshot) error { return nil }
func (f *fakeOpenReconcileJournal) RecordIntent(context.Context, ports.IntentRecord) error {
	return nil
}
func (f *fakeOpenReconcileJournal) RecordFact(_ context.Context, fact ports.FactRecord, snapshot domain.JournalSnapshot) error {
	f.snapshot = snapshot
	f.pending = nil
	f.fact = fact
	return nil
}
func (f *fakeOpenReconcileJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	return f.snapshot, f.pending, nil
}
func (f *fakeOpenReconcileJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	return []domain.JournalSnapshot{f.snapshot}, nil
}

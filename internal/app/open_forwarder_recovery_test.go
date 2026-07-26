package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
)

func TestOpenReconcileAdoptsPendingForwarderFromExactDurableEvidence(t *testing.T) {
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

func TestOpenCompleteWorkspaceOpenRevalidatesCommittedForwarders(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	events := []string{}
	forwarders := &openForwarders{events: &events}
	environment.open.deps.Forwarders = forwarders
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "forwarder-reconciled-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend, EntryMode: "",
	})
	if err != nil {
		t.Fatalf("initial Open() error = %v", err)
	}
	snapshot := first.Snapshot
	snapshot.State = domain.SessionOpen
	events = []string{}
	completed, err := environment.open.completeWorkspaceOpen(context.Background(), snapshot, OpenRequest{}, target.Result{})
	if err != nil {
		t.Fatalf("completeWorkspaceOpen() error = %v", err)
	}
	forwardStarts, forwardObservations := 0, 0
	for _, event := range events {
		switch event {
		case "forward:registry", "forward:fileserver":
			forwardStarts++
		case "observe-forward:registry", "observe-forward:fileserver":
			forwardObservations++
		}
	}
	if forwardStarts != 0 || forwardObservations != 2 {
		t.Fatalf("forwarder starts = %d observations = %d, want 0 and 2", forwardStarts, forwardObservations)
	}
	if len(completed.Snapshot.Recovery.Forwarding) != 2 {
		t.Fatalf("reconciled forwarders = %#v", completed.Snapshot.Recovery.Forwarding)
	}
}

func TestOpenCompleteWorkspaceOpenRestartsUnhealthyCommittedForwarder(t *testing.T) {
	t.Parallel()
	environment := newRemoteOpenTestEnvironment(t)
	events := []string{}
	forwarders := &openForwarders{events: &events}
	environment.open.deps.Forwarders = forwarders
	first, err := environment.open.Run(context.Background(), OpenRequest{
		SessionID: "forwarder-reconciled-stale-session", Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
		Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", Context: "default", Provider: "docker", Machine: "machine-a", Runtime: environment.runtime, Backend: environment.backend, EntryMode: "",
	})
	if err != nil {
		t.Fatalf("initial Open() error = %v", err)
	}
	forwarders.observeErr = errors.New("workspace forwarder evidence is stale")
	environment.open.deps.Forwarders = forwarders
	snapshot := first.Snapshot
	snapshot.State = domain.SessionOpen
	events = []string{}
	completed, err := environment.open.completeWorkspaceOpen(context.Background(), snapshot, OpenRequest{}, target.Result{})
	if err != nil {
		t.Fatalf("completeWorkspaceOpen() error = %v", err)
	}
	forwardStarts, forwardObservations := 0, 0
	for _, event := range events {
		switch event {
		case "forward:registry", "forward:fileserver":
			forwardStarts++
		case "observe-forward:registry", "observe-forward:fileserver":
			forwardObservations++
		}
	}
	if forwardStarts != 2 || forwardObservations != 2 {
		t.Fatalf("forwarder starts = %d observations = %d, want 2 and 2", forwardStarts, forwardObservations)
	}
	if len(completed.Snapshot.Recovery.Forwarding) != 2 {
		t.Fatalf("reconciled forwarders = %#v", completed.Snapshot.Recovery.Forwarding)
	}
}

func TestOpenReconcilePendingForwarderFailsClosedWithoutExactEvidence(t *testing.T) {
	t.Parallel()
	for _, failure := range []string{"durable evidence is absent", "durable evidence identity mismatched"} {
		failure := failure
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			env := newRemoteOpenTestEnvironment(t)
			events := []string{}
			forwarders := &openForwarders{events: &events}
			env.open.deps.Forwarders = forwarders
			first, err := env.open.Run(context.Background(), OpenRequest{
				SessionID: "forwarder-fail-closed-" + strings.ReplaceAll(failure, " ", "-"), Capsule: "brain", Branch: "feature", SourceLineage: domain.Lineage{Branch: "main"},
				Mode: domain.SessionReadWrite, RemoteAvailable: true, Target: "MemoryD", EntryMode: domain.EntryTerminal,
				Context: "default", Provider: "docker", Machine: "machine-a", Runtime: env.runtime, Backend: env.backend,
			})
			if err != nil {
				t.Fatal(err)
			}
			runtimeRoot := first.Snapshot.Recovery.Session.RuntimeRoot
			registryEndpoint := endpoint(first.Snapshot.Recovery.Configuration.RegistryPort)
			pending := ports.IntentRecord{
				ID: "forwarder-fail-closed-intent", SessionID: first.Snapshot.SessionID, Transition: "ForwarderStarted:registry", Timestamp: time.Unix(99, 0).UTC(),
				Input: safeJSON(domain.ForwardingRequest{
					Name: "registry", WorkspaceID: first.Snapshot.Workspace.ID, Context: first.Snapshot.Workspace.Context,
					LocalEndpoint: registryEndpoint, WorkspaceEndpoint: registryEndpoint,
					LogPath: filepath.Join(runtimeRoot, "registry-forward.log"), EvidencePath: filepath.Join(runtimeRoot, "registry-forward.json"),
				}),
			}
			crashed := first.Snapshot
			crashed.State = domain.SessionOpening
			crashed.Recovery.Forwarding = nil
			journal := &fakeOpenReconcileJournal{snapshot: crashed, pending: []ports.PendingIntent{{Intent: pending}}}
			env.open.deps.Journal = journal
			forwarders.observeErr = errors.New(failure)

			if _, err := env.open.Reconcile(context.Background(), first.Snapshot.SessionID); err == nil {
				t.Fatal("Reconcile() error = nil")
			}
			registryStarts := 0
			for _, event := range events {
				if event == "forward:registry" {
					registryStarts++
				}
			}
			if registryStarts != 1 || journal.fact.IntentID != "" || len(journal.snapshot.Recovery.Forwarding) != 0 {
				t.Fatalf("starts=%d fact=%#v forwarding=%#v", registryStarts, journal.fact, journal.snapshot.Recovery.Forwarding)
			}
		})
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

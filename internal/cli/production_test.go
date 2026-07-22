package cli

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestProductionRootRegistersSetupCommand(t *testing.T) {
	command, _, err := NewRoot().Find([]string{"setup"})
	if err != nil {
		t.Fatalf("Find(setup): %v", err)
	}
	if command.Name() != "setup" {
		t.Fatalf("Find(setup) = %q, want setup", command.Name())
	}
}

func TestMachineIdentityFallbackIsStableAndFailsClosed(t *testing.T) {
	t.Parallel()
	missing := func(context.Context) (string, error) { return "", errors.New("no machine-id") }
	hostname := func() (string, error) { return "ror.devpod", nil }
	first, err := resolveMachineID(context.Background(), missing, hostname)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveMachineID(context.Background(), missing, hostname)
	if err != nil || first == "" || first != second {
		t.Fatalf("fallback first=%q second=%q err=%v", first, second, err)
	}
	if _, err := resolveMachineID(context.Background(), missing, func() (string, error) { return "", errors.New("no hostname") }); err == nil {
		t.Fatal("missing identity sources unexpectedly succeeded")
	}
}

func TestResolveProductionProviderSelectsConfiguredRemoteProvider(t *testing.T) {
	t.Setenv("CAMP_DEVPOD_PROVIDER", "room-of-requirement")
	provider, local, err := resolveProductionProvider()
	if err != nil {
		t.Fatal(err)
	}
	if provider != "room-of-requirement" || local {
		t.Fatalf("provider=%q local=%t", provider, local)
	}
}

func TestResolveProductionProviderKeepsDockerDefaultLocal(t *testing.T) {
	t.Setenv("CAMP_DEVPOD_PROVIDER", "")
	provider, local, err := resolveProductionProvider()
	if err != nil {
		t.Fatal(err)
	}
	if provider != "" || !local {
		t.Fatalf("provider=%q local=%t", provider, local)
	}
}

func TestStartSessionSupervisorUsesHiddenCommandAndWaitsForClaim(t *testing.T) {
	t.Parallel()

	journal := &recordingJournal{snapshot: domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     "session-1",
		Supervisor:    domain.SupervisorRecord{},
	}}
	processes := &recordingProcessManager{journal: journal, identity: domain.ProcessIdentity{PID: 321, BootID: "boot", StartTicks: 9}}
	composition := productionComposition{paths: config.XDGPaths{DataRoot: t.TempDir()}, journal: journal}

	if err := startSessionSupervisor(context.Background(), composition, processes, "session-1"); err != nil {
		t.Fatalf("startSessionSupervisor() error = %v", err)
	}
	if got := processes.lastSpec.Command.Argv; len(got) != 2 || got[0] != "supervise" || got[1] != "session-1" {
		t.Fatalf("command argv = %#v", got)
	}
	if processes.lastSpec.Command.Executable == "" || !processes.lastSpec.NewSession || !filepath.IsAbs(processes.lastSpec.LogPath) {
		t.Fatalf("spec = %#v", processes.lastSpec)
	}
	loaded, _, err := journal.Load(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Supervisor.Identity != processes.identity || loaded.Supervisor.Desired != domain.RuntimeDesiredRunning || loaded.Supervisor.Observed != domain.RuntimeObservedReady {
		t.Fatalf("loaded supervisor = %#v", loaded.Supervisor)
	}
}

type recordingJournal struct {
	snapshot domain.JournalSnapshot
}

func (r *recordingJournal) Create(context.Context, domain.JournalSnapshot) error   { return nil }
func (r *recordingJournal) RecordIntent(context.Context, ports.IntentRecord) error { return nil }
func (r *recordingJournal) RecordFact(context.Context, ports.FactRecord, domain.JournalSnapshot) error {
	return nil
}
func (r *recordingJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	return r.snapshot, nil, nil
}
func (r *recordingJournal) List(context.Context) ([]domain.JournalSnapshot, error) { return nil, nil }

type recordingProcessManager struct {
	journal  *recordingJournal
	identity domain.ProcessIdentity
	lastSpec ports.ProcessSpec
}

func (r *recordingProcessManager) Start(_ context.Context, spec ports.ProcessSpec) (domain.ProcessIdentity, error) {
	r.lastSpec = spec
	r.journal.snapshot.Supervisor = domain.SupervisorRecord{
		Identity: r.identity,
		Desired:  domain.RuntimeDesiredRunning,
		Observed: domain.RuntimeObservedReady,
	}
	return r.identity, nil
}
func (r *recordingProcessManager) Inspect(context.Context, domain.ProcessIdentity) (ports.ProcessStatus, error) {
	return ports.ProcessStatus{}, nil
}
func (r *recordingProcessManager) InspectPID(context.Context, int) (ports.ProcessStatus, error) {
	return ports.ProcessStatus{}, nil
}
func (r *recordingProcessManager) Children(context.Context, domain.ProcessIdentity) ([]ports.ProcessStatus, error) {
	return nil, nil
}
func (r *recordingProcessManager) Group(context.Context, int) ([]ports.ProcessStatus, error) {
	return nil, nil
}
func (r *recordingProcessManager) Stop(context.Context, domain.ProcessIdentity, time.Duration) error {
	return nil
}

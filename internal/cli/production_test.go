package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

func TestPersistInitConfigurationWritesOnlyRequestedFirstRunValues(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config", "camp", "config.yaml")
	request := InitRequest{Source: "/srv/brain", Backend: "file:///srv/camp", Capsule: "brain", DevPodProvider: "docker"}
	written, err := persistInitConfiguration(path, request, config.S3Values{})
	if err != nil {
		t.Fatal(err)
	}
	want := config.Persistent{DefaultCapsule: "brain", Backend: "file:///srv/camp", Source: "/srv/brain", DevPodProvider: "docker"}
	if written != want {
		t.Fatalf("written = %#v, want %#v", written, want)
	}
	got, err := config.NewStore(path).Read()
	if err != nil || got != want {
		t.Fatalf("persisted = %#v, error = %v", got, err)
	}
}

func TestConfiguredInitPreflightUsesStrictBackendResolution(t *testing.T) {
	t.Parallel()
	request := InitRequest{Source: "/srv/brain", Backend: "https://example.test/not-a-backend", Capsule: "brain", DevPodProvider: "docker"}
	if _, err := validateConfiguredInit(request, config.S3Values{}); err == nil || !strings.Contains(err.Error(), "strict file") {
		t.Fatalf("validateConfiguredInit() error = %v, want strict backend rejection", err)
	}
}

func TestConfiguredInitRejectsInvalidBackendBeforeFilesystemOrToolEffects(t *testing.T) {
	root := t.TempDir()
	configHome := filepath.Join(root, "config")
	dataHome := filepath.Join(root, "data")
	cacheHome := filepath.Join(root, "cache")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	t.Setenv("PATH", filepath.Join(root, "empty-path"))
	request := InitRequest{Source: filepath.Join(root, "brain"), Backend: "https://example.test/not-a-backend", Capsule: "brain", DevPodProvider: "docker", DevPodContext: "ror"}
	err := NewProductionLifecycle().Init(context.Background(), request, ModeHuman, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "strict file") {
		t.Fatalf("Init() error = %v, want strict backend rejection", err)
	}
	for _, path := range []string{filepath.Join(dataHome, "camp"), filepath.Join(configHome, "camp", "config.yaml")} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("invalid configured init changed %s: %v", path, statErr)
		}
	}
}

func TestPersistInitConfigurationPreservesEffectiveS3AndUnrelatedFields(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config", "camp", "config.yaml")
	s3 := config.S3Values{Endpoint: "http://127.0.0.1:9000", Region: "us-east-1", PathStyle: true, Insecure: true}
	store := config.NewStore(path)
	if err := store.Update(config.Persistent{DefaultCapsule: "old", Backend: "s3://camp-bucket/old", S3: s3, RegistryPort: 5001, FileserverPort: 8081}); err != nil {
		t.Fatal(err)
	}
	request := InitRequest{Source: "/srv/brain", Backend: "s3://camp-bucket/team", Capsule: "brain", DevPodProvider: "room-of-requirement", DevPodContext: "ror"}
	written, err := persistInitConfiguration(path, request, s3)
	if err != nil {
		t.Fatal(err)
	}
	if written.S3 != s3 || written.RegistryPort != 5001 || written.FileserverPort != 8081 || written.DevPodContext != "ror" {
		t.Fatalf("persisted = %#v, want effective S3, ports, and context", written)
	}
}

func TestWriteConfiguredInitSuccessStatesExactlyWhatWasWritten(t *testing.T) {
	t.Parallel()
	result := configuredInitResult{ConfigPath: "/home/josh/.config/camp/config.yaml", Source: "/srv/brain", Backend: "file:///srv/camp", Capsule: "brain", DevPodProvider: "room-of-requirement", DevPodContext: "ror"}
	var human bytes.Buffer
	if err := writeConfiguredInitSuccess(&human, ModeHuman, result); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{result.ConfigPath, "source=/srv/brain", "backend=file:///srv/camp", "capsule=brain", "devpod-provider=room-of-requirement", "devpod-context=ror"} {
		if !strings.Contains(human.String(), value) {
			t.Fatalf("human output %q does not state %q", human.String(), value)
		}
	}
	var machine bytes.Buffer
	if err := writeConfiguredInitSuccess(&machine, ModeJSON, result); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"kind":"init"`, `"configPath":"/home/josh/.config/camp/config.yaml"`, `"devpodProvider":"room-of-requirement"`, `"devpodContext":"ror"`} {
		if !strings.Contains(machine.String(), value) {
			t.Fatalf("JSON output %q does not state %q", machine.String(), value)
		}
	}
}

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
	provider, local, err := resolveProductionProvider(os.Getenv("CAMP_DEVPOD_PROVIDER"))
	if err != nil {
		t.Fatal(err)
	}
	if provider != "room-of-requirement" || local {
		t.Fatalf("provider=%q local=%t", provider, local)
	}
}

func TestResolveProductionProviderKeepsDockerDefaultLocal(t *testing.T) {
	t.Setenv("CAMP_DEVPOD_PROVIDER", "")
	provider, local, err := resolveProductionProvider(os.Getenv("CAMP_DEVPOD_PROVIDER"))
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
	composition := productionComposition{productionBase: productionBase{paths: config.XDGPaths{DataRoot: t.TempDir()}, journal: journal}}

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

func TestStartSessionSupervisorReusesExactRunningOwnerOnReentry(t *testing.T) {
	t.Parallel()

	owner := domain.ProcessIdentity{PID: 301, BootID: "boot-owner", StartTicks: 19}
	journal := &recordingJournal{snapshot: domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     "session-reentry",
		Supervisor: domain.SupervisorRecord{
			Identity: owner,
			Desired:  domain.RuntimeDesiredRunning,
			Observed: domain.RuntimeObservedReady,
		},
	}}
	processes := &recordingProcessManager{
		journal:  journal,
		identity: domain.ProcessIdentity{PID: 302, BootID: "boot-new", StartTicks: 20},
		statuses: map[domain.ProcessIdentity]ports.ProcessStatus{owner: {Identity: owner, Running: true}},
	}
	composition := productionComposition{productionBase: productionBase{paths: config.XDGPaths{DataRoot: t.TempDir()}, journal: journal}}

	if err := startSessionSupervisor(context.Background(), composition, processes, "session-reentry"); err != nil {
		t.Fatalf("startSessionSupervisor(reentry) error = %v", err)
	}
	if processes.startCalls != 0 {
		t.Fatalf("ProcessManager.Start calls = %d, want 0", processes.startCalls)
	}
	if journal.snapshot.Supervisor.Identity != owner {
		t.Fatalf("durable owner changed on reentry = %#v", journal.snapshot.Supervisor)
	}
}

func TestStartSessionSupervisorDoesNotReuseOwnerWithPendingClaimRecovery(t *testing.T) {
	t.Parallel()

	owner := domain.ProcessIdentity{PID: 351, BootID: "boot-owner", StartTicks: 24}
	replacement := domain.ProcessIdentity{PID: 352, BootID: "boot-replacement", StartTicks: 25}
	journal := &recordingJournal{
		snapshot: domain.JournalSnapshot{
			SchemaVersion: domain.SchemaVersion,
			SessionID:     "session-pending-claim",
			Supervisor: domain.SupervisorRecord{
				Identity: owner,
				Desired:  domain.RuntimeDesiredRunning,
				Observed: domain.RuntimeObservedReady,
			},
		},
		pending: []ports.PendingIntent{{Intent: ports.IntentRecord{ID: "pending-replacement", SessionID: "session-pending-claim", Transition: "SupervisorClaimed", Attempt: 1, Timestamp: time.Unix(10, 0)}}},
	}
	processes := &recordingProcessManager{
		journal:  journal,
		identity: replacement,
		statuses: map[domain.ProcessIdentity]ports.ProcessStatus{owner: {Identity: owner, Running: true}},
	}
	composition := productionComposition{productionBase: productionBase{paths: config.XDGPaths{DataRoot: t.TempDir()}, journal: journal}}

	if err := startSessionSupervisor(context.Background(), composition, processes, "session-pending-claim"); err != nil {
		t.Fatalf("startSessionSupervisor(pending claim) error = %v", err)
	}
	if processes.startCalls != 1 {
		t.Fatalf("ProcessManager.Start calls = %d, want replacement claimant", processes.startCalls)
	}
}

func TestStartSessionSupervisorReplacesReusedPIDWithoutStoppingUnexpectedProcess(t *testing.T) {
	t.Parallel()

	staleOwner := domain.ProcessIdentity{PID: 401, BootID: "old-boot", StartTicks: 29}
	replacement := domain.ProcessIdentity{PID: 402, BootID: "new-boot", StartTicks: 30}
	journal := &recordingJournal{snapshot: domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     "session-pid-reuse",
		Supervisor: domain.SupervisorRecord{
			Identity: staleOwner,
			Desired:  domain.RuntimeDesiredRunning,
			Observed: domain.RuntimeObservedReady,
		},
	}}
	processes := &recordingProcessManager{
		journal:     journal,
		identity:    replacement,
		inspectErrs: map[domain.ProcessIdentity]error{staleOwner: supervisor.ErrProcessIdentity},
	}
	composition := productionComposition{productionBase: productionBase{paths: config.XDGPaths{DataRoot: t.TempDir()}, journal: journal}}

	if err := startSessionSupervisor(context.Background(), composition, processes, "session-pid-reuse"); err != nil {
		t.Fatalf("startSessionSupervisor(PID reuse) error = %v", err)
	}
	if processes.startCalls != 1 || processes.stopCalls != 0 {
		t.Fatalf("process calls start=%d stop=%d, want start=1 stop=0", processes.startCalls, processes.stopCalls)
	}
	if journal.snapshot.Supervisor.Identity != replacement {
		t.Fatalf("replacement owner = %#v", journal.snapshot.Supervisor)
	}
}

func TestRunSupervisorClaimsBeforeHeartbeatComposition(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CAMP_BACKEND", "file://"+filepath.Join(root, "backend"))
	t.Setenv("CAMP_CAPSULE", "default")
	sessionID := "session-1"
	journalRoot := filepath.Join(root, "data", "camp")
	journal, err := journalstore.NewStore(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Create(context.Background(), domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     sessionID,
		Capsule:       "default",
		Lineage:       domain.Lineage{Branch: "main"},
		Mode:          domain.SessionReadWrite,
		State:         domain.SessionOpen,
	}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("heartbeat composer reached")
	err = runSupervisor(context.Background(), sessionID, func(ctx context.Context, base productionBase) (supervisorHeartbeat, error) {
		loaded, _, loadErr := base.journal.Load(ctx, sessionID)
		if loadErr != nil {
			return supervisorHeartbeat{}, loadErr
		}
		if loaded.Supervisor.Identity == (domain.ProcessIdentity{}) || loaded.Supervisor.Desired != domain.RuntimeDesiredRunning || loaded.Supervisor.Observed != domain.RuntimeObservedPending {
			t.Fatalf("supervisor claim not recorded before heartbeat composition: %#v", loaded.Supervisor)
		}
		return supervisorHeartbeat{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runSupervisor() error = %v, want %v", err, sentinel)
	}
}

func TestRunSupervisorHeartbeatFailureLeavesClaimPending(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("CAMP_BACKEND", "file://"+filepath.Join(root, "backend"))
	t.Setenv("CAMP_CAPSULE", "default")
	sessionID := "session-2"
	journalRoot := filepath.Join(root, "data", "camp")
	journal, err := journalstore.NewStore(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := journal.Create(context.Background(), domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     sessionID,
		Capsule:       "default",
		Lineage:       domain.Lineage{Branch: "main"},
		Mode:          domain.SessionReadWrite,
		State:         domain.SessionOpen,
	}); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("heartbeat composer failed")
	err = runSupervisor(context.Background(), sessionID, func(ctx context.Context, base productionBase) (supervisorHeartbeat, error) {
		loaded, _, loadErr := base.journal.Load(ctx, sessionID)
		if loadErr != nil {
			return supervisorHeartbeat{}, loadErr
		}
		if loaded.Supervisor.Identity == (domain.ProcessIdentity{}) || loaded.Supervisor.Observed != domain.RuntimeObservedPending {
			t.Fatalf("supervisor claim not pending before heartbeat composition failure: %#v", loaded.Supervisor)
		}
		return supervisorHeartbeat{}, sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runSupervisor() error = %v, want %v", err, sentinel)
	}
	loaded, _, err := journal.Load(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Supervisor.Observed != domain.RuntimeObservedPending {
		t.Fatalf("supervisor after heartbeat failure = %#v", loaded.Supervisor)
	}
}

type recordingJournal struct {
	snapshot domain.JournalSnapshot
	pending  []ports.PendingIntent
}

func (r *recordingJournal) Create(context.Context, domain.JournalSnapshot) error   { return nil }
func (r *recordingJournal) RecordIntent(context.Context, ports.IntentRecord) error { return nil }
func (r *recordingJournal) RecordFact(context.Context, ports.FactRecord, domain.JournalSnapshot) error {
	return nil
}
func (r *recordingJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	return r.snapshot, r.pending, nil
}
func (r *recordingJournal) List(context.Context) ([]domain.JournalSnapshot, error) { return nil, nil }

type recordingProcessManager struct {
	journal     *recordingJournal
	identity    domain.ProcessIdentity
	lastSpec    ports.ProcessSpec
	statuses    map[domain.ProcessIdentity]ports.ProcessStatus
	inspectErrs map[domain.ProcessIdentity]error
	startCalls  int
	stopCalls   int
}

func (r *recordingProcessManager) Start(_ context.Context, spec ports.ProcessSpec) (domain.ProcessIdentity, error) {
	r.startCalls++
	r.lastSpec = spec
	r.journal.snapshot.Supervisor = domain.SupervisorRecord{
		Identity: r.identity,
		Desired:  domain.RuntimeDesiredRunning,
		Observed: domain.RuntimeObservedReady,
	}
	return r.identity, nil
}
func (r *recordingProcessManager) Inspect(_ context.Context, identity domain.ProcessIdentity) (ports.ProcessStatus, error) {
	if err := r.inspectErrs[identity]; err != nil {
		return ports.ProcessStatus{}, err
	}
	if r.statuses != nil {
		if status, ok := r.statuses[identity]; ok {
			return status, nil
		}
	}
	return ports.ProcessStatus{Identity: r.identity, Running: true}, nil
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
	r.stopCalls++
	return nil
}

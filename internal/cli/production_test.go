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

	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/campconfig"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/domain"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

func TestProductionCheckpointCompositionIncludesStrictRemoteExecutor(t *testing.T) {
	transports := productionCheckpointTransports(devpod.NewClient("/usr/bin/devpod", subprocess.NewRunner()), nil)
	if transports.Local == nil || transports.RemoteKit == nil {
		t.Fatalf("production checkpoint transports = %#v", transports)
	}
}

type productionReopenLister struct {
	sessions []domain.JournalSnapshot
	err      error
}

func TestProductionConfigShowsEffectivePrecedenceAndRedactsEnvironmentSecrets(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("CAMP_CAPSULE", "from-env")
	t.Setenv("CAMP_ACCESS_TOKEN", "secret-token")
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environmentMap(os.Environ())})
	if err != nil {
		t.Fatal(err)
	}
	if err := config.NewStore(paths.ConfigPath).Update(config.Persistent{DefaultCapsule: "from-file", Backend: "file:///srv/camp"}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := (&ProductionLifecycle{}).ConfigShow(context.Background(), true, false, ModeHuman, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"capsule": "from-env"`) || strings.Contains(out.String(), "secret-token") || !strings.Contains(out.String(), "[REDACTED]") {
		t.Fatalf("effective config output = %q", out.String())
	}
}

func TestValidKitCampNameRejectsPathNamespaceComponents(t *testing.T) {
	for _, name := range []string{".", ".."} {
		if validKitCampName(name) {
			t.Fatalf("validKitCampName(%q) = true, want false", name)
		}
	}
}

func TestProductionKitVerifyPreservesCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.campkit")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := NewProductionLifecycle().KitVerify(ctx, path, ModeHuman, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("KitVerify() error = %v, want context.Canceled", err)
	}
}

func TestProductionConfigSetRoundTripsEverySupportedMachineKeyAndRejectsCampSelection(t *testing.T) {
	for _, test := range []struct {
		key   string
		value string
		want  config.Persistent
	}{
		{key: "backend", value: "file:///srv/camp", want: config.Persistent{Backend: "file:///srv/camp"}},
		{key: "devpodProvider", value: "docker", want: config.Persistent{DevPodProvider: "docker"}},
		{key: "devpodContext", value: "work", want: config.Persistent{DevPodContext: "work"}},
	} {
		t.Run(test.key, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			if err := (&ProductionLifecycle{}).ConfigSet(context.Background(), test.key, test.value, ModeHuman, io.Discard); err != nil {
				t.Fatal(err)
			}
			paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environmentMap(os.Environ())})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := config.NewStore(paths.ConfigPath).Read()
			if err != nil {
				t.Fatal(err)
			}
			if stored != test.want {
				t.Fatalf("stored config = %#v, want %#v", stored, test.want)
			}
		})
	}

	for _, key := range []string{"defaultCapsule", "source"} {
		t.Run("reject_"+key, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
			err := (&ProductionLifecycle{}).ConfigSet(context.Background(), key, "discarded", ModeHuman, io.Discard)
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code != ExitUsage {
				t.Fatalf("ConfigSet(%q) error = %v, want usage error", key, err)
			}
		})
	}
}

func (l productionReopenLister) List(context.Context) ([]domain.JournalSnapshot, error) {
	return l.sessions, l.err
}

type productionReopenContextKey struct{}

func TestDispatchProductionReopenPreservesOnlyFallbackContext(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0).UTC()
	closed := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     "closed-session",
		Capsule:       "brain",
		Lineage:       domain.Lineage{Branch: "main"},
		Mode:          domain.SessionReadWrite,
		State:         domain.SessionClosed,
		CreatedAt:     now.Add(-time.Minute),
		UpdatedAt:     now,
	}
	tests := []struct {
		name          string
		sessions      []domain.JournalSnapshot
		input         Selection
		selector      app.SessionSelector
		wantSelection Selection
	}{
		{
			name:          "fresh controller keeps original manifest context",
			input:         Selection{Camp: "brain"},
			selector:      app.SessionSelector{Capsule: "brain", Branch: "main"},
			wantSelection: Selection{Camp: "brain"},
		},
		{
			name:          "historical session keeps existing camp handoff",
			sessions:      []domain.JournalSnapshot{closed},
			input:         Selection{Session: closed.SessionID},
			selector:      app.SessionSelector{SessionID: closed.SessionID, Capsule: "brain", Branch: "main"},
			wantSelection: Selection{Camp: "brain"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), productionReopenContextKey{}, "preserved")
			ctx = withSelection(ctx, test.input)
			calls := 0
			err := dispatchProductionReopen(ctx, productionReopenLister{sessions: test.sessions}, test.selector, ModeJSON, io.Discard, func(openCtx context.Context, value string, mode OutputMode, out io.Writer) error {
				calls++
				if got := openCtx.Value(productionReopenContextKey{}); got != "preserved" {
					t.Fatalf("context marker = %v, want preserved", got)
				}
				if got := SelectionFromContext(openCtx); got != test.wantSelection {
					t.Fatalf("open selection = %#v, want %#v", got, test.wantSelection)
				}
				if value != "" || mode != ModeJSON || out != io.Discard {
					t.Fatalf("open arguments = %q, %q, %T", value, mode, out)
				}
				return nil
			})
			if err != nil || calls != 1 {
				t.Fatalf("dispatch error = %v, open calls = %d", err, calls)
			}
		})
	}
}

func TestDispatchProductionReopenDoesNotOpenOnInvalidHistory(t *testing.T) {
	t.Parallel()
	invalid := domain.JournalSnapshot{SessionID: "corrupt", State: domain.SessionState("unknown")}
	calls := 0
	err := dispatchProductionReopen(context.Background(), productionReopenLister{sessions: []domain.JournalSnapshot{invalid}}, app.SessionSelector{}, ModeHuman, io.Discard, func(context.Context, string, OutputMode, io.Writer) error {
		calls++
		return nil
	})
	if err == nil || calls != 0 {
		t.Fatalf("dispatch error = %v, open calls = %d", err, calls)
	}
}

func TestManifestOverridesMachineDefaultsForCampRuntime(t *testing.T) {
	root := t.TempDir()
	manifest := campconfig.Resolved{
		Root: root,
		Manifest: campconfig.Manifest{
			SchemaVersion: 1, ID: "alpha", Source: ".", Backend: "file:///camp-backend",
			Workspace: campconfig.Workspace{Provider: "room-of-requirement", Context: "alpha-context"},
		},
	}
	defaults := productionSettings{runtime: config.Runtime{Bootstrap: config.Bootstrap{
		Capsule: "legacy", Source: "/legacy", Backend: "file:///machine-default",
		DevPodProvider: "docker", DevPodContext: "default", RegistryPort: 5000, FileserverPort: 8080,
	}}}
	got, err := applyManifestSettings(defaults, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got.runtime.Capsule != "alpha" || got.runtime.Source != root || got.runtime.Backend != "file:///camp-backend" ||
		got.runtime.DevPodProvider != "room-of-requirement" || got.runtime.DevPodContext != "alpha-context" {
		t.Fatalf("camp runtime = %#v", got.runtime)
	}
}

func TestResolveInitManifestUsesMachineDefaultsAndExplicitCampName(t *testing.T) {
	root := t.TempDir()
	settings := productionSettings{runtime: config.Runtime{Bootstrap: config.Bootstrap{
		Backend: "file:///machine-backend", DevPodProvider: "docker", DevPodContext: "default",
	}}}
	manifest, err := resolveInitManifest(settings, InitRequest{Root: root, Capsule: "alpha"}, root)
	if err != nil {
		t.Fatal(err)
	}
	want := campconfig.Manifest{
		SchemaVersion: 1, ID: "alpha", Source: ".", Backend: "file:///machine-backend",
		Workspace: campconfig.Workspace{Provider: "docker", Context: "default"},
	}
	if manifest != want {
		t.Fatalf("manifest = %#v, want %#v", manifest, want)
	}
}

func TestFirstCampResolutionAutomaticallyMigratesSafeLegacySingleton(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".camp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".camp", "capsule.yaml"), []byte("schemaVersion: 1\nid: alpha\ndefaultBranch: main\ncreatedAt: 2026-07-25T00:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configHome := t.TempDir()
	dataHome := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	configPath := filepath.Join(configHome, "camp", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	legacy := "defaultCapsule: alpha\nsource: " + root + "\nbackend: " + backend + "\ndevpodProvider: docker\ndevpodContext: default\n"
	if err := os.WriteFile(configPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := resolveProductionSettingsForContext(withCampPath(context.Background(), root))
	if err != nil {
		t.Fatal(err)
	}
	if settings.runtime.Capsule != "alpha" || settings.runtime.Source != root || settings.runtime.Backend != backend {
		t.Fatalf("migrated runtime = %#v", settings.runtime)
	}
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "defaultCapsule") || strings.Contains(string(body), "source:") {
		t.Fatalf("migration retained singleton selection:\n%s", body)
	}
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Fatalf("migration backup: %v", err)
	}
}

func TestExplicitCampSourceProofRequiresMatchingCurrentManifest(t *testing.T) {
	root := t.TempDir()
	backend := "file://" + filepath.Join(t.TempDir(), "backend")
	snapshot := domain.JournalSnapshot{
		Capsule: "alpha",
		Recovery: domain.RecoveryRecord{Configuration: domain.ConfigurationRecord{
			Source: root, BackendURL: backend,
		}},
		Workspace: domain.WorkspaceRecord{Provider: "docker", Context: "default"},
	}
	if _, err := proveSelectedCampSource("alpha", snapshot); err == nil || !strings.Contains(err.Error(), "current manifest") {
		t.Fatalf("missing manifest proof error = %v", err)
	}
	if _, err := campconfig.Create(root, campconfig.Manifest{
		SchemaVersion: 1, ID: "alpha", Source: ".", Backend: backend,
		Workspace: campconfig.Workspace{Provider: "docker", Context: "default"},
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := proveSelectedCampSource("alpha", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Root != root || resolved.Manifest.ID != "alpha" {
		t.Fatalf("source proof = %#v", resolved)
	}
	snapshot.Recovery.Configuration.BackendURL = "file:///different"
	if _, err := proveSelectedCampSource("alpha", snapshot); err == nil || !strings.Contains(err.Error(), "does not match durable session") {
		t.Fatalf("backend mismatch error = %v", err)
	}
}

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
	want.DefaultCapsule = ""
	want.Source = ""
	if err != nil || got != want {
		t.Fatalf("persisted machine defaults = %#v, error = %v", got, err)
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

func TestStartSessionSupervisorWaitsForExactRunningPendingOwner(t *testing.T) {
	t.Parallel()

	owner := domain.ProcessIdentity{PID: 331, BootID: "boot-pending", StartTicks: 21}
	journal := &recordingJournal{snapshot: domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion,
		SessionID:     "session-pending-owner",
		Supervisor: domain.SupervisorRecord{
			Identity: owner,
			Desired:  domain.RuntimeDesiredRunning,
			Observed: domain.RuntimeObservedPending,
		},
	}}
	processes := &recordingProcessManager{
		journal:  journal,
		statuses: map[domain.ProcessIdentity]ports.ProcessStatus{owner: {Identity: owner, Running: true}},
		onInspect: func(identity domain.ProcessIdentity) {
			if identity == owner {
				journal.snapshot.Supervisor.Observed = domain.RuntimeObservedReady
			}
		},
	}
	composition := productionComposition{productionBase: productionBase{paths: config.XDGPaths{DataRoot: t.TempDir()}, journal: journal}}

	if err := startSessionSupervisor(context.Background(), composition, processes, "session-pending-owner"); err != nil {
		t.Fatalf("startSessionSupervisor(pending owner) error = %v", err)
	}
	if processes.startCalls != 0 {
		t.Fatalf("ProcessManager.Start calls = %d, want 0", processes.startCalls)
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
	onInspect   func(domain.ProcessIdentity)
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
	if r.onInspect != nil {
		r.onInspect(identity)
	}
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

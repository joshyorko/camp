package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	supervisoradapter "github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type recordingStoreInitializerRunner struct {
	commands []ports.Command
}

func (runner *recordingStoreInitializerRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	runner.commands = append(runner.commands, command)
	return ports.Result{}, nil
}

func TestInitializeRemoteFileserverStoreSeedsHaulerStore(t *testing.T) {
	workspace := t.TempDir()
	store := filepath.Join(workspace, ".camp", "transfer", "fileserver-store")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &recordingStoreInitializerRunner{}
	request := validRequest()
	request.WorkspaceRoot = workspace
	request.SessionID = "session-fileserver"
	if err := initializeRemoteFileserverStore(t.Context(), request, "/runtime/hauler", runner); err != nil {
		t.Fatal(err)
	}
	seed := filepath.Join(workspace, ".camp", "transfer", "fileserver-seed")
	body, err := os.ReadFile(seed)
	if err != nil || string(body) != "session-fileserver\n" {
		t.Fatalf("seed = %q, %v", body, err)
	}
	want := ports.Command{Executable: "/runtime/hauler", Argv: []string{
		"store", "--store", store, "add", "file", seed, "--name", "camp-session-seed",
	}}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0], want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

type recordingRemoteController struct {
	ensured   []supervisoradapter.ServiceSpec
	observed  []domain.ServiceUnitRecord
	restarted []domain.ServiceUnitRecord
	record    domain.ServiceUnitRecord
	state     supervisoradapter.UnitState
}

func (controller *recordingRemoteController) Ensure(_ context.Context, snapshot domain.JournalSnapshot, spec supervisoradapter.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	controller.ensured = append(controller.ensured, spec)
	controller.record = recordForSpec(spec)
	snapshot.Services = append(snapshot.Services, controller.record)
	return controller.record, snapshot, nil
}

func (controller *recordingRemoteController) Observe(_ context.Context, record domain.ServiceUnitRecord) (supervisoradapter.UnitObservation, error) {
	controller.observed = append(controller.observed, record)
	state := controller.state
	if state == "" {
		state = supervisoradapter.UnitLive
	}
	return supervisoradapter.UnitObservation{State: state, Record: record}, nil
}

func (controller *recordingRemoteController) Restart(_ context.Context, _, _, launchToken string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	controller.restarted = append(controller.restarted, controller.record)
	controller.record.LaunchToken = launchToken
	return controller.record, domain.JournalSnapshot{}, nil
}

func TestEnsureRemoteServiceRestartsStoppedRecordWithAttemptStableIdentity(t *testing.T) {
	request := validRequest()
	request.WorkspaceRoot = "/workspace"
	request.RuntimeRoot = "/runtime"
	definitions, err := remoteServiceDefinitions(request)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := remoteServiceSpec(request, definitions[0], testRemoteCapability())
	if err != nil {
		t.Fatal(err)
	}
	record := recordForSpec(spec)
	controller := &recordingRemoteController{record: record, state: supervisoradapter.UnitStopped}
	snapshot := domain.JournalSnapshot{SessionID: request.SessionID, Services: []domain.ServiceUnitRecord{record}}

	got, _, err := ensureRemoteService(t.Context(), controller, snapshot, spec)
	if err != nil {
		t.Fatal(err)
	}
	wantToken := spec.LaunchToken + "-restart-102-11"
	if got.LaunchToken != wantToken || len(controller.restarted) != 1 || len(controller.ensured) != 0 {
		t.Fatalf("record=%#v ensured=%d restarted=%d", got, len(controller.ensured), len(controller.restarted))
	}
}

func TestRemoteServiceSpecsUseExactInstalledToolsAndPrivateLoopbackMappings(t *testing.T) {
	root := t.TempDir()
	request := validRequest()
	request.WorkspaceRoot = root + "/workspace"
	request.RuntimeRoot = root + "/runtime"
	definitions, err := remoteServiceDefinitions(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v", definitions)
	}
	registry, _ := definitions[0].Command()
	fileserver, _ := definitions[1].Command()
	wantHauler := request.WorkspaceRoot + "/.camp/runtime/hauler"
	if registry.Executable != wantHauler || fileserver.Executable != wantHauler {
		t.Fatalf("Hauler executables = %q, %q", registry.Executable, fileserver.Executable)
	}
	if !reflect.DeepEqual(registry.Argv, []string{
		"store", "--store", request.RuntimeRoot + "/kit/store", "serve", "registry",
		"--directory", request.WorkspaceRoot + "/.camp/runtime/registry", "--port", "15000", "--readonly=false",
	}) {
		t.Fatalf("registry argv = %#v", registry.Argv)
	}
	if !reflect.DeepEqual(fileserver.Argv, []string{
		"store", "--store", request.WorkspaceRoot + "/.camp/transfer/fileserver-store", "serve", "fileserver",
		"--directory", request.WorkspaceRoot + "/.camp/transfer/export", "--port", "18080", "--timeout", "86400",
	}) {
		t.Fatalf("fileserver argv = %#v", fileserver.Argv)
	}
	if definitions[0].ReadinessPath != hauleradapter.RegistryReadinessPath ||
		definitions[1].ReadinessPath != hauleradapter.FileserverReadinessPath {
		t.Fatalf("readiness paths = %q, %q", definitions[0].ReadinessPath, definitions[1].ReadinessPath)
	}
}

func TestRemoteChildContextPrefixUsesOnlyExactVerifiedRunconWhenSELinuxEnforces(t *testing.T) {
	root := t.TempDir()
	enforce := filepath.Join(root, "enforce")
	runcon := filepath.Join(root, "runcon")
	if err := os.WriteFile(enforce, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runcon, []byte("verified runcon"), 0o755); err != nil {
		t.Fatal(err)
	}
	prefix, err := remoteChildContextPrefix(enforce, runcon)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prefix, []string{runcon, "-t", "unconfined_t"}) {
		t.Fatalf("prefix = %#v", prefix)
	}
	if err := os.Chmod(runcon, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := remoteChildContextPrefix(enforce, runcon); err == nil {
		t.Fatal("non-executable runcon was accepted")
	}
}

func TestRemoteChildContextPrefixIsEmptyWhenSELinuxIsNotEnforcing(t *testing.T) {
	root := t.TempDir()
	enforce := filepath.Join(root, "enforce")
	if prefix, err := remoteChildContextPrefix(enforce, filepath.Join(root, "missing-runcon")); err != nil || prefix != nil {
		t.Fatalf("prefix=%#v err=%v", prefix, err)
	}
	if err := os.WriteFile(enforce, []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if prefix, err := remoteChildContextPrefix(enforce, filepath.Join(root, "missing-runcon")); err != nil || prefix != nil {
		t.Fatalf("prefix=%#v err=%v", prefix, err)
	}
}

func TestEnsureRemoteServiceObservesRecordedUnitWithoutDuplicatingIt(t *testing.T) {
	request := validRequest()
	request.WorkspaceRoot = "/workspace"
	request.RuntimeRoot = "/runtime"
	definitions, err := remoteServiceDefinitions(request)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := remoteServiceSpec(request, definitions[0], testRemoteCapability())
	if err != nil {
		t.Fatal(err)
	}
	record := recordForSpec(spec)
	controller := &recordingRemoteController{record: record}
	snapshot := domain.JournalSnapshot{SessionID: request.SessionID, Services: []domain.ServiceUnitRecord{record}}

	got, _, err := ensureRemoteService(t.Context(), controller, snapshot, spec)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "registry" || len(controller.observed) != 1 || len(controller.ensured) != 0 || len(controller.restarted) != 0 {
		t.Fatalf("record=%#v observed=%d ensured=%d restarted=%d", got, len(controller.observed), len(controller.ensured), len(controller.restarted))
	}
}

func TestEnsureRemoteServiceRejectsCompleteHelperArgvOrDigestDriftBeforeRestart(t *testing.T) {
	request := validRequest()
	request.WorkspaceRoot = "/workspace"
	request.RuntimeRoot = "/runtime"
	definitions, err := remoteServiceDefinitions(request)
	if err != nil {
		t.Fatal(err)
	}
	capability := testRemoteCapability()
	capability.ChildContextPrefix = []string{"/usr/bin/runcon", "-t", "unconfined_t"}
	spec, err := remoteServiceSpec(request, definitions[0], capability)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*domain.ServiceUnitRecord)
	}{
		{"argv", func(record *domain.ServiceUnitRecord) {
			record.Helper.Argv[len(record.Helper.Argv)-1] = "/tmp/unverified-hauler"
		}},
		{"digest", func(record *domain.ServiceUnitRecord) {
			record.Helper.ArgvSHA256 = "forged"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := recordForSpec(spec)
			test.mutate(&record)
			controller := &recordingRemoteController{record: record, state: supervisoradapter.UnitStopped}
			snapshot := domain.JournalSnapshot{SessionID: request.SessionID, Services: []domain.ServiceUnitRecord{record}}
			if _, _, err := ensureRemoteService(t.Context(), controller, snapshot, spec); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("ensureRemoteService() error = %v", err)
			}
			if len(controller.observed) != 0 || len(controller.restarted) != 0 || len(controller.ensured) != 0 {
				t.Fatalf("drift reached controller: %#v", controller)
			}
		})
	}
}

func testRemoteCapability() supervisoradapter.ConfinementCapability {
	return supervisoradapter.ConfinementCapability{
		Executable: "/workspace/.camp/runtime/pasta", Version: "pasta 1",
		EnvironmentFingerprint: "fingerprint", Boundary: "remote-workspace",
	}
}

func recordForSpec(spec supervisoradapter.ServiceSpec) domain.ServiceUnitRecord {
	processSpec, err := supervisoradapter.BuildPastaLoopback(supervisoradapter.PastaLoopback{
		Capability: spec.Capability, Mapping: spec.Mapping, LogPath: spec.LogPath,
		PIDPath: spec.PIDPath, Child: spec.Child,
	})
	if err != nil {
		panic(err)
	}
	helperArgv := append([]string{processSpec.Command.Executable}, processSpec.Command.Argv...)
	helperDigest := sha256.Sum256([]byte(strings.Join(helperArgv, "\x00")))
	return domain.ServiceUnitRecord{
		Name: spec.Name, LaunchToken: spec.LaunchToken,
		Confinement: domain.ConfinementRecord{
			Executable: spec.Capability.Executable, Version: spec.Capability.Version,
			EnvironmentFingerprint: spec.Capability.EnvironmentFingerprint, Boundary: spec.Capability.Boundary,
		},
		Mapping: domain.EndpointMapping{
			HostAddress: spec.Mapping.HostAddress, HostPort: spec.Mapping.HostPort, GuestPort: spec.Mapping.GuestPort,
		},
		PIDPath: spec.PIDPath, LogPath: spec.LogPath,
		Helper: domain.ProcessRecord{
			Identity:          domain.ProcessIdentity{PID: 101, BootID: "boot", StartTicks: 10},
			DesiredExecutable: spec.Capability.Executable, ObservedExecutable: spec.Capability.Executable,
			Argv: helperArgv, ArgvSHA256: hex.EncodeToString(helperDigest[:]),
			PGID: 101, SID: 101, NetNS: "net:[host]",
		},
		Child: domain.ProcessRecord{
			Identity:          domain.ProcessIdentity{PID: 102, BootID: "boot", StartTicks: 11},
			DesiredExecutable: spec.Child.Executable, ObservedExecutable: spec.Child.Executable,
			Argv: append([]string{spec.Child.Executable}, spec.Child.Argv...), ArgvSHA256: "child",
			ParentPID: 101, PGID: 101, SID: 101, NetNS: "net:[child]",
		},
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
	}
}

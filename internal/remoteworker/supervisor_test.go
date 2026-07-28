package remoteworker

import (
	"context"
	"reflect"
	"testing"

	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	supervisoradapter "github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
)

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
		"store", "--store", request.RuntimeRoot + "/kit/store", "serve", "fileserver",
		"--directory", request.WorkspaceRoot + "/.camp/transfer", "--port", "18080", "--timeout", "86400",
	}) {
		t.Fatalf("fileserver argv = %#v", fileserver.Argv)
	}
	if definitions[0].ReadinessPath != hauleradapter.RegistryReadinessPath ||
		definitions[1].ReadinessPath != hauleradapter.FileserverReadinessPath {
		t.Fatalf("readiness paths = %q, %q", definitions[0].ReadinessPath, definitions[1].ReadinessPath)
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

func testRemoteCapability() supervisoradapter.ConfinementCapability {
	return supervisoradapter.ConfinementCapability{
		Executable: "/workspace/.camp/runtime/pasta", Version: "pasta 1",
		EnvironmentFingerprint: "fingerprint", Boundary: "remote-workspace",
	}
}

func recordForSpec(spec supervisoradapter.ServiceSpec) domain.ServiceUnitRecord {
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
			Argv: []string{spec.Capability.Executable, "--foreground"}, ArgvSHA256: "helper",
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

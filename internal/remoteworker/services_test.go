package remoteworker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

type recordingServicesRuntime struct {
	order      []string
	supervisor domain.ProcessRecord
	services   []domain.ServiceUnitRecord
	err        error
}

func (runtime *recordingServicesRuntime) Verify(context.Context, Request) error {
	runtime.order = append(runtime.order, "verify")
	return nil
}

func (runtime *recordingServicesRuntime) Ensure(context.Context, Request) (domain.ProcessRecord, []domain.ServiceUnitRecord, error) {
	runtime.order = append(runtime.order, "ensure")
	return runtime.supervisor, runtime.services, runtime.err
}

func TestStartServicesVerifiesHydrationBeforeStartingExactUnits(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	worker := validWorkerRecord()
	runtime := &recordingServicesRuntime{supervisor: validSupervisorRecord(), services: validRemoteServiceRecords()}

	receipt, err := startServices(t.Context(), request, worker, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.order) != 2 || runtime.order[0] != "verify" || runtime.order[1] != "ensure" {
		t.Fatalf("operation order = %v", runtime.order)
	}
	if receipt.Status != "ready" || receipt.SessionID != request.SessionID ||
		receipt.Worker.Identity != worker.Identity || receipt.Supervisor.Identity == receipt.Worker.Identity ||
		!completeProcessEvidence(receipt.Supervisor) || len(receipt.Services) != 2 || receipt.Services[0].Name != "registry" ||
		receipt.Services[1].Name != "fileserver" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestStartServicesRejectsIncompleteOrUnconfinedEvidence(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	tests := []struct {
		name   string
		mutate func([]domain.ServiceUnitRecord)
	}{
		{"missing service", func(records []domain.ServiceUnitRecord) { records[1] = domain.ServiceUnitRecord{} }},
		{"wildcard mapping", func(records []domain.ServiceUnitRecord) { records[0].Mapping.HostAddress = "0.0.0.0" }},
		{"missing child identity", func(records []domain.ServiceUnitRecord) { records[0].Child.Identity = domain.ProcessIdentity{} }},
		{"missing helper argv digest", func(records []domain.ServiceUnitRecord) { records[0].Helper.ArgvSHA256 = "" }},
		{"shared network namespace", func(records []domain.ServiceUnitRecord) { records[0].Child.NetNS = records[0].Helper.NetNS }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records := validRemoteServiceRecords()
			test.mutate(records)
			runtime := &recordingServicesRuntime{supervisor: validSupervisorRecord(), services: records}
			if _, err := startServices(t.Context(), request, validWorkerRecord(), runtime); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("startServices() error = %v", err)
			}
		})
	}
}

func TestStartServicesRejectsIncompleteSupervisorEvidence(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	runtime := &recordingServicesRuntime{services: validRemoteServiceRecords()}
	if _, err := startServices(t.Context(), request, validWorkerRecord(), runtime); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("startServices() error = %v", err)
	}
}

func validSupervisorRecord() domain.ProcessRecord {
	return domain.ProcessRecord{
		Identity:          domain.ProcessIdentity{PID: 99, BootID: "boot", StartTicks: 90},
		DesiredExecutable: "/workspace/.camp-bootstrap/camp-bootstrap", ObservedExecutable: "/workspace/.camp-bootstrap/camp-bootstrap",
		Argv: []string{"/workspace/.camp-bootstrap/camp-bootstrap", "__remote-service-supervisor"}, ArgvSHA256: "edc09ad8ecb4a2a08599619b362e012bf4c9366c7a80545fcc84b1511e117e18",
		ParentPID: 98, PGID: 90, SID: 90, NetNS: "net:[host]",
	}
}

func validWorkerRecord() domain.ProcessRecord {
	return domain.ProcessRecord{
		Identity:          domain.ProcessIdentity{PID: 98, BootID: "boot", StartTicks: 89},
		DesiredExecutable: "/workspace/.camp-bootstrap/camp-bootstrap", ObservedExecutable: "/workspace/.camp-bootstrap/camp-bootstrap",
		Argv: []string{"/workspace/.camp-bootstrap/camp-bootstrap", "__remote-worker"}, ArgvSHA256: "dc3e36d81c500d7581a4359fbb9a3ae7610c82340045b5efb9563455167c2a9f",
		ParentPID: 1, PGID: 89, SID: 89, NetNS: "net:[host]",
	}
}

func TestServiceActorEvidenceRoundTripsAndRejectsEitherIdentityMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actors.json")
	actors := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
		Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
	}
	if err := publishServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	if err := observeServiceActorEvidence(path, actors); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ServiceActorEvidence){
		func(value *ServiceActorEvidence) { value.Worker.Identity.StartTicks++ },
		func(value *ServiceActorEvidence) { value.Supervisor.ArgvSHA256 = "mismatch" },
	} {
		expected := actors
		mutate(&expected)
		if err := observeServiceActorEvidence(path, expected); !errors.Is(err, ErrServiceEvidence) {
			t.Fatalf("observeServiceActorEvidence() error = %v", err)
		}
	}
}

func TestServiceActorEvidenceRejectsConflatedOrWrongRoleCommands(t *testing.T) {
	for _, mutate := range []func(*ServiceActorEvidence){
		func(value *ServiceActorEvidence) { value.Supervisor.Identity = value.Worker.Identity },
		func(value *ServiceActorEvidence) { value.Worker.Argv[1] = "__remote-service-supervisor" },
		func(value *ServiceActorEvidence) { value.Supervisor.Argv[1] = "__remote-worker" },
	} {
		actors := ServiceActorEvidence{
			SchemaVersion: ProtocolSchemaVersion, SessionID: "session-1",
			Worker: validWorkerRecord(), Supervisor: validSupervisorRecord(),
		}
		mutate(&actors)
		if err := publishServiceActorEvidence(filepath.Join(t.TempDir(), "actors.json"), actors); !errors.Is(err, ErrServiceEvidence) {
			t.Fatalf("publishServiceActorEvidence() error = %v", err)
		}
	}
}

func validRemoteServiceRecords() []domain.ServiceUnitRecord {
	record := func(name string, hostPort, guestPort, helperPID, childPID int) domain.ServiceUnitRecord {
		return domain.ServiceUnitRecord{
			Name: name, LaunchToken: "session-" + name + "-v1",
			Confinement: domain.ConfinementRecord{
				Executable: "/workspace/.camp/runtime/pasta", Version: "pasta 1",
				EnvironmentFingerprint: "fingerprint", Boundary: "remote-workspace",
			},
			Mapping: domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: hostPort, GuestPort: guestPort},
			Helper: domain.ProcessRecord{
				Identity:          domain.ProcessIdentity{PID: helperPID, BootID: "boot", StartTicks: 100},
				DesiredExecutable: "/workspace/.camp/runtime/pasta", ObservedExecutable: "/workspace/.camp/runtime/pasta",
				Argv: []string{"/workspace/.camp/runtime/pasta", "--foreground"}, ArgvSHA256: "helper-digest",
				PGID: helperPID, SID: helperPID, NetNS: "net:[host]",
			},
			Child: domain.ProcessRecord{
				Identity:          domain.ProcessIdentity{PID: childPID, BootID: "boot", StartTicks: 101},
				DesiredExecutable: "/workspace/.camp/runtime/hauler", ObservedExecutable: "/workspace/.camp/runtime/hauler",
				Argv: []string{"/workspace/.camp/runtime/hauler", "store"}, ArgvSHA256: "child-digest",
				ParentPID: helperPID, PGID: helperPID, SID: helperPID, NetNS: "net:[child]",
			},
			DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
		}
	}
	return []domain.ServiceUnitRecord{
		record("registry", remoteRegistryPort, remoteRegistryGuestPort, 101, 102),
		record("fileserver", remoteFileserverPort, remoteFileserverGuestPort, 201, 202),
	}
}

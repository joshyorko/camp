package remoteworker

import (
	"context"
	"errors"
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
	runtime := &recordingServicesRuntime{supervisor: validSupervisorRecord(), services: validRemoteServiceRecords()}

	receipt, err := startServices(t.Context(), request, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.order) != 2 || runtime.order[0] != "verify" || runtime.order[1] != "ensure" {
		t.Fatalf("operation order = %v", runtime.order)
	}
	if receipt.Status != "ready" || receipt.SessionID != request.SessionID ||
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
			if _, err := startServices(t.Context(), request, runtime); !errors.Is(err, ErrServiceEvidence) {
				t.Fatalf("startServices() error = %v", err)
			}
		})
	}
}

func TestStartServicesRejectsIncompleteSupervisorEvidence(t *testing.T) {
	request := validRequest()
	request.Operation = OperationStartServices
	runtime := &recordingServicesRuntime{services: validRemoteServiceRecords()}
	if _, err := startServices(t.Context(), request, runtime); !errors.Is(err, ErrServiceEvidence) {
		t.Fatalf("startServices() error = %v", err)
	}
}

func validSupervisorRecord() domain.ProcessRecord {
	return domain.ProcessRecord{
		Identity:          domain.ProcessIdentity{PID: 99, BootID: "boot", StartTicks: 90},
		DesiredExecutable: "/workspace/.camp/runtime/camp", ObservedExecutable: "/workspace/.camp/runtime/camp",
		Argv: []string{"/workspace/.camp/runtime/camp", "__remote-worker"}, ArgvSHA256: "supervisor-digest",
		ParentPID: 1, PGID: 90, SID: 90, NetNS: "net:[host]",
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

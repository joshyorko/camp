package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrServiceEvidence = errors.New("remote Hauler service evidence is incomplete")

const (
	remoteRegistryPort        = 5000
	remoteFileserverPort      = 8080
	remoteRegistryGuestPort   = 15000
	remoteFileserverGuestPort = 18080
)

type ServiceReceipt struct {
	Status     string                     `json:"status"`
	SessionID  string                     `json:"sessionId"`
	Worker     domain.ProcessRecord       `json:"worker"`
	Supervisor domain.ProcessRecord       `json:"supervisor"`
	Services   []domain.ServiceUnitRecord `json:"services"`
}

type ServiceActorEvidence struct {
	SchemaVersion uint32               `json:"schemaVersion"`
	SessionID     string               `json:"sessionId"`
	Worker        domain.ProcessRecord `json:"worker"`
	Supervisor    domain.ProcessRecord `json:"supervisor"`
}

type servicesRuntime interface {
	Verify(context.Context, Request) error
	Ensure(context.Context, Request) (domain.ProcessRecord, []domain.ServiceUnitRecord, error)
}

func startServices(ctx context.Context, request Request, worker domain.ProcessRecord, runtime servicesRuntime) (ServiceReceipt, error) {
	if runtime == nil {
		return ServiceReceipt{}, errors.New("remote service runtime is unavailable")
	}
	if err := runtime.Verify(ctx, request); err != nil {
		return ServiceReceipt{}, err
	}
	supervisor, records, err := runtime.Ensure(ctx, request)
	if err != nil {
		return ServiceReceipt{}, err
	}
	if !completeProcessEvidence(supervisor) {
		return ServiceReceipt{}, fmt.Errorf("%w: supervisor", ErrServiceEvidence)
	}
	if err := validateServiceActors(worker, supervisor); err != nil {
		return ServiceReceipt{}, err
	}
	if err := validateServiceEvidence(records); err != nil {
		return ServiceReceipt{}, err
	}
	return ServiceReceipt{
		Status: "ready", SessionID: request.SessionID, Worker: worker, Supervisor: supervisor, Services: records,
	}, nil
}

func validateServiceActors(worker, supervisor domain.ProcessRecord) error {
	if !completeProcessEvidence(worker) || !completeProcessEvidence(supervisor) ||
		worker.Identity == supervisor.Identity || supervisor.ParentPID != worker.Identity.PID ||
		worker.Identity.BootID != supervisor.Identity.BootID ||
		worker.DesiredExecutable != worker.ObservedExecutable ||
		supervisor.DesiredExecutable != supervisor.ObservedExecutable ||
		worker.ObservedExecutable != supervisor.ObservedExecutable ||
		!processRecordArgvMatches(worker, "__remote-worker") ||
		!processRecordArgvMatches(supervisor, "__remote-service-supervisor") {
		return fmt.Errorf("%w: worker and supervisor identities are not distinct and related", ErrServiceEvidence)
	}
	return nil
}

func processRecordArgvMatches(record domain.ProcessRecord, operation string) bool {
	if len(record.Argv) != 2 || record.Argv[1] != operation {
		return false
	}
	digest := sha256.Sum256([]byte(record.Argv[0] + "\x00" + record.Argv[1]))
	return record.ArgvSHA256 == hex.EncodeToString(digest[:])
}

func validateServiceEvidence(records []domain.ServiceUnitRecord) error {
	if len(records) != 2 {
		return fmt.Errorf("%w: expected registry and fileserver", ErrServiceEvidence)
	}
	for index, want := range []struct {
		name                string
		hostPort, guestPort int
	}{{"registry", remoteRegistryPort, remoteRegistryGuestPort}, {"fileserver", remoteFileserverPort, remoteFileserverGuestPort}} {
		record := records[index]
		if record.Name != want.name || record.Mapping.HostAddress != "127.0.0.1" ||
			record.Mapping.HostPort != want.hostPort || record.Mapping.GuestPort != want.guestPort ||
			record.DesiredState != domain.RuntimeDesiredRunning || record.ObservedState != domain.RuntimeObservedReady ||
			!completeProcessEvidence(record.Helper) || !completeProcessEvidence(record.Child) ||
			record.Helper.PGID != record.Helper.Identity.PID || record.Helper.SID != record.Helper.Identity.PID ||
			record.Child.ParentPID != record.Helper.Identity.PID ||
			record.Child.PGID != record.Helper.PGID || record.Child.SID != record.Helper.SID ||
			record.Helper.NetNS == record.Child.NetNS || record.Confinement.Executable == "" ||
			record.Confinement.EnvironmentFingerprint == "" {
			return fmt.Errorf("%w: %s", ErrServiceEvidence, want.name)
		}
	}
	return nil
}

func completeProcessEvidence(record domain.ProcessRecord) bool {
	return record.Identity.PID > 0 && record.Identity.BootID != "" && record.Identity.StartTicks > 0 &&
		record.DesiredExecutable != "" && record.ObservedExecutable != "" && len(record.Argv) > 0 &&
		record.ArgvSHA256 != "" && record.PGID > 0 && record.SID > 0 && record.NetNS != ""
}

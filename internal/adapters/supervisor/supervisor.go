package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrUnknownPortOccupant = errors.New("stable endpoint is occupied by an unknown process")
	ErrUnitInvariant       = errors.New("PastaLoopback service-unit invariant failed")
)

type ServiceSpec struct {
	SessionID   string
	Name        string
	LaunchToken string
	Capability  ConfinementCapability
	Mapping     PortMapping
	LogPath     string
	PIDPath     string
	Child       ports.Command
}

type UnitEvidence struct {
	HostEndpoint  string
	GuestEndpoint string
	ChildNetNS    string
}

type UnitState string

const (
	UnitLive    UnitState = "live"
	UnitStopped UnitState = "stopped"
)

type UnitObservation struct {
	State  UnitState
	Record domain.ServiceUnitRecord
}

type UnitInspector interface {
	Prebind(context.Context, PortMapping) error
	Ready(context.Context, ServiceSpec, ports.ProcessStatus, ports.ProcessStatus) (UnitEvidence, error)
	Stopped(context.Context, domain.ServiceUnitRecord) error
	Absent(context.Context, domain.ServiceUnitRecord) error
}

type ServiceSupervisor struct {
	journal   ports.Journal
	processes ports.ProcessManager
	inspector UnitInspector
}

type startIntentPayload struct {
	LaunchToken            string      `json:"launchToken"`
	Service                string      `json:"service"`
	Mapping                PortMapping `json:"mapping"`
	LauncherPath           string      `json:"launcherPath"`
	LauncherVersion        string      `json:"launcherVersion"`
	EnvironmentFingerprint string      `json:"environmentFingerprint"`
	PIDPath                string      `json:"pidPath"`
	LogPath                string      `json:"logPath"`
	PastaArgv              []string    `json:"pastaArgv"`
	ChildExecutable        string      `json:"childExecutable"`
	ChildArgv              []string    `json:"childArgv"`
}

func NewServiceSupervisor(journal ports.Journal, processes ports.ProcessManager, inspector UnitInspector) *ServiceSupervisor {
	return &ServiceSupervisor{journal: journal, processes: processes, inspector: inspector}
}

func (s *ServiceSupervisor) Observe(ctx context.Context, record domain.ServiceUnitRecord) (UnitObservation, error) {
	if s == nil || s.processes == nil || s.inspector == nil {
		return UnitObservation{}, errors.New("service supervisor dependencies are incomplete")
	}
	spec, err := serviceSpecFromRecord(record)
	if err != nil {
		return UnitObservation{}, err
	}
	helper, err := s.processes.Inspect(ctx, record.Helper.Identity)
	if err != nil {
		return UnitObservation{}, err
	}
	if !helper.Running {
		if err := s.inspector.Stopped(ctx, record); err != nil {
			return UnitObservation{}, err
		}
		return UnitObservation{State: UnitStopped, Record: record}, nil
	}
	processSpec, err := spec.processSpec()
	if err != nil {
		return UnitObservation{}, err
	}
	if helper.Identity != record.Helper.Identity {
		return UnitObservation{}, ErrProcessIdentity
	}
	if err := validateHelper(processSpec, helper); err != nil {
		return UnitObservation{}, err
	}
	observed, err := s.observe(ctx, spec, helper)
	if err != nil {
		return UnitObservation{}, err
	}
	if observed.Helper.Identity != record.Helper.Identity || observed.Child.Identity != record.Child.Identity {
		return UnitObservation{}, ErrProcessIdentity
	}
	return UnitObservation{State: UnitLive, Record: observed}, nil
}

func (s *ServiceSupervisor) Restart(ctx context.Context, sessionID, serviceName, launchToken string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	if s == nil || s.journal == nil || s.processes == nil || s.inspector == nil || sessionID == "" || serviceName == "" || launchToken == "" {
		return domain.ServiceUnitRecord{}, domain.JournalSnapshot{}, errors.New("service restart dependencies or identity are incomplete")
	}
	snapshot, pending, err := s.journal.Load(ctx, sessionID)
	if err != nil {
		return domain.ServiceUnitRecord{}, domain.JournalSnapshot{}, err
	}
	record, ok := recordedService(snapshot, serviceName)
	if !ok {
		return domain.ServiceUnitRecord{}, snapshot, fmt.Errorf("service %q is not recorded", serviceName)
	}
	intent := ports.IntentRecord{ID: serviceName + "-" + launchToken + "-restart", SessionID: sessionID, Transition: "ServiceRestart", Attempt: 1, Timestamp: time.Now().UTC()}
	restartPending := false
	for _, item := range pending {
		if item.Intent.ID == intent.ID && item.Intent.Transition == intent.Transition {
			restartPending = true
			continue
		}
		return domain.ServiceUnitRecord{}, snapshot, fmt.Errorf("service restart requires recovery of pending transition %q", item.Intent.Transition)
	}
	if restartPending && record.LaunchToken == launchToken {
		observation, err := s.Observe(ctx, record)
		if err != nil || observation.State != UnitLive {
			return domain.ServiceUnitRecord{}, snapshot, errors.Join(err, ErrUnitInvariant)
		}
		if err := s.journal.RecordFact(context.WithoutCancel(ctx), ports.FactRecord{IntentID: intent.ID, SessionID: sessionID, Transition: intent.Transition, Timestamp: time.Now().UTC()}, snapshot); err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
		return record, snapshot, nil
	}
	if !restartPending {
		if err := s.journal.RecordIntent(ctx, intent); err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
	}
	observation, err := s.Observe(ctx, record)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	if observation.State == UnitLive {
		if err := s.Stop(ctx, record); err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
	}
	spec, err := serviceSpecFromRecord(record)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	spec.SessionID = sessionID
	spec.LaunchToken = launchToken
	restarted, next, err := s.Ensure(ctx, snapshot, spec)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	fact := ports.FactRecord{IntentID: intent.ID, SessionID: sessionID, Transition: intent.Transition, Timestamp: time.Now().UTC()}
	if err := s.journal.RecordFact(context.WithoutCancel(ctx), fact, next); err != nil {
		return domain.ServiceUnitRecord{}, next, err
	}
	return restarted, next, nil
}

func serviceSpecFromRecord(record domain.ServiceUnitRecord) (ServiceSpec, error) {
	if record.Name == "" || record.LaunchToken == "" || record.Child.DesiredExecutable == "" || len(record.Child.Argv) < 2 || record.Child.Argv[0] != record.Child.DesiredExecutable || record.Helper.DesiredExecutable != record.Confinement.Executable {
		return ServiceSpec{}, fmt.Errorf("recorded service identity is incomplete: %w", ErrUnitInvariant)
	}
	return ServiceSpec{
		SessionID: "observed-" + record.Name, Name: record.Name, LaunchToken: record.LaunchToken,
		Capability: ConfinementCapability{Executable: record.Confinement.Executable, Version: record.Confinement.Version, EnvironmentFingerprint: record.Confinement.EnvironmentFingerprint, Boundary: record.Confinement.Boundary},
		Mapping:    PortMapping{HostAddress: record.Mapping.HostAddress, HostPort: record.Mapping.HostPort, GuestPort: record.Mapping.GuestPort},
		LogPath:    record.LogPath, PIDPath: record.PIDPath,
		Child: ports.Command{Executable: record.Child.DesiredExecutable, Argv: append([]string(nil), record.Child.Argv[1:]...)},
	}, nil
}

func recordedService(snapshot domain.JournalSnapshot, name string) (domain.ServiceUnitRecord, bool) {
	for _, service := range snapshot.Services {
		if service.Name == name {
			return service, true
		}
	}
	return domain.ServiceUnitRecord{}, false
}

func (s *ServiceSupervisor) Ensure(ctx context.Context, snapshot domain.JournalSnapshot, service ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	if s.journal == nil || s.processes == nil || s.inspector == nil {
		return domain.ServiceUnitRecord{}, snapshot, errors.New("service supervisor dependencies are incomplete")
	}
	processSpec, err := service.processSpec()
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	loaded, pending, err := s.journal.Load(ctx, service.SessionID)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	snapshot = loaded
	for _, item := range pending {
		if item.Intent.Transition != "ServiceStart" {
			continue
		}
		var payload startIntentPayload
		if err := json.Unmarshal(item.Intent.Input, &payload); err != nil {
			return domain.ServiceUnitRecord{}, snapshot, fmt.Errorf("decode pending service start: %w", err)
		}
		if payload.LaunchToken != service.LaunchToken || payload.Service != service.Name {
			continue
		}
		record, err := s.discover(ctx, service, processSpec)
		if err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
		next := upsertService(snapshot, record)
		fact := ports.FactRecord{IntentID: item.Intent.ID, SessionID: service.SessionID, Transition: item.Intent.Transition, Timestamp: time.Now().UTC()}
		if err := s.journal.RecordFact(ctx, fact, next); err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
		return record, next, nil
	}
	if err := s.inspector.Prebind(ctx, service.Mapping); err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	intent, err := serviceStartIntent(service)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	if err := s.journal.RecordIntent(ctx, intent); err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	if err := os.MkdirAll(filepath.Dir(service.LogPath), 0o700); err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	if filepath.Dir(service.PIDPath) != filepath.Dir(service.LogPath) {
		if err := os.MkdirAll(filepath.Dir(service.PIDPath), 0o700); err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
	}
	if _, err := os.Lstat(service.PIDPath); err == nil {
		return domain.ServiceUnitRecord{}, snapshot, fmt.Errorf("unexplained private pidfile %q: %w", service.PIDPath, ErrUnitInvariant)
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	helperIdentity, err := s.processes.Start(ctx, processSpec)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	record, err := s.observeStarted(ctx, service, processSpec, helperIdentity)
	if err != nil {
		_ = s.cleanupPartial(ctx, service, helperIdentity)
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	next := upsertService(snapshot, record)
	fact := ports.FactRecord{IntentID: intent.ID, SessionID: service.SessionID, Transition: intent.Transition, Timestamp: time.Now().UTC()}
	if err := s.journal.RecordFact(ctx, fact, next); err != nil {
		return domain.ServiceUnitRecord{}, snapshot, err
	}
	return record, next, nil
}

func (s *ServiceSupervisor) Stop(ctx context.Context, record domain.ServiceUnitRecord) error {
	if record.Helper.PGID > 0 {
		members, err := s.processes.Group(ctx, record.Helper.PGID)
		if err != nil {
			return err
		}
		if err := validateRecordedGroup(members, record); err != nil {
			return err
		}
	}
	if record.Child.Identity.PID > 0 {
		if err := s.processes.Stop(ctx, record.Child.Identity, 5*time.Second); err != nil && !errors.Is(err, ErrProcessIdentity) {
			return fmt.Errorf("stop service child: %w", err)
		}
	}
	if record.Helper.Identity.PID > 0 {
		if err := s.processes.Stop(ctx, record.Helper.Identity, 5*time.Second); err != nil && !errors.Is(err, ErrProcessIdentity) {
			return fmt.Errorf("stop service helper: %w", err)
		}
	}
	if record.Helper.PGID > 0 {
		members, err := s.processes.Group(ctx, record.Helper.PGID)
		if err != nil {
			return err
		}
		if len(members) != 0 {
			return fmt.Errorf("service group still contains %d processes: %w", len(members), ErrUnitInvariant)
		}
	}
	return s.inspector.Absent(ctx, record)
}

func validateRecordedGroup(members []ports.ProcessStatus, record domain.ServiceUnitRecord) error {
	for _, member := range members {
		if !member.Running {
			continue
		}
		if member.Identity == record.Helper.Identity || member.Identity == record.Child.Identity {
			continue
		}
		return fmt.Errorf("unexpected process-group member %d: %w", member.Identity.PID, ErrUnitInvariant)
	}
	return nil
}

func (s *ServiceSupervisor) discover(ctx context.Context, service ServiceSpec, processSpec ports.ProcessSpec) (domain.ServiceUnitRecord, error) {
	body, err := os.ReadFile(service.PIDPath)
	if err != nil {
		return domain.ServiceUnitRecord{}, fmt.Errorf("pending service start lacks discoverable pidfile: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 {
		return domain.ServiceUnitRecord{}, fmt.Errorf("invalid private pidfile: %w", ErrUnitInvariant)
	}
	helper, err := s.processes.InspectPID(ctx, pid)
	if err != nil {
		return domain.ServiceUnitRecord{}, err
	}
	if err := validateHelper(processSpec, helper); err != nil {
		return domain.ServiceUnitRecord{}, err
	}
	return s.observe(ctx, service, helper)
}

func (s *ServiceSupervisor) observeStarted(ctx context.Context, service ServiceSpec, processSpec ports.ProcessSpec, helperIdentity domain.ProcessIdentity) (domain.ServiceUnitRecord, error) {
	deadline := time.Now().Add(15 * time.Second)
	for {
		helper, err := s.processes.Inspect(ctx, helperIdentity)
		if err == nil && helper.Running {
			if validateErr := validateHelper(processSpec, helper); validateErr == nil {
				record, observeErr := s.observe(ctx, service, helper)
				if observeErr == nil {
					return record, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return domain.ServiceUnitRecord{}, fmt.Errorf("service did not become ready: %w", ErrUnitInvariant)
		}
		select {
		case <-ctx.Done():
			return domain.ServiceUnitRecord{}, ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (s *ServiceSupervisor) observe(ctx context.Context, service ServiceSpec, helper ports.ProcessStatus) (domain.ServiceUnitRecord, error) {
	children, err := s.processes.Children(ctx, helper.Identity)
	if err != nil {
		return domain.ServiceUnitRecord{}, err
	}
	if len(children) != 1 {
		return domain.ServiceUnitRecord{}, fmt.Errorf("helper has %d direct children: %w", len(children), ErrUnitInvariant)
	}
	child := children[0]
	wantChildArgv := append([]string{service.Child.Executable}, service.Child.Argv...)
	if !child.Running || child.ParentPID != helper.Identity.PID || child.Executable != service.Child.Executable || !reflect.DeepEqual(child.Argv, wantChildArgv) || child.PGID != helper.PGID || child.SID != helper.SID || child.NetNS == "" || child.NetNS == helper.NetNS {
		return domain.ServiceUnitRecord{}, fmt.Errorf("child identity or namespace mismatch: %w", ErrUnitInvariant)
	}
	evidence, err := s.inspector.Ready(ctx, service, helper, child)
	if err != nil {
		return domain.ServiceUnitRecord{}, err
	}
	if evidence.ChildNetNS != child.NetNS {
		return domain.ServiceUnitRecord{}, fmt.Errorf("readiness namespace drift: %w", ErrUnitInvariant)
	}
	return domain.ServiceUnitRecord{
		Name: service.Name, LaunchToken: service.LaunchToken,
		Confinement: domain.ConfinementRecord{Executable: service.Capability.Executable, Version: service.Capability.Version, EnvironmentFingerprint: service.Capability.EnvironmentFingerprint, Boundary: service.Capability.Boundary},
		Mapping:     domain.EndpointMapping{HostAddress: service.Mapping.HostAddress, HostPort: service.Mapping.HostPort, GuestPort: service.Mapping.GuestPort},
		PIDPath:     service.PIDPath, LogPath: service.LogPath,
		Helper: processRecord(service.Capability.Executable, helper), Child: processRecord(service.Child.Executable, child),
		DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
	}, nil
}

func (s *ServiceSupervisor) cleanupPartial(ctx context.Context, service ServiceSpec, helper domain.ProcessIdentity) error {
	helperStatus, err := s.processes.Inspect(ctx, helper)
	if err != nil {
		return err
	}
	if !helperStatus.Running || helperStatus.PGID <= 0 || helperStatus.SID <= 0 || helperStatus.NetNS == "" {
		return fmt.Errorf("partial helper identity is not safely inspectable: %w", ErrUnitInvariant)
	}
	children, err := s.processes.Children(ctx, helper)
	if err != nil {
		return err
	}
	if len(children) > 1 {
		return fmt.Errorf("partial helper has %d direct children: %w", len(children), ErrUnitInvariant)
	}
	if len(children) == 1 {
		child := children[0]
		wantArgv := append([]string{service.Child.Executable}, service.Child.Argv...)
		if !child.Running || child.ParentPID != helper.PID || child.PGID != helperStatus.PGID || child.SID != helperStatus.SID || child.NetNS == "" || child.NetNS == helperStatus.NetNS || child.Executable != service.Child.Executable || !reflect.DeepEqual(child.Argv, wantArgv) {
			return fmt.Errorf("partial child identity or command mismatch: %w", ErrUnitInvariant)
		}
		if err := s.processes.Stop(ctx, child.Identity, time.Second); err != nil {
			return err
		}
	}
	return s.processes.Stop(ctx, helper, time.Second)
}

func (service ServiceSpec) processSpec() (ports.ProcessSpec, error) {
	if service.SessionID == "" || service.Name == "" || service.LaunchToken == "" {
		return ports.ProcessSpec{}, errors.New("service identity is incomplete")
	}
	if filepath.Base(service.Child.Executable) != "hauler" || !isHaulerService(service.Child.Argv) {
		return ports.ProcessSpec{}, errors.New("PastaLoopback production service requires an exact Hauler serve command")
	}
	return BuildPastaLoopback(PastaLoopback{Capability: service.Capability, Mapping: service.Mapping, LogPath: service.LogPath, PIDPath: service.PIDPath, Child: service.Child})
}

func isHaulerService(argv []string) bool {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == "serve" && (argv[index+1] == "registry" || argv[index+1] == "fileserver") {
			return true
		}
	}
	return false
}

func serviceStartIntent(service ServiceSpec) (ports.IntentRecord, error) {
	spec, err := service.processSpec()
	if err != nil {
		return ports.IntentRecord{}, err
	}
	payload := startIntentPayload{
		LaunchToken: service.LaunchToken, Service: service.Name, Mapping: service.Mapping,
		LauncherPath: service.Capability.Executable, LauncherVersion: service.Capability.Version,
		EnvironmentFingerprint: service.Capability.EnvironmentFingerprint,
		PIDPath:                service.PIDPath, LogPath: service.LogPath, PastaArgv: spec.Command.Argv,
		ChildExecutable: service.Child.Executable, ChildArgv: service.Child.Argv,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return ports.IntentRecord{}, err
	}
	return ports.IntentRecord{ID: service.Name + "-" + service.LaunchToken, SessionID: service.SessionID, Transition: "ServiceStart", Attempt: 1, Timestamp: time.Now().UTC(), Input: body}, nil
}

func validateHelper(spec ports.ProcessSpec, helper ports.ProcessStatus) error {
	wantArgv := append([]string{spec.Command.Executable}, spec.Command.Argv...)
	if !helper.Running || helper.PGID != helper.Identity.PID || helper.SID != helper.Identity.PID || helper.NetNS == "" || !reflect.DeepEqual(helper.Argv, wantArgv) {
		return fmt.Errorf("helper identity or argv mismatch: %w", ErrUnitInvariant)
	}
	return nil
}

func processRecord(desired string, status ports.ProcessStatus) domain.ProcessRecord {
	return domain.ProcessRecord{Identity: status.Identity, DesiredExecutable: desired, ObservedExecutable: status.Executable, Argv: append([]string(nil), status.Argv...), ParentPID: status.ParentPID, PGID: status.PGID, SID: status.SID, NetNS: status.NetNS}
}

func upsertService(snapshot domain.JournalSnapshot, record domain.ServiceUnitRecord) domain.JournalSnapshot {
	for index := range snapshot.Services {
		if snapshot.Services[index].Name == record.Name {
			snapshot.Services[index] = record
			return snapshot
		}
	}
	snapshot.Services = append(snapshot.Services, record)
	return snapshot
}

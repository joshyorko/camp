package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

type fakeUnitProcessManager struct {
	log         ports.Journal
	sessionID   string
	startCount  int
	start       domain.ProcessIdentity
	helper      ports.ProcessStatus
	children    []ports.ProcessStatus
	group       []ports.ProcessStatus
	groupErr    error
	stopOrder   []domain.ProcessIdentity
	inspectErr  error
	beforeStart func(context.Context) error
}

func (m *fakeUnitProcessManager) Start(ctx context.Context, _ ports.ProcessSpec) (domain.ProcessIdentity, error) {
	_, pending, err := m.log.Load(ctx, m.sessionID)
	foundStart := false
	for _, item := range pending {
		foundStart = foundStart || item.Intent.Transition == "ServiceStart"
	}
	if err != nil || !foundStart {
		return domain.ProcessIdentity{}, errors.New("start effect happened before durable intent")
	}
	if m.beforeStart != nil {
		if err := m.beforeStart(ctx); err != nil {
			return domain.ProcessIdentity{}, err
		}
		m.beforeStart = nil
	}
	m.startCount++
	return m.start, nil
}
func (m *fakeUnitProcessManager) Inspect(context.Context, domain.ProcessIdentity) (ports.ProcessStatus, error) {
	return m.helper, m.inspectErr
}
func (m *fakeUnitProcessManager) InspectPID(context.Context, int) (ports.ProcessStatus, error) {
	return m.helper, nil
}
func (m *fakeUnitProcessManager) Children(context.Context, domain.ProcessIdentity) ([]ports.ProcessStatus, error) {
	return append([]ports.ProcessStatus(nil), m.children...), nil
}
func (m *fakeUnitProcessManager) Group(context.Context, int) ([]ports.ProcessStatus, error) {
	return append([]ports.ProcessStatus(nil), m.group...), m.groupErr
}
func (m *fakeUnitProcessManager) Stop(_ context.Context, identity domain.ProcessIdentity, _ time.Duration) error {
	m.stopOrder = append(m.stopOrder, identity)
	return nil
}

type fakeUnitInspector struct {
	prebindErr   error
	absentErr    error
	stoppedErr   error
	evidence     UnitEvidence
	readyCalls   int
	stoppedCalls int
	absentCalls  int
	prebindCalls int
}

func (i *fakeUnitInspector) Prebind(context.Context, PortMapping) error {
	i.prebindCalls++
	return i.prebindErr
}
func (i *fakeUnitInspector) Ready(context.Context, ServiceSpec, ports.ProcessStatus, ports.ProcessStatus) (UnitEvidence, error) {
	i.readyCalls++
	return i.evidence, nil
}

func TestServiceSupervisorObserveRejectsPIDReuseAndStaleListeners(t *testing.T) {
	t.Parallel()
	record, spec, helper, child := observedServiceFixture(t)
	manager := &fakeUnitProcessManager{helper: helper, children: []ports.ProcessStatus{child}}
	inspector := &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}}
	controller := NewServiceSupervisor(nil, manager, inspector)

	observation, err := controller.Observe(context.Background(), record)
	if err != nil || observation.State != UnitLive || observation.Record.Child.Identity != child.Identity {
		t.Fatalf("Observe(live) = %#v, %v", observation, err)
	}
	manager.inspectErr = ErrProcessIdentity
	if _, err := controller.Observe(context.Background(), record); !errors.Is(err, ErrProcessIdentity) {
		t.Fatalf("Observe(reused) error = %v", err)
	}
	manager.inspectErr = nil
	manager.helper.Running = false
	inspector.stoppedErr = ErrUnitInvariant
	if _, err := controller.Observe(context.Background(), record); !errors.Is(err, ErrUnitInvariant) {
		t.Fatalf("Observe(stale listener) error = %v", err)
	}
	if inspector.absentCalls != 0 || inspector.stoppedCalls != 1 {
		t.Fatalf("Observe(stopped) cleanup calls=%d pure checks=%d", inspector.absentCalls, inspector.stoppedCalls)
	}
	_ = spec
}

func TestServiceSupervisorObserveReconstructsRecordedSELinuxChildPrefix(t *testing.T) {
	t.Parallel()
	record, _, helper, child := observedServiceFixture(t)
	separator := -1
	for index, argument := range helper.Argv {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		t.Fatal("fixture helper argv lacks Pasta child separator")
	}
	prefix := []string{"/usr/bin/runcon", "-t", "unconfined_t"}
	helper.Argv = append(append(append([]string(nil), helper.Argv[:separator+1]...), prefix...), helper.Argv[separator+1:]...)
	record.Helper = processRecord(record.Helper.DesiredExecutable, helper)
	manager := &fakeUnitProcessManager{helper: helper, children: []ports.ProcessStatus{child}}
	inspector := &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}}
	observation, err := NewServiceSupervisor(nil, manager, inspector).Observe(context.Background(), record)
	if err != nil || observation.State != UnitLive {
		t.Fatalf("Observe(SELinux child prefix) = %#v, %v", observation, err)
	}
}

func TestServiceSupervisorRestartJournalsBeforeStopAndReusesRecordedCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, _, helper, child := observedServiceFixture(t)
	log, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "restart-session", Services: []domain.ServiceUnitRecord{record}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	manager := &fakeUnitProcessManager{log: log, sessionID: snapshot.SessionID, helper: helper, children: []ports.ProcessStatus{child}, start: helper.Identity}
	inspector := &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}}
	controller := NewServiceSupervisor(log, manager, inspector)

	restarted, next, err := controller.Restart(ctx, snapshot.SessionID, record.Name, "restart-token")
	if err != nil {
		t.Fatalf("Restart() error = %v", err)
	}
	if restarted.LaunchToken != "restart-token" || next.Services[0].LaunchToken != "restart-token" {
		t.Fatalf("Restart() record = %#v next=%#v", restarted, next.Services)
	}
	if !reflect.DeepEqual(manager.stopOrder, []domain.ProcessIdentity{child.Identity, helper.Identity}) || manager.startCount != 1 {
		t.Fatalf("restart stop/start = %#v/%d", manager.stopOrder, manager.startCount)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("restart pending = %#v, %v", pending, err)
	}
}

func TestServiceSupervisorRestartWithinAuthorizedParentIntent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, _, helper, child := observedServiceFixture(t)
	log, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "nested-restart-session", Services: []domain.ServiceUnitRecord{record}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	parent := ports.IntentRecord{ID: "checkpoint-1-3", SessionID: snapshot.SessionID, Transition: "RegistrySnapshotSealed", Attempt: 1, Timestamp: time.Unix(20, 0).UTC()}
	if err := log.RecordIntent(ctx, parent); err != nil {
		t.Fatal(err)
	}
	manager := &fakeUnitProcessManager{log: log, sessionID: snapshot.SessionID, helper: helper, children: []ports.ProcessStatus{child}, start: helper.Identity}
	controller := NewServiceSupervisor(log, manager, &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}})

	if _, _, err := controller.RestartWithin(ctx, snapshot.SessionID, record.Name, "restart-token", parent.ID); err != nil {
		t.Fatalf("RestartWithin() error = %v", err)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Intent.ID != parent.ID {
		t.Fatalf("pending after nested restart = %#v, want only parent", pending)
	}
}

func TestServiceSupervisorRestartCleansProvenStoppedUnitBeforeStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, spec, helper, child := observedServiceFixture(t)
	log, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "stopped-restart-session", Services: []domain.ServiceUnitRecord{record}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	parent := ports.IntentRecord{ID: "checkpoint-1-3", SessionID: snapshot.SessionID, Transition: "RegistrySnapshotSealed", Attempt: 1, Timestamp: time.Unix(20, 0).UTC()}
	if err := log.RecordIntent(ctx, parent); err != nil {
		t.Fatal(err)
	}
	restart := ports.IntentRecord{ID: record.Name + "-" + record.LaunchToken + "-restart", SessionID: snapshot.SessionID, Transition: "ServiceRestart", Attempt: 1, Timestamp: time.Unix(21, 0).UTC()}
	if err := log.RecordIntent(ctx, restart); err != nil {
		t.Fatal(err)
	}
	spec.SessionID = snapshot.SessionID
	start, err := serviceStartIntent(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.RecordIntent(ctx, start); err != nil {
		t.Fatal(err)
	}
	stopped := helper
	stopped.Running = false
	manager := &fakeUnitProcessManager{log: log, sessionID: snapshot.SessionID, helper: stopped, start: helper.Identity}
	manager.beforeStart = func(context.Context) error {
		manager.helper = helper
		manager.children = []ports.ProcessStatus{child}
		return nil
	}
	inspector := &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}}
	controller := NewServiceSupervisor(log, manager, inspector)

	if _, _, err := controller.RestartWithin(ctx, snapshot.SessionID, record.Name, record.LaunchToken, parent.ID); err != nil {
		t.Fatalf("RestartWithin() error = %v", err)
	}
	if inspector.absentCalls != 1 || len(manager.stopOrder) != 0 {
		t.Fatalf("stopped cleanup absent=%d stopOrder=%#v", inspector.absentCalls, manager.stopOrder)
	}
}

func TestServiceSupervisorStartsExactPendingIntentWhenPidfileIsAbsentAndPortIsFree(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	_, spec, helper, child := observedServiceFixture(t)
	spec.SessionID = "pending-start-session"
	log, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: spec.SessionID}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	intent, err := serviceStartIntent(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	manager := &fakeUnitProcessManager{log: log, sessionID: snapshot.SessionID, start: helper.Identity}
	manager.beforeStart = func(context.Context) error {
		manager.helper = helper
		manager.children = []ports.ProcessStatus{child}
		return nil
	}
	inspector := &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}}
	controller := NewServiceSupervisor(log, manager, inspector)

	if _, _, err := controller.Ensure(ctx, snapshot, spec); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if manager.startCount != 1 || inspector.prebindCalls != 1 {
		t.Fatalf("pending start effects start=%d prebind=%d", manager.startCount, inspector.prebindCalls)
	}
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after recovery = %#v error=%v", pending, err)
	}
}

func TestServiceSupervisorRestoresRecordedChildContextForProvenAbsentPendingStart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	record, spec, helper, child := observedServiceFixture(t)
	spec.SessionID = "pending-prefixed-start-session"
	log, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	legacyIntent, err := serviceStartIntent(spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Capability.ChildContextPrefix = []string{"/usr/bin/runcon", "-t", "unconfined_t"}
	prefixed, err := spec.processSpec()
	if err != nil {
		t.Fatal(err)
	}
	helper.Argv = append([]string{prefixed.Command.Executable}, prefixed.Command.Argv...)
	record.Helper = processRecord(spec.Capability.Executable, helper)
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: spec.SessionID, Services: []domain.ServiceUnitRecord{record}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := log.RecordIntent(ctx, legacyIntent); err != nil {
		t.Fatal(err)
	}
	manager := &fakeUnitProcessManager{log: log, sessionID: snapshot.SessionID, start: helper.Identity}
	manager.beforeStart = func(context.Context) error {
		manager.helper = helper
		manager.children = []ports.ProcessStatus{child}
		return nil
	}
	controller := NewServiceSupervisor(log, manager, &fakeUnitInspector{evidence: UnitEvidence{ChildNetNS: child.NetNS}})

	if _, _, err := controller.Ensure(ctx, snapshot, spec); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
}

func observedServiceFixture(t *testing.T) (domain.ServiceUnitRecord, ServiceSpec, ports.ProcessStatus, ports.ProcessStatus) {
	t.Helper()
	privateRoot := t.TempDir()
	helperID := domain.ProcessIdentity{PID: 301, BootID: "boot", StartTicks: 30}
	childID := domain.ProcessIdentity{PID: 302, BootID: "boot", StartTicks: 31}
	childArgv := []string{"/opt/hauler", "store", "--store", "/state/store", "serve", "registry", "--directory", "/state/registry", "--port", "5100", "--readonly=false"}
	spec := ServiceSpec{SessionID: "restart-session", Name: "registry", LaunchToken: "original-token", Capability: ConfinementCapability{Executable: "/usr/bin/pasta", Version: "v", EnvironmentFingerprint: "f"}, Mapping: PortMapping{HostAddress: "127.0.0.1", HostPort: 5000, GuestPort: 5100}, LogPath: filepath.Join(privateRoot, "registry.log"), PIDPath: filepath.Join(privateRoot, "registry.pid"), Child: ports.Command{Executable: "/opt/hauler", Argv: childArgv[1:]}}
	built, err := spec.processSpec()
	if err != nil {
		t.Fatal(err)
	}
	helper := ports.ProcessStatus{Identity: helperID, Running: true, Executable: "/usr/bin/pasta", Argv: append([]string{built.Command.Executable}, built.Command.Argv...), PGID: helperID.PID, SID: helperID.PID, NetNS: "net:[host]"}
	child := ports.ProcessStatus{Identity: childID, Running: true, Executable: "/opt/hauler", Argv: childArgv, ParentPID: helperID.PID, PGID: helperID.PID, SID: helperID.PID, NetNS: "net:[child]"}
	record := domain.ServiceUnitRecord{Name: spec.Name, LaunchToken: spec.LaunchToken, Confinement: domain.ConfinementRecord{Executable: spec.Capability.Executable, Version: spec.Capability.Version, EnvironmentFingerprint: spec.Capability.EnvironmentFingerprint}, Mapping: domain.EndpointMapping{HostAddress: spec.Mapping.HostAddress, HostPort: spec.Mapping.HostPort, GuestPort: spec.Mapping.GuestPort}, PIDPath: spec.PIDPath, LogPath: spec.LogPath, Helper: processRecord(spec.Capability.Executable, helper), Child: processRecord(spec.Child.Executable, child), DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady}
	return record, spec, helper, child
}
func (i *fakeUnitInspector) Absent(context.Context, domain.ServiceUnitRecord) error {
	i.absentCalls++
	return i.absentErr
}
func (i *fakeUnitInspector) Stopped(context.Context, domain.ServiceUnitRecord) error {
	i.stoppedCalls++
	return i.stoppedErr
}

func TestServiceSupervisorJournalsBeforeStartAndDiscoversCrashBeforeFact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	log, err := journal.NewStore(filepath.Join(root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-a", State: domain.SessionOpening, Lease: domain.LeaseRecord{Revision: "r1"}}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	helperID := domain.ProcessIdentity{PID: 101, BootID: "boot", StartTicks: 10}
	childID := domain.ProcessIdentity{PID: 102, BootID: "boot", StartTicks: 11}
	childArgv := []string{"/opt/hauler", "store", "--store", "/state/store", "serve", "registry", "--directory", "/state/registry", "--port", "5100", "--readonly=false"}
	manager := &fakeUnitProcessManager{
		log: log, sessionID: snapshot.SessionID, start: helperID,
		helper:   ports.ProcessStatus{Identity: helperID, Running: true, Executable: "/usr/bin/pasta.avx2", PGID: 101, SID: 101, NetNS: "net:[host]"},
		children: []ports.ProcessStatus{{Identity: childID, Running: true, Executable: "/opt/hauler", Argv: childArgv, ParentPID: 101, PGID: 101, SID: 101, NetNS: "net:[child]"}},
	}
	manager.beforeStart = func(ctx context.Context) error {
		concurrent, _, err := log.Load(ctx, snapshot.SessionID)
		if err != nil {
			return err
		}
		concurrent.Lease.Revision = "r2"
		intent := ports.IntentRecord{ID: "concurrent-lease", SessionID: snapshot.SessionID, Transition: "LeaseRenewed", Attempt: 1, Timestamp: time.Unix(10, 0).UTC()}
		if err := log.RecordIntent(ctx, intent); err != nil {
			return err
		}
		return log.RecordFact(ctx, ports.FactRecord{IntentID: intent.ID, SessionID: snapshot.SessionID, Transition: intent.Transition, Timestamp: intent.Timestamp}, concurrent)
	}
	inspector := &fakeUnitInspector{evidence: UnitEvidence{HostEndpoint: "127.0.0.1:5000", GuestEndpoint: "127.0.0.1:5100", ChildNetNS: "net:[child]"}}
	controller := NewServiceSupervisor(log, manager, inspector)
	spec := ServiceSpec{
		SessionID: snapshot.SessionID, Name: "registry", LaunchToken: "launch-token",
		Capability: ConfinementCapability{Executable: "/usr/bin/pasta", Version: "version", EnvironmentFingerprint: "fingerprint"},
		Mapping:    PortMapping{HostAddress: "127.0.0.1", HostPort: 5000, GuestPort: 5100},
		LogPath:    filepath.Join(root, "private", "pasta.log"), PIDPath: filepath.Join(root, "private", "pasta.pid"),
		Child: ports.Command{Executable: "/opt/hauler", Argv: childArgv[1:]},
	}
	built, err := spec.processSpec()
	if err != nil {
		t.Fatal(err)
	}
	manager.helper.Argv = append([]string{built.Command.Executable}, built.Command.Argv...)
	record, next, err := controller.Ensure(ctx, snapshot, spec)
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if manager.startCount != 1 || record.Helper.Identity != helperID || record.Child.Identity != childID || len(next.Services) != 1 {
		t.Fatalf("record=%#v snapshot=%#v startCount=%d", record, next, manager.startCount)
	}
	loaded, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 || loaded.Lease.Revision != "r2" || loaded.Services[0].Child.Identity != childID {
		t.Fatalf("pending after ready fact = %#v, %v", pending, err)
	}

	// Simulate a controller crash after spawn but before the identity fact by
	// creating a new session with the durable start intent and private pidfile.
	crashSnapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-crash", State: domain.SessionOpening}
	if err := log.Create(ctx, crashSnapshot); err != nil {
		t.Fatal(err)
	}
	manager.sessionID = crashSnapshot.SessionID
	manager.startCount = 0
	spec.SessionID = crashSnapshot.SessionID
	intent, err := serviceStartIntent(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.RecordIntent(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.PIDPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.PIDPath, []byte("101\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, _, err = controller.Ensure(ctx, crashSnapshot, spec)
	if err != nil {
		t.Fatalf("Ensure(discovery) error = %v", err)
	}
	if manager.startCount != 0 || record.Helper.Identity != helperID {
		t.Fatalf("discovery launched duplicate: count=%d record=%#v", manager.startCount, record)
	}
}

func TestServiceSupervisorFailsClosedOnUnknownPortAndStopsChildFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log, _ := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-a"}
	_ = log.Create(ctx, snapshot)
	manager := &fakeUnitProcessManager{log: log, sessionID: snapshot.SessionID}
	inspector := &fakeUnitInspector{prebindErr: ErrUnknownPortOccupant}
	controller := NewServiceSupervisor(log, manager, inspector)
	_, _, err := controller.Ensure(ctx, snapshot, ServiceSpec{SessionID: snapshot.SessionID, Name: "registry", LaunchToken: "t", Capability: ConfinementCapability{Executable: "/usr/bin/pasta", Version: "v", EnvironmentFingerprint: "f"}, Mapping: PortMapping{HostAddress: "127.0.0.1", HostPort: 5000, GuestPort: 5100}, LogPath: "/tmp/a.log", PIDPath: "/tmp/a.pid", Child: ports.Command{Executable: "/opt/hauler", Argv: []string{"store", "--store", "/s", "serve", "registry"}}})
	if !errors.Is(err, ErrUnknownPortOccupant) || manager.startCount != 0 {
		t.Fatalf("Ensure(occupied) = %v, startCount=%d", err, manager.startCount)
	}

	child := domain.ProcessIdentity{PID: 2, BootID: "b", StartTicks: 2}
	helper := domain.ProcessIdentity{PID: 1, BootID: "b", StartTicks: 1}
	record := domain.ServiceUnitRecord{Helper: domain.ProcessRecord{Identity: helper}, Child: domain.ProcessRecord{Identity: child}}
	inspector.prebindErr = nil
	if err := controller.Stop(ctx, record); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !reflect.DeepEqual(manager.stopOrder, []domain.ProcessIdentity{child, helper}) {
		t.Fatalf("stop order = %#v, want child then helper", manager.stopOrder)
	}
}

func TestRecordedProcessArgvDigestRejectsKnownIdentityDrift(t *testing.T) {
	t.Parallel()
	status := ports.ProcessStatus{Identity: domain.ProcessIdentity{PID: 7, BootID: "b", StartTicks: 9}, Running: true, Executable: "/opt/hauler", Argv: []string{"/opt/hauler", "store"}, ParentPID: 6, PGID: 6, SID: 6, NetNS: "net:[1]"}
	record := processRecord("/opt/hauler", status)
	if record.ArgvSHA256 == "" {
		t.Fatal("argv digest is empty")
	}
	drifted := status
	drifted.Argv = []string{"/opt/hauler", "other"}
	if err := validateRecordedGroup([]ports.ProcessStatus{drifted}, domain.ServiceUnitRecord{Child: record}); err == nil {
		t.Fatal("known process argv drift was accepted")
	}
}

func TestValidateRecordedGroupAllowsDetachedHelperReparenting(t *testing.T) {
	t.Parallel()
	record, _, helper, child := observedServiceFixture(t)
	helper.ParentPID++
	if err := validateRecordedGroup([]ports.ProcessStatus{helper, child}, record); err != nil {
		t.Fatalf("validateRecordedGroup(reparented helper) error = %v", err)
	}

	driftedChild := child
	driftedChild.ParentPID++
	if err := validateRecordedGroup([]ports.ProcessStatus{helper, driftedChild}, record); err == nil {
		t.Fatal("validateRecordedGroup() accepted child parent drift")
	}
}

func TestWaitForServiceAbsentAllowsTransientListenerShutdown(t *testing.T) {
	t.Parallel()
	calls := 0
	err := waitForServiceAbsent(context.Background(), func() error {
		calls++
		if calls == 1 {
			return fmt.Errorf("listener remains: %w", ErrUnitInvariant)
		}
		return nil
	}, 100*time.Millisecond, time.Millisecond)
	if err != nil || calls != 2 {
		t.Fatalf("waitForServiceAbsent() calls=%d error=%v", calls, err)
	}
}

func TestServiceSupervisorValidatesProcessGroupBeforeStoppingAnyMember(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	log, err := journal.NewStore(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-group"}
	if err := log.Create(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	helper := domain.ProcessIdentity{PID: 11, BootID: "boot", StartTicks: 11}
	child := domain.ProcessIdentity{PID: 12, BootID: "boot", StartTicks: 12}
	unknown := domain.ProcessIdentity{PID: 13, BootID: "boot", StartTicks: 13}
	manager := &fakeUnitProcessManager{group: []ports.ProcessStatus{
		{Identity: helper, Running: true, PGID: helper.PID},
		{Identity: child, Running: true, PGID: helper.PID},
		{Identity: unknown, Running: true, PGID: helper.PID},
	}}
	controller := NewServiceSupervisor(log, manager, &fakeUnitInspector{})
	record := domain.ServiceUnitRecord{
		Helper: domain.ProcessRecord{Identity: helper, PGID: helper.PID},
		Child:  domain.ProcessRecord{Identity: child, PGID: helper.PID},
	}

	err = controller.Stop(ctx, record)
	if !errors.Is(err, ErrUnitInvariant) {
		t.Fatalf("Stop() error = %v, want ErrUnitInvariant", err)
	}
	if len(manager.stopOrder) != 0 {
		t.Fatalf("Stop() signalled members before group validation: %#v", manager.stopOrder)
	}
}

func TestServiceSupervisorDoesNotKillUnexpectedPartialChild(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	helper := domain.ProcessIdentity{PID: 21, BootID: "boot", StartTicks: 21}
	child := domain.ProcessIdentity{PID: 22, BootID: "boot", StartTicks: 22}
	manager := &fakeUnitProcessManager{
		helper: ports.ProcessStatus{Identity: helper, Running: true, PGID: helper.PID, SID: helper.PID, NetNS: "net:[host]"},
		children: []ports.ProcessStatus{{
			Identity: child, Running: true, Executable: "/usr/bin/unrelated", Argv: []string{"/usr/bin/unrelated"},
			ParentPID: helper.PID, PGID: helper.PID, SID: helper.PID, NetNS: "net:[child]",
		}},
	}
	controller := NewServiceSupervisor(nil, manager, &fakeUnitInspector{})
	service := ServiceSpec{
		Child: ports.Command{
			Executable: "/opt/hauler",
			Argv:       []string{"store", "--store", "/state/store", "serve", "registry", "--directory", "/state/registry", "--port", "5100", "--readonly=false"},
		},
	}

	err := controller.cleanupPartial(ctx, service, helper)
	if !errors.Is(err, ErrUnitInvariant) {
		t.Fatalf("cleanupPartial() error = %v, want ErrUnitInvariant", err)
	}
	if len(manager.stopOrder) != 0 {
		t.Fatalf("cleanupPartial() stopped an unexpected child: %#v", manager.stopOrder)
	}
}

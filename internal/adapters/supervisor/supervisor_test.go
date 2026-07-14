package supervisor

import (
	"context"
	"errors"
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
	log        ports.Journal
	sessionID  string
	startCount int
	start      domain.ProcessIdentity
	helper     ports.ProcessStatus
	children   []ports.ProcessStatus
	stopOrder  []domain.ProcessIdentity
}

func (m *fakeUnitProcessManager) Start(ctx context.Context, _ ports.ProcessSpec) (domain.ProcessIdentity, error) {
	_, pending, err := m.log.Load(ctx, m.sessionID)
	if err != nil || len(pending) != 1 || pending[0].Intent.Transition != "ServiceStart" {
		return domain.ProcessIdentity{}, errors.New("start effect happened before durable intent")
	}
	m.startCount++
	return m.start, nil
}
func (m *fakeUnitProcessManager) Inspect(context.Context, domain.ProcessIdentity) (ports.ProcessStatus, error) {
	return m.helper, nil
}
func (m *fakeUnitProcessManager) InspectPID(context.Context, int) (ports.ProcessStatus, error) {
	return m.helper, nil
}
func (m *fakeUnitProcessManager) Children(context.Context, domain.ProcessIdentity) ([]ports.ProcessStatus, error) {
	return append([]ports.ProcessStatus(nil), m.children...), nil
}
func (m *fakeUnitProcessManager) Group(context.Context, int) ([]ports.ProcessStatus, error) {
	return nil, nil
}
func (m *fakeUnitProcessManager) Stop(_ context.Context, identity domain.ProcessIdentity, _ time.Duration) error {
	m.stopOrder = append(m.stopOrder, identity)
	return nil
}

type fakeUnitInspector struct {
	prebindErr error
	absentErr  error
	evidence   UnitEvidence
}

func (i *fakeUnitInspector) Prebind(context.Context, PortMapping) error { return i.prebindErr }
func (i *fakeUnitInspector) Ready(context.Context, ServiceSpec, ports.ProcessStatus, ports.ProcessStatus) (UnitEvidence, error) {
	return i.evidence, nil
}
func (i *fakeUnitInspector) Absent(context.Context, domain.ServiceUnitRecord) error {
	return i.absentErr
}

func TestServiceSupervisorJournalsBeforeStartAndDiscoversCrashBeforeFact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	log, err := journal.NewStore(filepath.Join(root, "journal"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-a", State: domain.SessionOpening}
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
	_, pending, err := log.Load(ctx, snapshot.SessionID)
	if err != nil || len(pending) != 0 {
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

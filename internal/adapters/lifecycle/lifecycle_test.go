package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestCloseEffectsUseRecordedOwnershipAndChildFirstControllers(t *testing.T) {
	var events []string
	processes := &fakeProcesses{events: &events}
	services := &fakeServices{events: &events}
	workspace := &fakeWorkspace{events: &events}
	leases := &fakeLeases{events: &events}
	ownership := &fakeOwnership{events: &events, removed: true}
	effects := NewCloseEffects(workspace, processes, services, leases, ownership)
	snapshot := lifecycleSnapshot(t.TempDir())

	if err := effects.CloseWorkspace(context.Background(), snapshot, false); err != nil {
		t.Fatalf("CloseWorkspace() error = %v", err)
	}
	if err := effects.StopForwarders(context.Background(), snapshot); err != nil {
		t.Fatalf("StopForwarders() error = %v", err)
	}
	if err := effects.StopServices(context.Background(), snapshot); err != nil {
		t.Fatalf("StopServices() error = %v", err)
	}
	if err := effects.StopSupervisor(context.Background(), snapshot); err != nil {
		t.Fatalf("StopSupervisor() error = %v", err)
	}
	if err := effects.ReleaseLease(context.Background(), snapshot); err != nil {
		t.Fatalf("ReleaseLease() error = %v", err)
	}
	removed, err := effects.RemoveMaterialization(context.Background(), snapshot)
	if err != nil || !removed {
		t.Fatalf("RemoveMaterialization() = %v, %v", removed, err)
	}

	want := []string{"workspace:delete:default:camp-session", "process:31", "service:registry", "service:fileserver", "process:41", "lease:lease-revision", "materialization:" + snapshot.Materialization.CanonicalPath}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
	if leases.token.Lease != *snapshot.Lease.Lease || leases.token.Revision != ports.Revision(snapshot.Lease.Revision) {
		t.Fatalf("lease token = %#v", leases.token)
	}
}

func TestCloseEffectsComposeProductionServiceSupervisorChildFirst(t *testing.T) {
	var events []string
	processes := &fakeProcesses{events: &events}
	services := supervisor.NewServiceSupervisor(nil, processes, &fakeUnitInspector{events: &events})
	effects := NewCloseEffects(&fakeWorkspace{}, processes, services, &fakeLeases{}, &fakeOwnership{})
	snapshot := lifecycleSnapshot(t.TempDir())

	if err := effects.StopServices(context.Background(), snapshot); err != nil {
		t.Fatalf("StopServices() error = %v", err)
	}
	want := []string{"process:12", "process:11", "absent:registry", "process:22", "process:21", "absent:fileserver"}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want child/helper/absence order %#v", events, want)
	}
}

func TestCloseEffectsKeepWorkspaceStopsAndAdoptedRootIsPreserved(t *testing.T) {
	var events []string
	effects := NewCloseEffects(&fakeWorkspace{events: &events}, &fakeProcesses{events: &events}, &fakeServices{events: &events}, &fakeLeases{events: &events}, &fakeOwnership{events: &events})
	snapshot := lifecycleSnapshot(t.TempDir())
	snapshot.Materialization.Mode = domain.MaterializationAdopted

	if err := effects.CloseWorkspace(context.Background(), snapshot, true); err != nil {
		t.Fatalf("CloseWorkspace() error = %v", err)
	}
	removed, err := effects.RemoveMaterialization(context.Background(), snapshot)
	if err != nil || removed {
		t.Fatalf("RemoveMaterialization() = %v, %v", removed, err)
	}
	if want := []string{"workspace:stop:default:camp-session", "materialization:" + snapshot.Materialization.CanonicalPath}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestCloseEffectsFailClosedOnIncompleteRecordedIdentity(t *testing.T) {
	effects := NewCloseEffects(&fakeWorkspace{}, &fakeProcesses{}, &fakeServices{}, &fakeLeases{}, &fakeOwnership{})
	snapshot := lifecycleSnapshot(t.TempDir())
	snapshot.Recovery.Forwarding[0].Process.Identity = domain.ProcessIdentity{PID: 31}
	if err := effects.StopForwarders(context.Background(), snapshot); err == nil {
		t.Fatal("StopForwarders() error = nil, want incomplete identity error")
	}
}

func TestSessionObserverReportsOnlyLiveListenerValidatedEvidence(t *testing.T) {
	snapshot := lifecycleSnapshot(t.TempDir())
	processes := &fakeProcesses{statuses: map[int]ports.ProcessStatus{
		11: {Identity: snapshot.Services[0].Helper.Identity, Running: true},
		12: {Identity: snapshot.Services[0].Child.Identity, Running: true},
		21: {Identity: snapshot.Services[1].Helper.Identity, Running: false},
		22: {Identity: snapshot.Services[1].Child.Identity, Running: false},
		41: {Identity: snapshot.Supervisor.Identity, Running: true},
	}}
	services := &fakeServices{observations: map[string]supervisor.UnitObservation{
		"registry":   {State: supervisor.UnitLive, Record: snapshot.Services[0]},
		"fileserver": {State: supervisor.UnitStopped, Record: snapshot.Services[1]},
	}}

	evidence, err := NewSessionObserver(processes, services).Observe(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	want := app.SessionEvidence{Supervisor: app.ProcessIdentityMatch, Services: map[string]app.ServiceEvidence{
		"registry":   {Helper: app.ProcessIdentityMatch, Child: app.ProcessIdentityMatch},
		"fileserver": {Helper: app.ProcessIdentityAbsent, Child: app.ProcessIdentityAbsent},
	}}
	if !reflect.DeepEqual(evidence, want) {
		t.Fatalf("evidence = %#v, want %#v", evidence, want)
	}
}

func TestSessionObserverReportsPIDReuseWithoutCallingListenerInspector(t *testing.T) {
	snapshot := lifecycleSnapshot(t.TempDir())
	processes := &fakeProcesses{statuses: map[int]ports.ProcessStatus{
		11: {Identity: domain.ProcessIdentity{PID: 11, BootID: "other", StartTicks: 11}, Running: true},
		12: {Identity: snapshot.Services[0].Child.Identity, Running: true},
		21: {Identity: snapshot.Services[1].Helper.Identity, Running: false},
		22: {Identity: snapshot.Services[1].Child.Identity, Running: false},
		41: {Identity: snapshot.Supervisor.Identity, Running: false},
	}}

	evidence, err := NewSessionObserver(processes, &fakeServices{observations: map[string]supervisor.UnitObservation{
		"fileserver": {State: supervisor.UnitStopped, Record: snapshot.Services[1]},
	}}).Observe(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if got := evidence.Services["registry"].Helper; got != app.ProcessIdentityReused {
		t.Fatalf("registry helper evidence = %q", got)
	}
}

func TestSessionObserverFailsClosedWhenListenerValidationFails(t *testing.T) {
	snapshot := lifecycleSnapshot(t.TempDir())
	processes := matchingProcesses(snapshot)
	services := &fakeServices{observeErr: errors.New("listener escaped loopback")}
	if _, err := NewSessionObserver(processes, services).Observe(context.Background(), snapshot); err == nil {
		t.Fatal("Observe() error = nil")
	}
}

func TestSessionObserverFailsClosedWhenStoppedServiceListenerRemains(t *testing.T) {
	snapshot := lifecycleSnapshot(t.TempDir())
	processes := &fakeProcesses{statuses: map[int]ports.ProcessStatus{
		11: {Identity: snapshot.Services[0].Helper.Identity, Running: false},
		12: {Identity: snapshot.Services[0].Child.Identity, Running: false},
		21: {Identity: snapshot.Services[1].Helper.Identity, Running: false},
		22: {Identity: snapshot.Services[1].Child.Identity, Running: false},
		41: {Identity: snapshot.Supervisor.Identity, Running: false},
	}}
	services := &fakeServices{observeErr: errors.New("service listener remains on port")}
	if _, err := NewSessionObserver(processes, services).Observe(context.Background(), snapshot); err == nil {
		t.Fatal("Observe() error = nil")
	}
}

func TestServingRefresherRotatesBothRecordedServices(t *testing.T) {
	snapshot := lifecycleSnapshot(t.TempDir())
	journal := &fakeJournal{snapshot: snapshot}
	services := &fakeServices{}
	request := app.ServingRefreshRequest{
		SessionID:            snapshot.SessionID,
		Generation:           domain.GenerationRef{Generation: 8, ArchiveSHA256: "sha"},
		HaulPath:             filepath.Join(t.TempDir(), "generation-8.tar.zst"),
		RegistrySnapshotRoot: filepath.Join(t.TempDir(), "registry-cut-8"),
	}

	if err := NewServingRefresher(journal, services).Refresh(context.Background(), request); err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if !reflect.DeepEqual(services.stopNames, []string{"registry", "fileserver"}) {
		t.Fatalf("stop order = %#v", services.stopNames)
	}
	if len(services.ensureSpecs) != 2 {
		t.Fatalf("Ensure() calls = %d", len(services.ensureSpecs))
	}
	assertDirectoryArgument(t, services.ensureSpecs[0].Child.Argv, request.RegistrySnapshotRoot)
	assertDirectoryArgument(t, services.ensureSpecs[1].Child.Argv, filepath.Dir(request.HaulPath))
	for _, spec := range services.ensureSpecs {
		if spec.LaunchToken != snapshot.SessionID+"-generation-8-"+spec.Name {
			t.Fatalf("launch token = %q", spec.LaunchToken)
		}
	}
}

func TestServingRefresherValidatesAllRecordsBeforeStoppingAnything(t *testing.T) {
	snapshot := lifecycleSnapshot(t.TempDir())
	snapshot.Services[1].Child.Argv = []string{"/opt/hauler", "serve", "fileserver"}
	services := &fakeServices{}
	err := NewServingRefresher(&fakeJournal{snapshot: snapshot}, services).Refresh(context.Background(), app.ServingRefreshRequest{
		SessionID: snapshot.SessionID, Generation: domain.GenerationRef{Generation: 8, ArchiveSHA256: "sha"},
		HaulPath: filepath.Join(t.TempDir(), "generation.tar.zst"), RegistrySnapshotRoot: filepath.Join(t.TempDir(), "registry"),
	})
	if err == nil {
		t.Fatal("Refresh() error = nil")
	}
	if len(services.stopNames) != 0 {
		t.Fatalf("stopped services = %#v", services.stopNames)
	}
}

func TestServingRefresherRejectsWrongHaulerServicePairingBeforeStoppingAnything(t *testing.T) {
	tests := map[string]func(*domain.ServiceUnitRecord){
		"executable": func(record *domain.ServiceUnitRecord) { record.Child.DesiredExecutable = "hauler" },
		"subcommand": func(record *domain.ServiceUnitRecord) { record.Child.Argv[4] = "load" },
		"service":    func(record *domain.ServiceUnitRecord) { record.Child.Argv[5] = "fileserver" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			snapshot := lifecycleSnapshot(t.TempDir())
			mutate(&snapshot.Services[0])
			services := &fakeServices{}
			err := NewServingRefresher(&fakeJournal{snapshot: snapshot}, services).Refresh(context.Background(), app.ServingRefreshRequest{
				SessionID: snapshot.SessionID, Generation: domain.GenerationRef{Generation: 8, ArchiveSHA256: "sha"},
				HaulPath: filepath.Join(t.TempDir(), "generation.tar.zst"), RegistrySnapshotRoot: filepath.Join(t.TempDir(), "registry"),
			})
			if err == nil {
				t.Fatal("Refresh() error = nil, want mismatched Hauler service rejection")
			}
			if len(services.stopNames) != 0 {
				t.Fatalf("stopped services = %#v", services.stopNames)
			}
		})
	}
}

func lifecycleSnapshot(root string) domain.JournalSnapshot {
	identity := func(pid int) domain.ProcessIdentity {
		return domain.ProcessIdentity{PID: pid, BootID: "boot", StartTicks: uint64(pid)}
	}
	service := func(name string, helper, child, port int) domain.ServiceUnitRecord {
		return domain.ServiceUnitRecord{
			Name: name, LaunchToken: "launch-" + name,
			Confinement: domain.ConfinementRecord{Executable: "/usr/bin/pasta", Version: "1", EnvironmentFingerprint: "env", Boundary: "PastaLoopback"},
			Mapping:     domain.EndpointMapping{HostAddress: "127.0.0.1", HostPort: port, GuestPort: port},
			PIDPath:     filepath.Join(root, name+".pid"), LogPath: filepath.Join(root, name+".log"),
			Helper:       domain.ProcessRecord{Identity: identity(helper), DesiredExecutable: "/usr/bin/pasta"},
			Child:        domain.ProcessRecord{Identity: identity(child), DesiredExecutable: "/opt/hauler", Argv: []string{"/opt/hauler", "store", "--store", filepath.Join(root, "store"), "serve", name, "--directory", filepath.Join(root, name), "--port", "5000"}},
			DesiredState: domain.RuntimeDesiredRunning, ObservedState: domain.RuntimeObservedReady,
		}
	}
	lease := domain.WriterLease{SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "capsule", Lineage: domain.Lineage{Branch: "main"}}
	return domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Capsule: "capsule", Lineage: lease.Lineage, Mode: domain.SessionReadWrite,
		Workspace:       domain.WorkspaceRecord{ID: "camp-session", Context: "default", Provider: "docker"},
		Services:        []domain.ServiceUnitRecord{service("registry", 11, 12, 5000), service("fileserver", 21, 22, 5001)},
		Supervisor:      domain.SupervisorRecord{Identity: identity(41), Desired: domain.RuntimeDesiredRunning},
		Lease:           domain.LeaseRecord{Lease: &lease, Revision: "lease-revision"},
		Recovery:        domain.RecoveryRecord{Forwarding: []domain.ForwardingRecord{{Name: "registry", Process: domain.ProcessRecord{Identity: identity(31)}}}, Cleanup: domain.CleanupPolicy{WorkspaceAction: domain.WorkspaceCleanupDelete}},
		Materialization: domain.Materialization{Mode: domain.MaterializationCreated, CanonicalPath: filepath.Join(root, "capsule"), OwnershipMarker: "marker", Device: 1, Inode: 2, CleanupPermitted: true},
	}
}

func matchingProcesses(snapshot domain.JournalSnapshot) *fakeProcesses {
	statuses := map[int]ports.ProcessStatus{snapshot.Supervisor.Identity.PID: {Identity: snapshot.Supervisor.Identity, Running: true}}
	for _, service := range snapshot.Services {
		statuses[service.Helper.Identity.PID] = ports.ProcessStatus{Identity: service.Helper.Identity, Running: true}
		statuses[service.Child.Identity.PID] = ports.ProcessStatus{Identity: service.Child.Identity, Running: true}
	}
	return &fakeProcesses{statuses: statuses}
}

func assertDirectoryArgument(t *testing.T, argv []string, want string) {
	t.Helper()
	for index := range argv {
		if argv[index] == "--directory" && index+1 < len(argv) && argv[index+1] == want {
			return
		}
	}
	t.Fatalf("argv %#v does not contain --directory %q", argv, want)
}

type fakeWorkspace struct{ events *[]string }

func (f *fakeWorkspace) StopInContext(_ context.Context, contextName, workspaceID string, _ bool) (ports.Result, error) {
	if f.events != nil {
		*f.events = append(*f.events, "workspace:stop:"+contextName+":"+workspaceID)
	}
	return ports.Result{}, nil
}
func (f *fakeWorkspace) DeleteInContext(_ context.Context, contextName, workspaceID string, _ bool) (ports.Result, error) {
	if f.events != nil {
		*f.events = append(*f.events, "workspace:delete:"+contextName+":"+workspaceID)
	}
	return ports.Result{}, nil
}

type fakeProcesses struct {
	events   *[]string
	statuses map[int]ports.ProcessStatus
}

func (f *fakeProcesses) Start(context.Context, ports.ProcessSpec) (domain.ProcessIdentity, error) {
	return domain.ProcessIdentity{}, nil
}
func (f *fakeProcesses) Inspect(_ context.Context, identity domain.ProcessIdentity) (ports.ProcessStatus, error) {
	status, ok := f.statuses[identity.PID]
	if !ok {
		return ports.ProcessStatus{Identity: identity, Running: false}, nil
	}
	return status, nil
}
func (f *fakeProcesses) InspectPID(context.Context, int) (ports.ProcessStatus, error) {
	return ports.ProcessStatus{}, nil
}
func (f *fakeProcesses) Children(context.Context, domain.ProcessIdentity) ([]ports.ProcessStatus, error) {
	return nil, nil
}
func (f *fakeProcesses) Group(context.Context, int) ([]ports.ProcessStatus, error) { return nil, nil }
func (f *fakeProcesses) Stop(_ context.Context, identity domain.ProcessIdentity, _ time.Duration) error {
	if f.events != nil {
		*f.events = append(*f.events, "process:"+itoa(identity.PID))
	}
	return nil
}

type fakeServices struct {
	events       *[]string
	observations map[string]supervisor.UnitObservation
	observeErr   error
	stopNames    []string
	ensureSpecs  []supervisor.ServiceSpec
}

type fakeUnitInspector struct{ events *[]string }

func (f *fakeUnitInspector) Prebind(context.Context, supervisor.PortMapping) error { return nil }
func (f *fakeUnitInspector) Ready(context.Context, supervisor.ServiceSpec, ports.ProcessStatus, ports.ProcessStatus) (supervisor.UnitEvidence, error) {
	return supervisor.UnitEvidence{}, nil
}
func (f *fakeUnitInspector) Stopped(context.Context, domain.ServiceUnitRecord) error { return nil }
func (f *fakeUnitInspector) Absent(_ context.Context, record domain.ServiceUnitRecord) error {
	*f.events = append(*f.events, "absent:"+record.Name)
	return nil
}

func (f *fakeServices) Observe(_ context.Context, record domain.ServiceUnitRecord) (supervisor.UnitObservation, error) {
	if f.observeErr != nil {
		return supervisor.UnitObservation{}, f.observeErr
	}
	if observation, ok := f.observations[record.Name]; ok {
		return observation, nil
	}
	return supervisor.UnitObservation{}, errors.New("unexpected service observation")
}
func (f *fakeServices) Stop(_ context.Context, record domain.ServiceUnitRecord) error {
	if f.events != nil {
		*f.events = append(*f.events, "service:"+record.Name)
	}
	f.stopNames = append(f.stopNames, record.Name)
	return nil
}
func (f *fakeServices) Ensure(_ context.Context, snapshot domain.JournalSnapshot, spec supervisor.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	f.ensureSpecs = append(f.ensureSpecs, spec)
	return domain.ServiceUnitRecord{Name: spec.Name}, snapshot, nil
}

type fakeLeases struct {
	events *[]string
	token  coordination.LeaseToken
}

func (f *fakeLeases) Release(_ context.Context, token coordination.LeaseToken) error {
	f.token = token
	if f.events != nil {
		*f.events = append(*f.events, "lease:"+string(token.Revision))
	}
	return nil
}

type fakeOwnership struct {
	events  *[]string
	removed bool
}

func (f *fakeOwnership) RemoveOwned(_ context.Context, materialization domain.Materialization) (bool, error) {
	if f.events != nil {
		*f.events = append(*f.events, "materialization:"+materialization.CanonicalPath)
	}
	return f.removed, nil
}

type fakeJournal struct {
	snapshot domain.JournalSnapshot
	pending  []ports.PendingIntent
}

func (f *fakeJournal) Create(context.Context, domain.JournalSnapshot) error   { return nil }
func (f *fakeJournal) RecordIntent(context.Context, ports.IntentRecord) error { return nil }
func (f *fakeJournal) RecordFact(context.Context, ports.FactRecord, domain.JournalSnapshot) error {
	return nil
}
func (f *fakeJournal) Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error) {
	return f.snapshot, f.pending, nil
}
func (f *fakeJournal) List(context.Context) ([]domain.JournalSnapshot, error) {
	return []domain.JournalSnapshot{f.snapshot}, nil
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	result := ""
	for value > 0 {
		result = string(rune('0'+value%10)) + result
		value /= 10
	}
	return result
}

package lifecycle

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestServiceStarterCommitsBothLoopbackServices(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-a", Materialization: domain.Materialization{CanonicalPath: root}, Recovery: domain.RecoveryRecord{
		Configuration: domain.ConfigurationRecord{RegistryPort: 5000, FileserverPort: 8080},
		Session:       domain.SessionArtifactPaths{Root: root, RuntimeRoot: filepath.Join(root, "runtime"), RegistryOverlay: filepath.Join(root, "registry"), HaulPath: filepath.Join(root, "generation.tar.zst")},
	}}
	ensurer := &recordingEnsurer{}
	starter := NewServiceStarter(staticConfinement{}, staticPorts{}, ensurer, "/opt/hauler", staticStore{})
	result, err := starter.Start(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(ensurer.specs) != 2 || len(result.Services) != 2 {
		t.Fatalf("specs=%#v services=%#v", ensurer.specs, result.Services)
	}
	if result.Recovery.Configuration.RegistryPort != 5001 || result.Recovery.Configuration.FileserverPort != 8081 {
		t.Fatalf("committed endpoints = %#v", result.Recovery.Configuration)
	}
	for _, spec := range ensurer.specs {
		if spec.Mapping.HostAddress != "127.0.0.1" || spec.Child.Executable != "/opt/hauler" {
			t.Fatalf("unsafe spec: %#v", spec)
		}
	}
}

func TestServiceStarterPreservesCommittedEndpointsAcrossJournalReloads(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	snapshot := domain.JournalSnapshot{SchemaVersion: domain.SchemaVersion, SessionID: "session-reload", Recovery: domain.RecoveryRecord{
		Configuration: domain.ConfigurationRecord{RegistryPort: 5000, FileserverPort: 8080},
		Session:       domain.SessionArtifactPaths{Root: root, RuntimeRoot: filepath.Join(root, "runtime"), RegistryOverlay: filepath.Join(root, "registry"), HaulPath: filepath.Join(root, "generation.tar.zst")},
	}}
	ensurer := &reloadingEnsurer{durable: snapshot}
	result, err := NewServiceStarter(staticConfinement{}, staticPorts{}, ensurer, "/opt/hauler", staticStore{}).Start(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if result.Recovery.Configuration.RegistryPort != 5001 || result.Recovery.Configuration.FileserverPort != 8081 {
		t.Fatalf("advertised endpoints after reload = %#v", result.Recovery.Configuration)
	}
}

type staticConfinement struct{}

func (staticConfinement) Resolve(context.Context) (ports.ConfinementCapability, error) {
	return ports.ConfinementCapability{Executable: "/usr/bin/pasta", Version: "v", EnvironmentFingerprint: "f", Boundary: "host"}, nil
}

type staticPorts struct{}

func (staticPorts) Candidates(_ context.Context, preferred, _ int) ([]int, error) {
	return []int{preferred + 1}, nil
}

type staticStore struct{}

func (staticStore) AddFile(context.Context, string, string, string) (ports.Result, error) {
	return ports.Result{}, nil
}

type recordingEnsurer struct{ specs []supervisor.ServiceSpec }

func (r *recordingEnsurer) Ensure(_ context.Context, snapshot domain.JournalSnapshot, spec supervisor.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	r.specs = append(r.specs, spec)
	record := domain.ServiceUnitRecord{Name: spec.Name, LaunchToken: spec.LaunchToken, Mapping: domain.EndpointMapping{HostPort: spec.Mapping.HostPort}}
	snapshot.Services = append(snapshot.Services, record)
	return record, snapshot, nil
}

type reloadingEnsurer struct{ durable domain.JournalSnapshot }

func (r *reloadingEnsurer) Ensure(_ context.Context, _ domain.JournalSnapshot, spec supervisor.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	record := domain.ServiceUnitRecord{Name: spec.Name, LaunchToken: spec.LaunchToken, Mapping: domain.EndpointMapping{HostPort: spec.Mapping.HostPort}}
	r.durable.Services = append(r.durable.Services, record)
	return record, r.durable, nil
}

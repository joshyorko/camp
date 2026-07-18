package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type confinementResolver interface {
	Resolve(context.Context) (ports.ConfinementCapability, error)
}
type portCandidates interface {
	Candidates(context.Context, int, int) ([]int, error)
}
type serviceEnsurer interface {
	Ensure(context.Context, domain.JournalSnapshot, supervisor.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
}
type haulerStoreInitializer interface {
	AddFile(context.Context, string, string, string) (ports.Result, error)
}

type ServiceStarter struct {
	confinement confinementResolver
	ports       portCandidates
	services    serviceEnsurer
	hauler      string
	stores      haulerStoreInitializer
}

func NewServiceStarter(confinement confinementResolver, ports portCandidates, services serviceEnsurer, haulerExecutable string, stores haulerStoreInitializer) *ServiceStarter {
	return &ServiceStarter{confinement: confinement, ports: ports, services: services, hauler: haulerExecutable, stores: stores}
}

func (s *ServiceStarter) Start(ctx context.Context, snapshot domain.JournalSnapshot) (domain.JournalSnapshot, error) {
	if s == nil || s.confinement == nil || s.ports == nil || s.services == nil || s.stores == nil || !filepath.IsAbs(s.hauler) || snapshot.SessionID == "" {
		return snapshot, errors.New("production service-start dependencies are incomplete")
	}
	capability, err := s.confinement.Resolve(ctx)
	if err != nil {
		return snapshot, err
	}
	store := filepath.Join(snapshot.Recovery.Session.Root, "store")
	files := filepath.Dir(snapshot.Recovery.Session.HaulPath)
	for _, directory := range []string{store, files, snapshot.Recovery.Session.RegistryOverlay, snapshot.Recovery.Session.RuntimeRoot} {
		if !filepath.IsAbs(directory) {
			return snapshot, errors.New("service directory is not absolute")
		}
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return snapshot, err
		}
	}
	seed := filepath.Join(snapshot.Recovery.Session.RuntimeRoot, "store-seed")
	if err := os.WriteFile(seed, []byte(snapshot.SessionID+"\n"), 0o600); err != nil {
		return snapshot, err
	}
	if result, err := s.stores.AddFile(ctx, store, seed, "camp-session-seed"); err != nil || result.ExitCode != 0 {
		return snapshot, errors.Join(err, errors.New("initialize live Hauler store"))
	}
	definitions := []struct {
		name             string
		preferred, guest int
		overlay          string
	}{
		{hauler.RegistryServiceName, snapshot.Recovery.Configuration.RegistryPort, 5000, snapshot.Recovery.Session.RegistryOverlay},
		{hauler.FileserverServiceName, snapshot.Recovery.Configuration.FileserverPort, 8080, files},
	}
	for _, item := range definitions {
		options := hauler.ServiceDefinitionOptions{HaulerExecutable: s.hauler, StoreDirectory: store, OverlayDirectory: item.overlay, GuestPort: item.guest, LogPath: filepath.Join(snapshot.Recovery.Session.RuntimeRoot, item.name+".log"), PIDPath: filepath.Join(snapshot.Recovery.Session.RuntimeRoot, item.name+".pid")}
		var definition hauler.ServiceDefinition
		if item.name == hauler.RegistryServiceName {
			definition, err = hauler.NewRegistryServiceDefinition(options)
		} else {
			definition, err = hauler.NewFileserverServiceDefinition(options)
		}
		if err != nil {
			return snapshot, err
		}
		command, err := definition.Command()
		if err != nil {
			return snapshot, err
		}
		candidates, err := s.ports.Candidates(ctx, item.preferred, 5)
		if err != nil {
			return snapshot, err
		}
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if candidate != item.guest {
				filtered = append(filtered, candidate)
			}
		}
		if len(filtered) == 0 {
			return snapshot, errors.New("service host port must differ from confined guest port")
		}
		spec := supervisor.ServiceSpec{SessionID: snapshot.SessionID, Name: item.name, LaunchToken: snapshot.SessionID + "-" + item.name + "-initial", Capability: capability, Mapping: supervisor.PortMapping{HostAddress: "127.0.0.1", GuestPort: item.guest}, LogPath: options.LogPath, PIDPath: options.PIDPath, Child: command}
		var record domain.ServiceUnitRecord
		record, snapshot, err = supervisor.LaunchEndpoint(ctx, snapshot, spec, filtered, 0, s.services.Ensure)
		if err != nil {
			return snapshot, err
		}
		if item.name == hauler.RegistryServiceName {
			snapshot.Recovery.Configuration.RegistryPort = record.Mapping.HostPort
		} else {
			snapshot.Recovery.Configuration.FileserverPort = record.Mapping.HostPort
		}
	}
	for _, service := range snapshot.Services {
		switch service.Name {
		case hauler.RegistryServiceName:
			snapshot.Recovery.Configuration.RegistryPort = service.Mapping.HostPort
		case hauler.FileserverServiceName:
			snapshot.Recovery.Configuration.FileserverPort = service.Mapping.HostPort
		}
	}
	return snapshot, nil
}

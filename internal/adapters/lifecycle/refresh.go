package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type serviceRefresher interface {
	Stop(context.Context, domain.ServiceUnitRecord) error
	Ensure(context.Context, domain.JournalSnapshot, supervisor.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
}

type ServingRefresher struct {
	journal  ports.Journal
	services serviceRefresher
}

func NewServingRefresher(journal ports.Journal, services serviceRefresher) *ServingRefresher {
	return &ServingRefresher{journal: journal, services: services}
}

func (r *ServingRefresher) Refresh(ctx context.Context, request app.ServingRefreshRequest) error {
	if r == nil || r.journal == nil || r.services == nil {
		return errors.New("serving refresher dependencies are incomplete")
	}
	if request.SessionID == "" || request.Generation.Generation == 0 || request.Generation.ArchiveSHA256 == "" || !filepath.IsAbs(request.HaulPath) || !filepath.IsAbs(request.RegistrySnapshotRoot) {
		return errors.New("serving refresh identity or paths are incomplete")
	}
	snapshot, pending, err := r.journal.Load(ctx, request.SessionID)
	if err != nil {
		return err
	}
	if snapshot.SessionID != request.SessionID || !matchingRefreshPending(pending, request) {
		return errors.New("serving refresh snapshot is not stable")
	}
	directories := map[string]string{"registry": request.RegistrySnapshotRoot, "fileserver": filepath.Dir(request.HaulPath)}
	specs := make([]supervisor.ServiceSpec, 0, len(directories))
	records := make([]domain.ServiceUnitRecord, 0, len(directories))
	for _, name := range []string{"registry", "fileserver"} {
		record, ok := findService(snapshot.Services, name)
		if !ok {
			return fmt.Errorf("serving refresh service %q is missing", name)
		}
		directory := directories[name]
		if name == hauler.RegistryServiceName {
			directory = recordedServingDirectory(record.Child.Argv)
		}
		spec, err := refreshedServiceSpec(snapshot.SessionID, record, directory, request.Generation.Generation)
		if err != nil {
			return fmt.Errorf("prepare serving refresh service %q: %w", name, err)
		}
		if err := spec.Validate(); err != nil {
			return fmt.Errorf("validate serving refresh service %q: %w", name, err)
		}
		records = append(records, record)
		specs = append(specs, spec)
	}
	for index, record := range records {
		if !hasPendingServiceStart(pending, specs[index]) {
			if err := r.services.Stop(ctx, record); err != nil {
				return fmt.Errorf("stop serving refresh service %q: %w", record.Name, err)
			}
		}
		_, next, err := r.services.Ensure(ctx, snapshot, specs[index])
		if err != nil {
			return fmt.Errorf("start serving refresh service %q: %w", record.Name, err)
		}
		snapshot = next
	}
	return nil
}

func hasPendingServiceStart(pending []ports.PendingIntent, spec supervisor.ServiceSpec) bool {
	for _, item := range pending {
		if item.Intent.SessionID == spec.SessionID && item.Intent.Transition == "ServiceStart" && item.Intent.ID == spec.Name+"-"+spec.LaunchToken {
			return true
		}
	}
	return false
}

func recordedServingDirectory(argv []string) string {
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == "--directory" {
			return argv[index+1]
		}
	}
	return ""
}

func matchingRefreshPending(pending []ports.PendingIntent, request app.ServingRefreshRequest) bool {
	if len(pending) == 0 {
		return true
	}
	matches := 0
	for _, item := range pending {
		intent := item.Intent
		if intent.SessionID != request.SessionID || intent.Transition != "ServingContentRefreshed" {
			continue
		}
		var recorded app.ServingRefreshRequest
		if json.Unmarshal(intent.Input, &recorded) == nil && recorded == request {
			matches++
		}
	}
	return matches == 1
}

func refreshedServiceSpec(sessionID string, record domain.ServiceUnitRecord, directory string, generation uint64) (supervisor.ServiceSpec, error) {
	if record.Child.DesiredExecutable == "" || !filepath.IsAbs(record.Child.DesiredExecutable) || len(record.Child.Argv) < 6 || record.Child.Argv[0] != record.Child.DesiredExecutable || !filepath.IsAbs(directory) {
		return supervisor.ServiceSpec{}, errors.New("recorded service identity is incomplete")
	}
	if record.Name != hauler.RegistryServiceName && record.Name != hauler.FileserverServiceName {
		return supervisor.ServiceSpec{}, errors.New("recorded Hauler service is unsupported")
	}
	if record.Child.Argv[1] != "store" || record.Child.Argv[2] != "--store" || !filepath.IsAbs(record.Child.Argv[3]) || record.Child.Argv[4] != "serve" || record.Child.Argv[5] != record.Name {
		return supervisor.ServiceSpec{}, errors.New("recorded Hauler executable, subcommand, and service do not match")
	}
	argv := append([]string(nil), record.Child.Argv[1:]...)
	replaced := false
	for index := 0; index+1 < len(argv); index++ {
		if argv[index] == "--directory" {
			argv[index+1] = directory
			replaced = true
			break
		}
	}
	if !replaced {
		return supervisor.ServiceSpec{}, errors.New("recorded service command has no serving directory")
	}
	var childContextPrefix []string
	if len(record.Helper.Argv) != 0 {
		var err error
		childContextPrefix, err = supervisor.RecordedChildContextPrefix(record)
		if err != nil {
			return supervisor.ServiceSpec{}, err
		}
	}
	return supervisor.ServiceSpec{
		SessionID: sessionID, Name: record.Name, LaunchToken: sessionID + "-generation-" + strconv.FormatUint(generation, 10) + "-" + record.Name,
		Capability: supervisor.ConfinementCapability{Executable: record.Confinement.Executable, Version: record.Confinement.Version, EnvironmentFingerprint: record.Confinement.EnvironmentFingerprint, Boundary: record.Confinement.Boundary, ChildContextPrefix: childContextPrefix},
		Mapping:    supervisor.PortMapping{HostAddress: record.Mapping.HostAddress, HostPort: record.Mapping.HostPort, GuestPort: record.Mapping.GuestPort},
		LogPath:    record.LogPath, PIDPath: record.PIDPath,
		Child: ports.Command{Executable: record.Child.DesiredExecutable, Argv: argv},
	}, nil
}

func findService(services []domain.ServiceUnitRecord, name string) (domain.ServiceUnitRecord, bool) {
	for _, service := range services {
		if service.Name == name {
			return service, true
		}
	}
	return domain.ServiceUnitRecord{}, false
}

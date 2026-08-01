package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	supervisoradapter "github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
)

type remoteServiceController interface {
	Ensure(context.Context, domain.JournalSnapshot, supervisoradapter.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
	Observe(context.Context, domain.ServiceUnitRecord) (supervisoradapter.UnitObservation, error)
	Restart(context.Context, string, string, string) (domain.ServiceUnitRecord, domain.JournalSnapshot, error)
}

type productionServicesRuntime struct {
	manifest haulkit.Manifest
	worker   domain.ProcessRecord
}

func (runtimeState *productionServicesRuntime) Verify(ctx context.Context, request Request) error {
	hydration := newProductionHydrationRuntime()
	if _, complete, err := hydration.ObserveCompleted(request); err != nil {
		return err
	} else if !complete {
		return errors.New("remote workspace hydration is not complete")
	}
	kit, err := newProductionActivationRuntime().Verify(ctx, request)
	if err != nil {
		return err
	}
	body, err := readStableIdentityFile(request.ManifestPath, request.Expected.Manifest)
	if err != nil {
		return err
	}
	runtimeState.manifest, err = haulkit.DecodeCanonical(body)
	if err != nil {
		return err
	}
	if kit.Store != filepath.Join(request.RuntimeRoot, "kit", "store") {
		return ErrIdentityMismatch
	}
	for name, identity := range map[string]haulkit.FileIdentity{
		"hauler": runtimeState.manifest.Tools.Hauler,
		"pasta":  runtimeState.manifest.Tools.Pasta,
	} {
		path := filepath.Join(request.WorkspaceRoot, ".camp", "runtime", name)
		observed, observeErr := observeFile(name, path)
		if observeErr != nil || observed.SHA256 != identity.SHA256 || observed.Size != identity.Size {
			return fmt.Errorf("%w: installed %s", ErrIdentityMismatch, name)
		}
	}
	return nil
}

func (runtimeState *productionServicesRuntime) Ensure(ctx context.Context, request Request) (domain.ProcessRecord, []domain.ServiceUnitRecord, error) {
	definitions, err := remoteServiceDefinitions(request)
	if err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	serviceRoot := filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "services")
	for _, directory := range []string{
		serviceRoot,
		filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "registry"),
		filepath.Join(request.WorkspaceRoot, ".camp", "transfer"),
		filepath.Join(request.WorkspaceRoot, ".camp", "transfer", "export"),
		filepath.Join(request.WorkspaceRoot, ".camp", "transfer", "fileserver-store"),
	} {
		if err := secureMkdirAllOperation(directory); err != nil {
			return domain.ProcessRecord{}, nil, err
		}
	}
	haulerPath := filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hauler")
	if err := initializeRemoteFileserverStore(ctx, request, haulerPath, subprocess.NewRunner()); err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	lock, err := lockRemoteServices(serviceRoot)
	if err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	defer unlockRemoteServices(lock)
	store, err := journal.NewStore(filepath.Join(serviceRoot, "journal"))
	if err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	snapshot, _, err := store.Load(ctx, request.SessionID)
	if errors.Is(err, os.ErrNotExist) {
		now := time.Now().UTC()
		snapshot = domain.JournalSnapshot{
			SchemaVersion: domain.SchemaVersion, SessionID: request.SessionID,
			State: domain.SessionOpen, CreatedAt: now, UpdatedAt: now,
		}
		if err := store.Create(ctx, snapshot); err != nil {
			return domain.ProcessRecord{}, nil, err
		}
	} else if err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	processes, err := supervisoradapter.NewProcessManager()
	if err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	supervisorStatus, err := processes.InspectPID(ctx, os.Getpid())
	if err != nil || !supervisorStatus.Running {
		return domain.ProcessRecord{}, nil, errors.Join(err, ErrServiceEvidence)
	}
	supervisorRecord := remoteProcessRecord(supervisorStatus.Executable, supervisorStatus)
	if err := validateServiceActors(runtimeState.worker, supervisorRecord); err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	controller := supervisoradapter.NewServiceSupervisor(
		store, processes, supervisoradapter.NewUnitInspector(subprocess.NewRunner(), http.DefaultClient),
	)
	childContextPrefix, err := remoteChildContextPrefix("/sys/fs/selinux/enforce", "/usr/bin/runcon")
	if err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	capability := remoteConfinementCapability(request, runtimeState.manifest.Tools.Pasta.Version, childContextPrefix)
	records := make([]domain.ServiceUnitRecord, 0, len(definitions))
	for _, definition := range definitions {
		spec, err := remoteServiceSpec(request, definition, capability)
		if err != nil {
			return domain.ProcessRecord{}, nil, err
		}
		record, next, err := ensureRemoteService(ctx, controller, snapshot, spec)
		if err != nil {
			return domain.ProcessRecord{}, nil, err
		}
		records = append(records, record)
		snapshot = next
	}
	actorsRoot := filepath.Join(serviceRoot, "actors")
	if err := secureMkdirAllOperation(actorsRoot); err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	evidence := ServiceActorEvidence{
		SchemaVersion: ProtocolSchemaVersion, SessionID: request.SessionID,
		Worker: runtimeState.worker, Supervisor: supervisorRecord,
	}
	evidencePath := filepath.Join(actorsRoot, strconv.Itoa(runtimeState.worker.Identity.PID)+"-"+
		strconv.FormatUint(runtimeState.worker.Identity.StartTicks, 10)+".json")
	if err := publishServiceActorEvidence(evidencePath, evidence); err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	if err := observeServiceActorEvidence(evidencePath, evidence); err != nil {
		return domain.ProcessRecord{}, nil, err
	}
	return supervisorRecord, records, nil
}

func initializeRemoteFileserverStore(ctx context.Context, request Request, haulerPath string, runner ports.Runner) error {
	transferRoot := filepath.Join(request.WorkspaceRoot, ".camp", "transfer")
	seedPath := filepath.Join(transferRoot, "fileserver-seed")
	body := []byte(request.SessionID + "\n")
	digest := sha256.Sum256(body)
	expected := FileIdentity{Name: filepath.Base(seedPath), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	if err := publishStableBytes(seedPath, body, expected); err != nil {
		return fmt.Errorf("initialize remote fileserver seed: %w", err)
	}
	store := filepath.Join(transferRoot, "fileserver-store")
	result, err := hauleradapter.NewClient(haulerPath, runner).AddFile(ctx, store, seedPath, "camp-session-seed")
	if err != nil || result.ExitCode != 0 {
		return errors.Join(err, errors.New("initialize remote fileserver Hauler store"))
	}
	return nil
}

func lockRemoteServices(root string) (*os.File, error) {
	path := filepath.Join(root, "start.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock.Chmod(0o600); err != nil {
		lock.Close()
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func unlockRemoteServices(lock *os.File) {
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
}

func remoteServiceDefinitions(request Request) ([]hauleradapter.ServiceDefinition, error) {
	runtimeRoot := filepath.Join(request.WorkspaceRoot, ".camp", "runtime")
	serviceRoot := filepath.Join(runtimeRoot, "services")
	common := hauleradapter.ServiceDefinitionOptions{
		HaulerExecutable: filepath.Join(runtimeRoot, "hauler"),
		StoreDirectory:   filepath.Join(request.RuntimeRoot, "kit", "store"),
	}
	registryOptions := common
	registryOptions.OverlayDirectory = filepath.Join(runtimeRoot, "registry")
	registryOptions.GuestPort = remoteRegistryGuestPort
	registryOptions.LogPath = filepath.Join(serviceRoot, "registry.log")
	registryOptions.PIDPath = filepath.Join(serviceRoot, "registry.pid")
	registryOptions.ReadOnly = false
	registry, err := hauleradapter.NewRegistryServiceDefinition(registryOptions)
	if err != nil {
		return nil, err
	}
	fileserverOptions := common
	fileserverOptions.StoreDirectory = filepath.Join(request.WorkspaceRoot, ".camp", "transfer", "fileserver-store")
	fileserverOptions.OverlayDirectory = filepath.Join(request.WorkspaceRoot, ".camp", "transfer", "export")
	fileserverOptions.GuestPort = remoteFileserverGuestPort
	fileserverOptions.LogPath = filepath.Join(serviceRoot, "fileserver.log")
	fileserverOptions.PIDPath = filepath.Join(serviceRoot, "fileserver.pid")
	fileserverOptions.TimeoutSeconds = 24 * 60 * 60
	fileserver, err := hauleradapter.NewFileserverServiceDefinition(fileserverOptions)
	if err != nil {
		return nil, err
	}
	return []hauleradapter.ServiceDefinition{registry, fileserver}, nil
}

func remoteServiceSpec(request Request, definition hauleradapter.ServiceDefinition, capability supervisoradapter.ConfinementCapability) (supervisoradapter.ServiceSpec, error) {
	command, err := definition.Command()
	if err != nil {
		return supervisoradapter.ServiceSpec{}, err
	}
	hostPort := remoteFileserverPort
	if definition.Name == hauleradapter.RegistryServiceName {
		hostPort = remoteRegistryPort
	}
	return supervisoradapter.ServiceSpec{
		SessionID: request.SessionID, Name: definition.Name,
		LaunchToken: request.SessionID + "-" + definition.Name + "-v1",
		Capability:  capability,
		Mapping: supervisoradapter.PortMapping{
			HostAddress: "127.0.0.1", HostPort: hostPort, GuestPort: definition.GuestPort,
		},
		LogPath: definition.LogPath, PIDPath: definition.PIDPath, Child: command,
	}, nil
}

func ensureRemoteService(ctx context.Context, controller remoteServiceController, snapshot domain.JournalSnapshot, spec supervisoradapter.ServiceSpec) (domain.ServiceUnitRecord, domain.JournalSnapshot, error) {
	for _, record := range snapshot.Services {
		if record.Name != spec.Name {
			continue
		}
		if !recordMatchesRemoteSpec(record, spec) {
			return domain.ServiceUnitRecord{}, snapshot, fmt.Errorf("%w: recorded %s service differs from requested identity", ErrServiceEvidence, spec.Name)
		}
		observation, err := controller.Observe(ctx, record)
		if err != nil {
			return domain.ServiceUnitRecord{}, snapshot, err
		}
		if observation.State == supervisoradapter.UnitLive {
			return observation.Record, snapshot, nil
		}
		restartToken := spec.LaunchToken + "-restart-" + strconv.Itoa(record.Child.Identity.PID) +
			"-" + strconv.FormatUint(record.Child.Identity.StartTicks, 10)
		restarted, next, err := controller.Restart(ctx, spec.SessionID, spec.Name, restartToken)
		return restarted, next, err
	}
	record, next, err := controller.Ensure(ctx, snapshot, spec)
	if err != nil {
		return domain.ServiceUnitRecord{}, snapshot, serviceLaunchError(spec, err)
	}
	return record, next, nil
}

func serviceLaunchError(spec supervisoradapter.ServiceSpec, launchErr error) error {
	file, err := os.OpenFile(spec.LogPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return launchErr
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return launchErr
	}
	body, err := io.ReadAll(io.LimitReader(file, maxDiagnosticBytes+1))
	if err != nil || len(body) == 0 {
		return launchErr
	}
	diagnostic := boundedStderrDiagnostic(body)
	if diagnostic == "" {
		return launchErr
	}
	return fmt.Errorf("%w; %s log: %s", launchErr, spec.Name, diagnostic)
}

func recordMatchesRemoteSpec(record domain.ServiceUnitRecord, spec supervisoradapter.ServiceSpec) bool {
	wantArgv := append([]string{spec.Child.Executable}, spec.Child.Argv...)
	processSpec, err := supervisoradapter.BuildPastaLoopback(supervisoradapter.PastaLoopback{
		Capability: spec.Capability, Mapping: spec.Mapping, LogPath: spec.LogPath,
		PIDPath: spec.PIDPath, Child: spec.Child,
	})
	if err != nil {
		return false
	}
	wantHelperArgv := append([]string{processSpec.Command.Executable}, processSpec.Command.Argv...)
	helperDigest := sha256.Sum256([]byte(strings.Join(wantHelperArgv, "\x00")))
	return validRemoteLaunchToken(record.LaunchToken, spec.LaunchToken) &&
		record.Confinement.Executable == spec.Capability.Executable &&
		record.Confinement.Version == spec.Capability.Version &&
		record.Confinement.EnvironmentFingerprint == spec.Capability.EnvironmentFingerprint &&
		record.Confinement.Boundary == spec.Capability.Boundary &&
		record.Mapping == (domain.EndpointMapping{
			HostAddress: spec.Mapping.HostAddress, HostPort: spec.Mapping.HostPort, GuestPort: spec.Mapping.GuestPort,
		}) &&
		record.PIDPath == spec.PIDPath && record.LogPath == spec.LogPath &&
		record.Helper.DesiredExecutable == spec.Capability.Executable &&
		reflect.DeepEqual(record.Helper.Argv, wantHelperArgv) &&
		record.Helper.ArgvSHA256 == hex.EncodeToString(helperDigest[:]) &&
		record.Child.DesiredExecutable == spec.Child.Executable &&
		reflect.DeepEqual(record.Child.Argv, wantArgv)
}

func validRemoteLaunchToken(recorded, initial string) bool {
	if recorded == initial {
		return true
	}
	suffix := strings.TrimPrefix(recorded, initial+"-restart-")
	parts := strings.Split(suffix, "-")
	if suffix == recorded || len(parts) != 2 {
		return false
	}
	pid, pidErr := strconv.Atoi(parts[0])
	_, ticksErr := strconv.ParseUint(parts[1], 10, 64)
	return pid > 0 && pidErr == nil && ticksErr == nil
}

func remoteConfinementCapability(request Request, version string, childContextPrefix []string) supervisoradapter.ConfinementCapability {
	executable := filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "pasta")
	source := strings.Join(append([]string{
		executable, version, "remote-workspace", runtime.GOOS, runtime.GOARCH, strconv.Itoa(os.Getuid()),
	}, childContextPrefix...), "\x00")
	digest := sha256.Sum256([]byte(source))
	return supervisoradapter.ConfinementCapability{
		Executable: executable, Version: version,
		EnvironmentFingerprint: hex.EncodeToString(digest[:]), Boundary: "remote-workspace",
		ChildContextPrefix: append([]string(nil), childContextPrefix...),
	}
}

func remoteChildContextPrefix(enforcePath, runconPath string) ([]string, error) {
	enforcing, err := os.ReadFile(enforcePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read SELinux enforcement state: %w", err)
	}
	if strings.TrimSpace(string(enforcing)) != "1" {
		return nil, nil
	}
	if !filepath.IsAbs(runconPath) || filepath.Clean(runconPath) != runconPath {
		return nil, errors.New("SELinux runcon path is not exact and absolute")
	}
	file, err := os.OpenFile(runconPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open exact SELinux runcon: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, errors.Join(err, errors.New("exact SELinux runcon is not an executable regular file"))
	}
	return []string{runconPath, "-t", "unconfined_t"}, nil
}

func remoteProcessRecord(desired string, status ports.ProcessStatus) domain.ProcessRecord {
	digest := sha256.Sum256([]byte(strings.Join(status.Argv, "\x00")))
	return domain.ProcessRecord{
		Identity: status.Identity, DesiredExecutable: desired, ObservedExecutable: status.Executable,
		Argv: append([]string(nil), status.Argv...), ArgvSHA256: hex.EncodeToString(digest[:]),
		ParentPID: status.ParentPID, PGID: status.PGID, SID: status.SID, NetNS: status.NetNS,
	}
}

func readStableIdentityFile(path string, expected FileIdentity) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != expected.Size {
		return nil, ErrIdentityMismatch
	}
	body, err := io.ReadAll(io.LimitReader(file, expected.Size+1))
	if err != nil || int64(len(body)) != expected.Size {
		return nil, errors.Join(err, ErrIdentityMismatch)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
		return nil, ErrIdentityMismatch
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != expected.SHA256 {
		return nil, ErrIdentityMismatch
	}
	return body, nil
}

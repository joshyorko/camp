package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/host"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	lifecycleadapter "github.com/joshyorko/camp/internal/adapters/lifecycle"
	"github.com/joshyorko/camp/internal/adapters/objectstore"
	"github.com/joshyorko/camp/internal/adapters/sshtransfer"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/checkpoint"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/images"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/registry"
	"github.com/joshyorko/camp/internal/target"
	"github.com/joshyorko/camp/internal/workspace"
)

type ProductionLifecycle struct{}

func NewProductionLifecycle() *ProductionLifecycle { return &ProductionLifecycle{} }

func (p *ProductionLifecycle) Init(ctx context.Context, root string, mode OutputMode, out io.Writer) error {
	composition, err := composeProduction(ctx)
	if err != nil {
		return err
	}
	if root == "" {
		root = composition.runtime.Source
	}
	if root == "" {
		return UsageError(errors.New("init requires a root or CAMP_SOURCE"))
	}
	result, err := composition.initializer.Initialize(ctx, root, composition.runtime.Capsule)
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "init", result, fmt.Sprintf("Initialized %s at %s\n", result.Metadata.ID, root))
}

func (p *ProductionLifecycle) Open(ctx context.Context, value string, mode OutputMode, out io.Writer) error {
	composition, err := composeProduction(ctx)
	if err != nil {
		return err
	}
	machine, err := productionMachineID(ctx)
	if err != nil {
		return err
	}
	services, err := composeServiceBundle(composition)
	if err != nil {
		return err
	}
	explicitRoot, landing := "", value
	if value != "" {
		if info, statErr := os.Stat(value); statErr == nil && info.IsDir() {
			explicitRoot, landing = value, ""
		}
	}
	if explicitRoot == "" {
		explicitRoot = composition.runtime.Source
	}
	provider, localProvider, err := resolveProductionProvider()
	if err != nil {
		return err
	}
	request := app.OpenRequest{
		Capsule: composition.runtime.Capsule, ExplicitRoot: explicitRoot, Target: landing,
		Runtime: composition.runtime, ResolvedBackend: composition.backend,
		Mode: domain.SessionReadWrite, EntryMode: domain.EntryTerminal,
		Machine: machine, RemoteAvailable: explicitRoot == "", Provider: provider, LocalProvider: localProvider,
	}
	usecase, err := composeOpen(ctx, composition, services)
	if err != nil {
		return err
	}
	result, err := usecase.Run(ctx, request)
	if err != nil {
		return err
	}
	if result.Snapshot.Mode == domain.SessionReadWrite {
		if err := startSessionSupervisor(ctx, composition, services.processes, result.Snapshot.SessionID); err != nil {
			return err
		}
	}
	return writeSuccess(out, mode, "open", result, fmt.Sprintf("Opened %s (%s)\n", result.Snapshot.Capsule, result.Snapshot.SessionID))
}

func resolveProductionProvider() (string, bool, error) {
	provider := strings.TrimSpace(os.Getenv("CAMP_DEVPOD_PROVIDER"))
	if provider == "" {
		return "", true, nil
	}
	if provider == "." || provider == ".." || strings.ContainsAny(provider, "/\\\t\r\n ") {
		return "", false, errors.New("CAMP_DEVPOD_PROVIDER is invalid")
	}
	return provider, provider == "docker" || provider == "podman", nil
}

func (p *ProductionLifecycle) Supervise(ctx context.Context, sessionID string, mode OutputMode, out io.Writer) error {
	return runSupervisor(ctx, sessionID, composeSupervisorHeartbeat)
}

func productionMachineID(ctx context.Context) (string, error) {
	return resolveMachineID(ctx, host.NewIdentity().MachineID, os.Hostname)
}

func resolveMachineID(ctx context.Context, primary func(context.Context) (string, error), hostname func() (string, error)) (string, error) {
	machine, err := primary(ctx)
	if err == nil {
		return machine, nil
	}
	name, hostErr := hostname()
	if hostErr != nil || strings.TrimSpace(name) == "" {
		return "", errors.Join(err, hostErr, errors.New("hostname identity is empty"))
	}
	return "hostname:" + strings.TrimSpace(name), nil
}

func composeOpen(ctx context.Context, composition productionComposition, services serviceBundle) (*app.Open, error) {
	return app.NewOpenWithBackend(ctx, app.OpenDependencies{
		Journal: composition.journal, Paths: composition.paths, ResolvedBackend: composition.backend,
		Ownership: composition.ownership, Initializer: composition.initializer,
		Services: services.starter, Forwarders: lifecycleadapter.NewForwarderManager(composition.devpod, services.processes),
		Hardlinks: workspace.NewHardlinkRestorer(composition.devpod),
		Images:    images.NewRestorer(composition.devpod, registry.NewCatalog(http.DefaultClient, 100)),
		Hydrator:  hydration.NewController(nil, composition.hauler, archive.NewTarZstd(), composition.ownership, hydration.Hooks{}),
		DevPod:    composition.devpod, Providers: composition.devpod,
		Target: target.Resolver{Zoxide: target.NewCommandZoxide("zoxide", composition.runner)}, Clock: composition.clock,
	}, composition.backend, objectstore.Options{})
}

func (p *ProductionLifecycle) Sync(ctx context.Context, mode OutputMode, out io.Writer) error {
	c, err := composeLifecycle(ctx)
	if err != nil {
		return err
	}
	session, err := app.SelectActiveSession(ctx, c.base.journal, app.SessionSelector{Capsule: c.base.runtime.Capsule, Branch: "main"})
	if err != nil {
		return err
	}
	result, err := app.NewSync(c.base.journal, c.locks, c.publisher).Run(ctx, session.SessionID)
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "sync", result, fmt.Sprintf("Published checkpoint %d\n", result.Generation.Generation))
}

func (p *ProductionLifecycle) Close(ctx context.Context, mode OutputMode, out io.Writer) error {
	c, err := composeLifecycle(ctx)
	if err != nil {
		return err
	}
	session, err := app.SelectActiveSession(ctx, c.base.journal, app.SessionSelector{Capsule: c.base.runtime.Capsule, Branch: "main"})
	if err != nil {
		return err
	}
	result, err := c.close.Run(ctx, app.CloseRequest{SessionID: session.SessionID})
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "close", result, fmt.Sprintf("Closed %s\n", session.SessionID))
}

func (p *ProductionLifecycle) Reopen(ctx context.Context, value string, mode OutputMode, out io.Writer) error {
	return p.Open(ctx, value, mode, out)
}

func (p *ProductionLifecycle) Recover(ctx context.Context, value string, mode OutputMode, out io.Writer) error {
	c, err := composeLifecycle(ctx)
	if err != nil {
		return err
	}
	selector := app.SessionSelector{SessionID: value, Capsule: c.base.runtime.Capsule, Branch: "main"}
	result, err := c.recover.Run(ctx, selector)
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "recover", result, fmt.Sprintf("Recovered %s\n", result.Session.ID))
}

type lifecycleComposition struct {
	base      productionComposition
	locks     *journalstore.OperationLocker
	leases    *coordination.LeaseRepository
	publisher *app.CheckpointPublisher
	close     *app.Close
	recover   *app.Recover
}

type supervisorBootstrap struct {
	base  productionBase
	locks *journalstore.OperationLocker
}

type supervisorHeartbeat struct {
	leases *coordination.LeaseRepository
}

type productionBase struct {
	paths     config.XDGPaths
	runtime   config.Runtime
	backend   config.Backend
	journal   ports.Journal
	ownership *capsule.Ownership
	clock     *host.Clock
}

type cleanupReconciler struct {
	journal ports.Journal
	close   *app.Close
}

func newCleanupReconciler(journal ports.Journal, close *app.Close) *cleanupReconciler {
	return &cleanupReconciler{journal: journal, close: close}
}
func (r *cleanupReconciler) Reconcile(ctx context.Context, sessionID string) (domain.JournalSnapshot, error) {
	if _, err := r.close.Run(ctx, app.CloseRequest{SessionID: sessionID}); err != nil {
		return domain.JournalSnapshot{}, err
	}
	snapshot, _, err := r.journal.Load(ctx, sessionID)
	return snapshot, err
}

func composeLifecycle(ctx context.Context) (lifecycleComposition, error) {
	base, err := composeProduction(ctx)
	if err != nil {
		return lifecycleComposition{}, err
	}
	services, err := composeServiceBundle(base)
	if err != nil {
		return lifecycleComposition{}, err
	}
	store, err := objectstore.NewWriter(ctx, base.backend, objectstore.Options{})
	if err != nil {
		return lifecycleComposition{}, err
	}
	locks, err := journalstore.NewOperationLocker(base.paths.DataRoot, host.NewIdentity())
	if err != nil {
		return lifecycleComposition{}, err
	}
	leases := coordination.NewLeaseRepository(store)
	generations := coordination.NewGenerationRepository(store)
	pointers := coordination.NewPointerRepository(store)
	catalog := registry.NewCatalog(http.DefaultClient, 100)
	barrier := lifecycleadapter.NewRegistryBarrier(base.journal, services.units)
	pipeline := app.CheckpointPipeline{Capturer: images.NewCapturer(base.devpod, catalog, base.clock), Sealer: registry.NewSnapshotter(barrier), Refresher: lifecycleadapter.NewServingRefresher(base.journal, services.units)}
	builder := checkpoint.NewBuilder(archive.NewTarZstd(), hauler.NewGenerationAssembler(base.hauler))
	transfer := sshtransfer.NewExecutor()
	remote := workspace.NewRequestBoundRemote(
		workspace.RemoteConfig{DevPodExecutable: base.devpodExecutable, TarExecutable: "tar"},
		base.devpod,
		workspace.NewMirrorStaging(filepath.Join(base.paths.DataRoot, "mirrors")),
		transfer,
		transfer,
	)
	publisher := app.NewCheckpointPublisher(base.journal, locks, leases, app.CheckpointTransports{Local: workspace.Local{}, Remote: remote}, pipeline, builder, generations, pointers, base.clock)
	effects := lifecycleadapter.NewCloseEffects(base.devpod, services.processes, services.units, leases, base.ownership)
	closeUsecase := app.NewClose(base.journal, locks, publisher, effects, base.clock)
	observer := lifecycleadapter.NewSessionObserver(services.processes, services.units)
	guard := app.NewRecoverySafetyGuard(base.ownership, leases, base.clock)
	openUsecase, err := composeOpen(ctx, base, services)
	if err != nil {
		return lifecycleComposition{}, err
	}
	recover := app.NewRecover(base.journal, observer, guard, openUsecase, newCleanupReconciler(base.journal, closeUsecase))
	return lifecycleComposition{base: base, locks: locks, leases: leases, publisher: publisher, close: closeUsecase, recover: recover}, nil
}

type productionComposition struct {
	productionBase
	initializer      *capsule.Initializer
	runner           *subprocess.Runner
	devpod           *devpod.Client
	devpodExecutable string
	hauler           *hauler.Client
}

type serviceBundle struct {
	starter   app.OpenServiceStarter
	units     *supervisor.ServiceSupervisor
	processes ports.ProcessManager
}

func startSessionSupervisor(ctx context.Context, composition productionComposition, processes ports.ProcessManager, sessionID string) error {
	campBinary, err := os.Executable()
	if err != nil {
		return err
	}
	campBinary, err = filepath.Abs(campBinary)
	if err != nil {
		return err
	}
	campBinary, err = filepath.EvalSymlinks(campBinary)
	if err != nil {
		return err
	}
	logPath := filepath.Join(composition.paths.DataRoot, "supervisors", sessionID+".log")
	identity, err := processes.Start(ctx, ports.ProcessSpec{
		Command: ports.Command{
			Executable: campBinary,
			Argv:       []string{"supervise", sessionID},
		},
		NewSession: true,
		LogPath:    logPath,
	})
	if err != nil {
		return err
	}
	if err := waitForSupervisorClaim(ctx, composition.journal, processes, sessionID, identity); err != nil {
		_ = processes.Stop(context.WithoutCancel(ctx), identity, 5*time.Second)
		return err
	}
	return nil
}

func waitForSupervisorClaim(ctx context.Context, journal ports.Journal, processes ports.ProcessManager, sessionID string, identity domain.ProcessIdentity) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		snapshot, _, err := journal.Load(ctx, sessionID)
		if err != nil {
			return err
		}
		if snapshot.Supervisor.Identity == identity && snapshot.Supervisor.Desired == domain.RuntimeDesiredRunning && snapshot.Supervisor.Observed == domain.RuntimeObservedReady {
			return nil
		}
		status, inspectErr := processes.Inspect(ctx, identity)
		if inspectErr != nil {
			return inspectErr
		}
		if !status.Running {
			return fmt.Errorf("session supervisor exited before readiness was recorded for %s", sessionID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session supervisor claim was not recorded for %s", sessionID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func composeServiceBundle(composition productionComposition) (serviceBundle, error) {
	haulerPath, err := exec.LookPath("hauler")
	if err != nil {
		return serviceBundle{}, fmt.Errorf("resolve locked Hauler executable: %w", err)
	}
	haulerPath, err = filepath.Abs(haulerPath)
	if err != nil {
		return serviceBundle{}, err
	}
	haulerPath, err = filepath.EvalSymlinks(haulerPath)
	if err != nil {
		return serviceBundle{}, fmt.Errorf("resolve locked Hauler executable target: %w", err)
	}
	processes, err := supervisor.NewProcessManager()
	if err != nil {
		return serviceBundle{}, err
	}
	units := supervisor.NewServiceSupervisor(composition.journal, processes, supervisor.NewUnitInspector(composition.runner, http.DefaultClient))
	confinement := supervisor.NewConfinementResolver(composition.runner, exec.LookPath, func() string { return "host" })
	return serviceBundle{starter: lifecycleadapter.NewServiceStarter(confinement, supervisor.NewPortAllocator(), units, haulerPath, composition.hauler), units: units, processes: processes}, nil
}

func composeSupervisorBootstrap(ctx context.Context) (supervisorBootstrap, error) {
	base, err := composeProductionBase(ctx)
	if err != nil {
		return supervisorBootstrap{}, err
	}
	locks, err := journalstore.NewOperationLocker(base.paths.DataRoot, host.NewIdentity())
	if err != nil {
		return supervisorBootstrap{}, err
	}
	return supervisorBootstrap{base: base, locks: locks}, nil
}

func composeSupervisorHeartbeat(ctx context.Context, base productionBase) (supervisorHeartbeat, error) {
	store, err := objectstore.NewWriter(ctx, base.backend, objectstore.Options{})
	if err != nil {
		return supervisorHeartbeat{}, err
	}
	return supervisorHeartbeat{leases: coordination.NewLeaseRepository(store)}, nil
}

func runSupervisor(ctx context.Context, sessionID string, composeHeartbeat func(context.Context, productionBase) (supervisorHeartbeat, error)) error {
	bootstrap, err := composeSupervisorBootstrap(ctx)
	if err != nil {
		return err
	}
	claimed := app.NewSupervise(bootstrap.base.journal, nil, bootstrap.locks, bootstrap.base.clock, time.Minute, host.NewIdentity())
	if err := claimed.Claim(ctx, sessionID); err != nil {
		return err
	}
	heartbeat, err := composeHeartbeat(ctx, bootstrap.base)
	if err != nil {
		return err
	}
	if err := claimed.MarkReady(ctx, sessionID); err != nil {
		return err
	}
	return app.NewSupervise(bootstrap.base.journal, heartbeat.leases, bootstrap.locks, bootstrap.base.clock, time.Minute).RunClaimed(ctx, sessionID)
}

func composeProductionBase(ctx context.Context) (productionBase, error) {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return productionBase{}, err
	}
	bootstrap, err := config.ResolveBootstrap(config.BootstrapInput{ConfigPath: paths.ConfigPath, Environment: environment})
	if err != nil {
		return productionBase{}, err
	}
	if bootstrap.Backend == "" {
		bootstrap.Backend = "file://" + filepath.Join(paths.DataRoot, "backend")
	}
	runtime, err := config.ResolveRuntime(config.RuntimeInput{Bootstrap: bootstrap, Environment: environment})
	if err != nil {
		return productionBase{}, err
	}
	backend, err := config.ResolveBackend(runtime.Backend, runtime.S3)
	if err != nil {
		return productionBase{}, err
	}
	journal, err := journalstore.NewStore(paths.DataRoot)
	if err != nil {
		return productionBase{}, err
	}
	ownership, err := capsule.NewOwnership(paths.DataRoot)
	if err != nil {
		return productionBase{}, err
	}
	clock := host.NewClock()
	return productionBase{paths: paths, runtime: runtime, backend: backend, journal: journal, ownership: ownership, clock: clock}, nil
}

func composeProduction(ctx context.Context) (productionComposition, error) {
	base, err := composeProductionBase(ctx)
	if err != nil {
		return productionComposition{}, err
	}
	runner := subprocess.NewRunner()
	devpodPath, err := exec.LookPath("devpod")
	if err != nil {
		return productionComposition{}, fmt.Errorf("resolve DevPod executable: %w", err)
	}
	devpodPath, err = filepath.Abs(devpodPath)
	if err != nil {
		return productionComposition{}, err
	}
	devpodPath, err = filepath.EvalSymlinks(devpodPath)
	if err != nil {
		return productionComposition{}, fmt.Errorf("resolve DevPod executable target: %w", err)
	}
	return productionComposition{
		productionBase: base,
		initializer:    capsule.NewInitializer(base.clock, capsule.NewCommandDigestResolver("docker", runner)),
		runner:         runner, devpod: devpod.NewClient(devpodPath, runner), devpodExecutable: devpodPath, hauler: hauler.NewClient("hauler", runner),
	}, nil
}

func environmentMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, item, ok := strings.Cut(value, "=")
		if ok {
			result[key] = item
		}
	}
	return result
}

func writeSuccess(out io.Writer, mode OutputMode, kind string, result any, human string) error {
	if mode == ModeHuman {
		_, err := io.WriteString(out, human)
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Result        any    `json:"result"`
	}{SchemaVersion: 1, Kind: kind, Result: result})
}

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

	"github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/host"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	lifecycleadapter "github.com/joshyorko/camp/internal/adapters/lifecycle"
	"github.com/joshyorko/camp/internal/adapters/objectstore"
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
	request := app.OpenRequest{
		Capsule: composition.runtime.Capsule, ExplicitRoot: explicitRoot, Target: landing,
		Runtime: composition.runtime, ResolvedBackend: composition.backend,
		Mode: domain.SessionReadWrite, EntryMode: domain.EntryTerminal,
		Machine: machine, RemoteAvailable: explicitRoot == "",
	}
	usecase, err := composeOpen(ctx, composition, services.starter)
	if err != nil {
		return err
	}
	result, err := usecase.Run(ctx, request)
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "open", result, fmt.Sprintf("Opened %s (%s)\n", result.Snapshot.Capsule, result.Snapshot.SessionID))
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

func composeOpen(ctx context.Context, composition productionComposition, services app.OpenServiceStarter) (*app.Open, error) {
	return app.NewOpenWithBackend(ctx, app.OpenDependencies{
		Journal: composition.journal, Paths: composition.paths, ResolvedBackend: composition.backend,
		Ownership: composition.ownership, Initializer: composition.initializer,
		Services: services,
		Hydrator: hydration.NewController(nil, composition.hauler, archive.NewTarZstd(), composition.ownership, hydration.Hooks{}),
		DevPod:   composition.devpod, Providers: composition.devpod,
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
	publisher *app.CheckpointPublisher
	close     *app.Close
	recover   *app.Recover
}

type cleanupReconciler struct {
	journal *journalstore.Store
	close   *app.Close
}

func newCleanupReconciler(journal *journalstore.Store, close *app.Close) *cleanupReconciler {
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
	pipeline := app.CheckpointPipeline{Capturer: images.NewCapturer(base.devpod, catalog, base.clock), Sealer: registry.NewSnapshotter(catalog, barrier), Refresher: lifecycleadapter.NewServingRefresher(base.journal, services.units)}
	builder := checkpoint.NewBuilder(archive.NewTarZstd(), hauler.NewGenerationAssembler(base.hauler))
	publisher := app.NewCheckpointPublisher(base.journal, locks, leases, app.CheckpointTransports{Local: workspace.Local{}}, pipeline, builder, generations, pointers, base.clock)
	effects := lifecycleadapter.NewCloseEffects(base.devpod, services.processes, services.units, leases, base.ownership)
	closeUsecase := app.NewClose(base.journal, locks, publisher, effects, base.clock)
	observer := lifecycleadapter.NewSessionObserver(services.processes, services.units)
	guard := app.NewRecoverySafetyGuard(base.ownership, leases, base.clock)
	openUsecase, err := composeOpen(ctx, base, services.starter)
	if err != nil {
		return lifecycleComposition{}, err
	}
	recover := app.NewRecover(base.journal, observer, guard, openUsecase, newCleanupReconciler(base.journal, closeUsecase))
	return lifecycleComposition{base: base, locks: locks, publisher: publisher, close: closeUsecase, recover: recover}, nil
}

type productionComposition struct {
	paths       config.XDGPaths
	runtime     config.Runtime
	backend     config.Backend
	journal     *journalstore.Store
	ownership   *capsule.Ownership
	initializer *capsule.Initializer
	runner      *subprocess.Runner
	clock       *host.Clock
	devpod      *devpod.Client
	hauler      *hauler.Client
}

type serviceBundle struct {
	starter   app.OpenServiceStarter
	units     *supervisor.ServiceSupervisor
	processes *supervisor.ProcessManager
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

func composeProduction(ctx context.Context) (productionComposition, error) {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return productionComposition{}, err
	}
	bootstrap, err := config.ResolveBootstrap(config.BootstrapInput{ConfigPath: paths.ConfigPath, Environment: environment})
	if err != nil {
		return productionComposition{}, err
	}
	if bootstrap.Backend == "" {
		bootstrap.Backend = "file://" + filepath.Join(paths.DataRoot, "backend")
	}
	runtime, err := config.ResolveRuntime(config.RuntimeInput{Bootstrap: bootstrap, Environment: environment})
	if err != nil {
		return productionComposition{}, err
	}
	backend, err := config.ResolveBackend(runtime.Backend, runtime.S3)
	if err != nil {
		return productionComposition{}, err
	}
	journal, err := journalstore.NewStore(paths.DataRoot)
	if err != nil {
		return productionComposition{}, err
	}
	ownership, err := capsule.NewOwnership(paths.DataRoot)
	if err != nil {
		return productionComposition{}, err
	}
	runner := subprocess.NewRunner()
	clock := host.NewClock()
	return productionComposition{
		paths: paths, runtime: runtime, backend: backend, journal: journal, ownership: ownership,
		initializer: capsule.NewInitializer(clock, capsule.NewCommandDigestResolver("docker", runner)),
		runner:      runner, clock: clock, devpod: devpod.NewClient("devpod", runner), hauler: hauler.NewClient("hauler", runner),
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

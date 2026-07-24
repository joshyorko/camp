package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	runtimepkg "runtime"
	"strings"
	"text/tabwriter"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	campcontract "github.com/joshyorko/camp"
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
	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/checkpoint"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/doctor"
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

func (p *ProductionLifecycle) List(ctx context.Context, mode OutputMode, out io.Writer) error {
	base, err := composeProductionBase(ctx)
	if err != nil {
		return err
	}
	store, err := objectstore.New(ctx, base.backend, objectstore.Options{})
	if err != nil {
		return err
	}
	rows, err := (app.CampInventory{
		Sessions: base.journal,
		Pointers: coordination.NewPointerRepository(store),
		Backend:  base.backend.SanitizedURL,
	}).List(ctx)
	if err != nil {
		return err
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "list", rows, "")
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "CAMP\tBRANCH\tGENERATION\tSTATE\tLAST SESSION\tBACKEND")
	for _, row := range rows {
		generation := "-"
		if row.Generation != 0 {
			generation = fmt.Sprintf("%d", row.Generation)
		}
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", row.Capsule, row.Branch, generation, row.State, row.SessionID, row.Backend)
	}
	return table.Flush()
}

func (p *ProductionLifecycle) Doctor(ctx context.Context, mode OutputMode, out io.Writer) error {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return err
	}
	runner := subprocess.NewRunner()
	confinement := supervisor.NewConfinementResolver(runner, exec.LookPath, func() string { return "host" })
	pastaRuntime, err := newProductionPastaRuntime(confinement, paths.RuntimeRoot)
	if err != nil {
		return err
	}
	managedTools, err := newDoctorManagedToolResolver(campcontract.DistributionToolLock(), paths.DataRoot)
	if err != nil {
		return err
	}
	bootstrap, err := config.ResolveBootstrap(config.BootstrapInput{ConfigPath: paths.ConfigPath, Environment: environment})
	if err != nil {
		return err
	}
	doctorJournal, err := journalstore.NewStore(paths.DataRoot)
	if err != nil {
		return err
	}
	sessions, err := doctorJournal.List(ctx)
	if err != nil {
		return err
	}
	probes := []doctor.Probe{
		doctor.ManagedToolProbe{Name: "devpod", Resolver: managedTools},
		doctor.ManagedToolProbe{Name: "hauler", Resolver: managedTools},
		doctor.PastaProbe{Runtime: pastaRuntime},
		doctor.BackendProbe{
			ConfigPath: paths.ConfigPath, Environment: environment,
			DefaultBackend: "file://" + filepath.Join(paths.DataRoot, "backend"),
			OpenStore: func(ctx context.Context, backend config.Backend) (ports.ObjectStore, error) {
				return objectstore.New(ctx, backend, objectstore.Options{})
			},
			CheckCredentials: func(ctx context.Context, backend config.Backend) error {
				awsRuntime, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(backend.S3.Region))
				if err != nil {
					return err
				}
				_, err = awsRuntime.Credentials.Retrieve(ctx)
				return err
			},
		},
	}
	probes = append(probes, doctor.LinuxHostProbes()...)
	probes = append(probes, productionReachabilityProbes(bootstrap, sessions, managedTools)...)
	report := (doctor.Runner{Timeout: 5 * time.Second, Probes: probes}).Run(ctx)
	if mode == ModeJSON {
		err = doctor.RenderJSON(out, report)
	} else {
		err = doctor.RenderHuman(out, report)
	}
	if err != nil {
		return err
	}
	if report.Blocked() {
		return &ExitError{Code: ExitFailure}
	}
	return nil
}

func (p *ProductionLifecycle) Init(ctx context.Context, request InitRequest, mode OutputMode, out io.Writer) error {
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	if request.Source != "" {
		if _, err := validateConfiguredInit(request, settings.runtime.S3); err != nil {
			return UsageError(err)
		}
	}
	runner := subprocess.NewRunner()
	initializer := capsule.NewInitializer(host.NewClock(), capsule.NewCommandDigestResolver("docker", runner))
	root := request.Root
	if request.Source != "" {
		root = request.Source
	}
	if root == "" {
		root = settings.runtime.Source
	}
	if root == "" {
		return UsageError(errors.New("init requires a root or CAMP_SOURCE"))
	}
	capsuleID := settings.runtime.Capsule
	if request.Capsule != "" {
		capsuleID = request.Capsule
	}
	result, err := initializer.Initialize(ctx, root, capsuleID)
	if err != nil {
		return err
	}
	if request.Source != "" {
		written, err := persistInitConfiguration(settings.paths.ConfigPath, request, settings.runtime.S3)
		if err != nil {
			return err
		}
		return writeConfiguredInitSuccess(out, mode, configuredInitResult{
			ConfigPath: settings.paths.ConfigPath, Source: written.Source, Backend: written.Backend,
			Capsule: written.DefaultCapsule, DevPodProvider: written.DevPodProvider, DevPodContext: written.DevPodContext,
		})
	}
	return writeSuccess(out, mode, "init", result, fmt.Sprintf("Initialized %s at %s\n", result.Metadata.ID, root))
}

type configuredInitResult struct {
	ConfigPath     string `json:"configPath"`
	Source         string `json:"source"`
	Backend        string `json:"backend"`
	Capsule        string `json:"capsule"`
	DevPodProvider string `json:"devpodProvider"`
	DevPodContext  string `json:"devpodContext"`
}

func validateConfiguredInit(request InitRequest, s3 config.S3Values) (config.Backend, error) {
	value := config.Persistent{DefaultCapsule: request.Capsule, Backend: request.Backend, Source: request.Source, DevPodProvider: request.DevPodProvider, DevPodContext: request.DevPodContext, S3: s3}
	if err := config.ValidatePersistent(value); err != nil {
		return config.Backend{}, err
	}
	return config.ResolveBackend(request.Backend, s3)
}

func persistInitConfiguration(path string, request InitRequest, s3 config.S3Values) (config.Persistent, error) {
	store := config.NewStore(path)
	return store.Modify(func(value *config.Persistent) error {
		value.DefaultCapsule = request.Capsule
		value.Backend = request.Backend
		value.Source = request.Source
		value.DevPodProvider = request.DevPodProvider
		value.DevPodContext = request.DevPodContext
		value.S3 = s3
		return nil
	})
}

func writeConfiguredInitSuccess(out io.Writer, mode OutputMode, result configuredInitResult) error {
	if mode == ModeHuman {
		_, err := fmt.Fprintf(out, "Wrote %s: source=%s backend=%s capsule=%s devpod-provider=%s devpod-context=%s\n", result.ConfigPath, result.Source, result.Backend, result.Capsule, result.DevPodProvider, result.DevPodContext)
		return err
	}
	return writeSuccess(out, mode, "init", result, "")
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
	provider, localProvider, err := resolveProductionProvider(composition.runtime.DevPodProvider)
	if err != nil {
		return err
	}
	request := app.OpenRequest{
		Capsule: composition.runtime.Capsule, ExplicitRoot: explicitRoot, Target: landing,
		Runtime: composition.runtime, ResolvedBackend: composition.backend,
		Mode: domain.SessionReadWrite, EntryMode: domain.EntryTerminal,
		Machine: machine, RemoteAvailable: explicitRoot == "", Context: composition.runtime.DevPodContext, Provider: provider, LocalProvider: localProvider,
	}
	usecase, err := composeOpen(ctx, composition, services)
	if err != nil {
		return err
	}
	result, err := usecase.Run(ctx, request)
	if err != nil {
		return lifecycleFailure(err, result.RecoveryCommand)
	}
	if result.Snapshot.Mode == domain.SessionReadWrite {
		if err := startSessionSupervisor(ctx, composition, services.processes, result.Snapshot.SessionID); err != nil {
			return err
		}
	}
	if mode == ModeHuman {
		return writeHumanLifecycleResult(out, mode, "open", openTerminalEvents(result.Snapshot.Capsule, result.Snapshot.SessionID), "")
	}
	return writeSuccess(out, mode, "open", result, fmt.Sprintf("Opened %s (%s)\n", result.Snapshot.Capsule, result.Snapshot.SessionID))
}

func (p *ProductionLifecycle) Attach(ctx context.Context, request AttachRequest, mode OutputMode, out io.Writer) error {
	composition, err := composeProduction(ctx)
	if err != nil {
		return err
	}
	usecase := app.NewAttach(app.AttachDependencies{
		Sessions: composition.journal, Ownership: composition.ownership,
		Target: target.Resolver{Zoxide: target.NewCommandZoxide("zoxide", composition.runner)}, DevPod: composition.devpod,
	})
	result, err := usecase.Run(ctx, app.AttachRequest{
		Selector: app.SessionSelector{Capsule: composition.runtime.Capsule, Branch: "main"},
		Target:   request.Target, Entry: devpod.IDEEntry{IDE: devpod.IDE(request.IDE)},
		SSH: devpod.SSHOptions{
			User: request.User, ForwardPorts: request.ForwardPorts, ReverseForwards: request.ReverseForwardPorts,
			SendEnv: request.SendEnv, SetEnv: request.SetEnv, ForwardPortsTimeout: request.ForwardPortsTimeout,
			AgentForwarding: request.AgentForwarding, GPGAgentForwarding: request.GPGAgentForwarding, Stdio: request.Stdio,
			SSHKeepAliveInterval: request.SSHKeepAliveInterval, GitSSHSigningKey: request.GitSSHSigningKey,
			TermMode: request.TermMode, InstallTerminfo: request.InstallTerminfo, ForwardedArgv: request.DevPodArgs,
			Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		},
	})
	if err != nil {
		return err
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "attach", result, "")
	}
	return nil
}

func resolveProductionProvider(provider string) (string, bool, error) {
	provider = strings.TrimSpace(provider)
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
	ctx = app.WithProgressReporter(ctx, productionLifecycleProgressReporter(mode, out, "sync"))
	result, err := app.NewSync(c.base.journal, c.locks, c.publisher).Run(ctx, session.SessionID)
	if err != nil {
		recovery := result.RecoveryCommand
		if recovery == "" {
			recovery = "camp recover " + session.SessionID
		}
		return lifecycleFailure(err, recovery)
	}
	if mode == ModeHuman {
		return writeHumanLifecycleResult(out, mode, "sync", syncTerminalEvents(result.Generation.Generation), "")
	}
	return writeSuccess(out, mode, "sync", result, fmt.Sprintf("Published checkpoint %d\n", result.Generation.Generation))
}

func (p *ProductionLifecycle) Close(ctx context.Context, request CloseRequest, mode OutputMode, out io.Writer) error {
	c, err := composeLifecycle(ctx)
	if err != nil {
		return err
	}
	session, err := app.SelectActiveSession(ctx, c.base.journal, app.SessionSelector{Capsule: c.base.runtime.Capsule, Branch: "main"})
	if err != nil {
		return err
	}
	ctx = app.WithProgressReporter(ctx, productionLifecycleProgressReporter(mode, out, "close"))
	result, err := c.close.Run(ctx, app.CloseRequest{SessionID: session.SessionID, Discard: request.Discard})
	if err != nil {
		return lifecycleFailure(err, result.RecoveryCommand)
	}
	if mode == ModeHuman {
		if request.Discard {
			return writeHumanLifecycleResult(out, mode, "close", closeDiscardTerminalEvents(), "")
		}
		return writeHumanLifecycleResult(out, mode, "close", closeTerminalEvents(result.Generation.Generation, result.CleanupSucceeded), "")
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

const productionWriterLeaseTTL = 30 * time.Minute

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
	haulerExecutable string
	hauler           *hauler.Client
}

type productionSettings struct {
	paths       config.XDGPaths
	runtime     config.Runtime
	backend     config.Backend
	toolEnsurer toolEnsurer
	goos        string
	arch        string
}

type serviceBundle struct {
	starter   app.OpenServiceStarter
	units     *supervisor.ServiceSupervisor
	processes ports.ProcessManager
}

func startSessionSupervisor(ctx context.Context, composition productionComposition, processes ports.ProcessManager, sessionID string) error {
	reused, err := reuseRunningSessionSupervisor(ctx, composition.journal, processes, sessionID)
	if err != nil {
		return err
	}
	if reused {
		return nil
	}
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

func reuseRunningSessionSupervisor(ctx context.Context, journal ports.Journal, processes ports.ProcessManager, sessionID string) (bool, error) {
	snapshot, pending, err := journal.Load(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if len(pending) != 0 {
		return false, nil
	}
	record := snapshot.Supervisor
	if record.Identity.PID <= 0 || record.Identity.BootID == "" || record.Identity.StartTicks == 0 || record.Desired != domain.RuntimeDesiredRunning || record.Observed != domain.RuntimeObservedReady && record.Observed != domain.RuntimeObservedPending {
		return false, nil
	}
	status, err := processes.Inspect(ctx, record.Identity)
	if err != nil {
		if errors.Is(err, supervisor.ErrProcessIdentity) {
			return false, nil
		}
		return false, err
	}
	if !status.Running || status.Identity != record.Identity {
		return false, nil
	}
	if record.Observed == domain.RuntimeObservedPending {
		if err := waitForSupervisorClaim(ctx, journal, processes, sessionID, record.Identity); err != nil {
			return false, err
		}
	}
	return true, nil
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
	haulerPath := composition.haulerExecutable
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
	claimed := app.NewSupervise(bootstrap.base.journal, nil, bootstrap.locks, bootstrap.base.clock, productionWriterLeaseTTL, host.NewIdentity())
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
	return app.NewSupervise(bootstrap.base.journal, heartbeat.leases, bootstrap.locks, bootstrap.base.clock, productionWriterLeaseTTL, host.NewIdentity()).RunClaimed(ctx, sessionID)
}

func composeProductionBase(ctx context.Context) (productionBase, error) {
	settings, err := resolveProductionSettings()
	if err != nil {
		return productionBase{}, err
	}
	return composeProductionBaseWithSettings(settings)
}

func resolveProductionSettings() (productionSettings, error) {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return productionSettings{}, err
	}
	bootstrap, err := config.ResolveBootstrap(config.BootstrapInput{ConfigPath: paths.ConfigPath, Environment: environment})
	if err != nil {
		return productionSettings{}, err
	}
	if bootstrap.Backend == "" {
		bootstrap.Backend = "file://" + filepath.Join(paths.DataRoot, "backend")
	}
	runtime, err := config.ResolveRuntime(config.RuntimeInput{Bootstrap: bootstrap, Environment: environment})
	if err != nil {
		return productionSettings{}, err
	}
	backend, err := config.ResolveBackend(runtime.Backend, runtime.S3)
	if err != nil {
		return productionSettings{}, err
	}
	return productionSettings{paths: paths, runtime: runtime, backend: backend}, nil
}

func composeProductionBaseWithSettings(settings productionSettings) (productionBase, error) {
	paths, runtime, backend := settings.paths, settings.runtime, settings.backend
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
	settings, err := resolveProductionSettings()
	if err != nil {
		return productionComposition{}, err
	}
	return composeProductionWithSettings(ctx, settings)
}

func composeProductionWithSettings(ctx context.Context, settings productionSettings) (productionComposition, error) {
	base, err := composeProductionBaseWithSettings(settings)
	if err != nil {
		return productionComposition{}, err
	}
	runner := subprocess.NewRunner()
	ensurer := settings.toolEnsurer
	if ensurer == nil {
		lock, err := tooladapter.ParseLock(bytes.NewReader(campcontract.DistributionToolLock()))
		if err != nil {
			return productionComposition{}, err
		}
		ensurer, err = tooladapter.NewInstaller(lock, base.paths.DataRoot)
		if err != nil {
			return productionComposition{}, err
		}
	}
	goos, arch := settings.goos, settings.arch
	if goos == "" {
		goos = runtimepkg.GOOS
	}
	if arch == "" {
		arch = runtimepkg.GOARCH
	}
	toolPaths, err := resolveManagedToolPaths(ctx, ensurer, goos, arch)
	if err != nil {
		return productionComposition{}, err
	}
	devpodPath := toolPaths.devpod
	haulerPath := toolPaths.hauler
	return productionComposition{
		productionBase: base,
		initializer:    capsule.NewInitializer(base.clock, capsule.NewCommandDigestResolver("docker", runner)),
		runner:         runner, devpod: devpod.NewClient(devpodPath, runner), devpodExecutable: devpodPath,
		haulerExecutable: haulerPath, hauler: hauler.NewClient(haulerPath, runner),
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

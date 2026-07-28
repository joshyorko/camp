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
	strikeadapter "github.com/joshyorko/camp/internal/adapters/strike"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
	"github.com/joshyorko/camp/internal/app"
	"github.com/joshyorko/camp/internal/campconfig"
	"github.com/joshyorko/camp/internal/campkit"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/checkpoint"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/doctor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/images"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/presentation"
	"github.com/joshyorko/camp/internal/registry"
	"github.com/joshyorko/camp/internal/setupui"
	"github.com/joshyorko/camp/internal/target"
	"github.com/joshyorko/camp/internal/workspace"
)

type ProductionLifecycle struct {
	setupToolRunner  func(context.Context, OutputMode, io.Writer, func(string, tooladapter.Resolution) error) error
	setupInitializer func(context.Context, InitRequest, OutputMode, io.Writer) error
	prepareSync      func(context.Context) (productionSyncRun, error)
	prepareClose     func(context.Context) (productionCloseRun, error)
	richAvailable    func(OutputMode, io.Reader, io.Writer, map[string]string, terminalProbe) bool
	richRunner       richLifecycleRunner
	richSpriteLoader richLifecycleSpriteLoader
}

type productionSyncRun struct {
	sessionID string
	run       func(context.Context, app.ProgressReporter) (app.CheckpointResult, error)
}

type productionCloseRun struct {
	sessionID string
	mode      domain.SessionMode
	run       func(context.Context, CloseRequest, app.ProgressReporter) (app.CloseResult, error)
}

func NewProductionLifecycle() *ProductionLifecycle { return &ProductionLifecycle{} }

func (p *ProductionLifecycle) prepareProductionSync(ctx context.Context) (productionSyncRun, error) {
	if p.prepareSync != nil {
		return p.prepareSync(ctx)
	}
	c, err := composeLifecycle(ctx)
	if err != nil {
		return productionSyncRun{}, err
	}
	session, err := app.SelectActiveSession(ctx, c.base.journal, productionSessionSelector(ctx, c.base.runtime))
	if err != nil {
		return productionSyncRun{}, err
	}
	usecase := app.NewSync(c.base.journal, c.locks, c.publisher)
	return productionSyncRun{
		sessionID: session.SessionID,
		run: func(runCtx context.Context, reporter app.ProgressReporter) (app.CheckpointResult, error) {
			return usecase.Run(app.WithProgressReporter(runCtx, reporter), session.SessionID)
		},
	}, nil
}

func (p *ProductionLifecycle) prepareProductionClose(ctx context.Context) (productionCloseRun, error) {
	if p.prepareClose != nil {
		return p.prepareClose(ctx)
	}
	c, err := composeLifecycle(ctx)
	if err != nil {
		return productionCloseRun{}, err
	}
	session, err := app.SelectActiveSession(ctx, c.base.journal, productionSessionSelector(ctx, c.base.runtime))
	if err != nil {
		return productionCloseRun{}, err
	}
	return productionCloseRun{
		sessionID: session.SessionID,
		mode:      session.Mode,
		run: func(runCtx context.Context, request CloseRequest, reporter app.ProgressReporter) (app.CloseResult, error) {
			return c.close.Run(app.WithProgressReporter(runCtx, reporter), app.CloseRequest{SessionID: session.SessionID, Discard: request.Discard})
		},
	}, nil
}

func (p *ProductionLifecycle) richLifecycleAvailable(mode OutputMode, in io.Reader, out io.Writer) bool {
	available := p.richAvailable
	if available == nil {
		available = richLifecycleAvailable
	}
	return available(mode, in, out, environmentMap(os.Environ()), probeTerminal)
}

func (p *ProductionLifecycle) runRichLifecycle(ctx context.Context, out io.Writer, workflow setupui.LifecycleWorkflow, worker richLifecycleWorker) (string, error) {
	runner := p.richRunner
	if runner == nil {
		runner = setupui.RunLifecycle
	}
	loadSprites := p.richSpriteLoader
	if loadSprites == nil {
		loadSprites = setupui.LoadSprites
	}
	return runRichLifecycleOperationWithDependencies(ctx, os.Stdin, out, workflow, worker, loadSprites, runner)
}

func (p *ProductionLifecycle) Strike(ctx context.Context, request StrikeRequest, mode OutputMode, out io.Writer) error {
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	composition, err := composeProductionWithSettings(ctx, settings)
	if err != nil {
		return err
	}
	base := composition.productionBase
	services, err := composeServiceBundle(composition)
	if err != nil {
		return err
	}
	managedBackend := filepath.Join(base.paths.DataRoot, "backend")
	backendSafe := base.backend.Kind == config.BackendFile && base.backend.File != nil && filepath.Clean(base.backend.File.Root) == managedBackend
	names := []string{"backend", "camp", "doctor", "locks", "mirrors", "quarantine", "sessions", "stores", "supervisors"}
	targets := make([]string, 0, len(names))
	for _, name := range names {
		targets = append(targets, filepath.Join(base.paths.DataRoot, name))
	}
	effects := lifecycleadapter.NewCloseEffects(composition.devpod, services.processes, services.units, nil, base.ownership)
	usecase := app.Strike{Sessions: base.journal, Controller: strikeadapter.NewController(time.Now), Effects: effects}
	result, err := usecase.Run(ctx, app.StrikeRequest{Purge: request.Purge, Yes: request.Yes}, app.StrikePlan{
		DataRoot: base.paths.DataRoot, Targets: targets, BackendSafe: backendSafe,
	})
	if err != nil {
		return err
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "strike", result, "")
	}
	if result.Purged {
		_, err = fmt.Fprintln(out, "strike: permanently removed verified local Camp state\nnext: camp open")
		return err
	}
	_, err = fmt.Fprintf(out, "strike: archived local Camp state at %s\nnext: camp open\n", result.ArchivedPath)
	return err
}

func (p *ProductionLifecycle) List(ctx context.Context, mode OutputMode, out io.Writer) error {
	settings, err := resolveProductionSettings()
	if err != nil {
		return err
	}
	base, err := composeProductionBaseWithSettings(settings)
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

func (p *ProductionLifecycle) ConfigShow(_ context.Context, effective, _ bool, mode OutputMode, out io.Writer) error {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return err
	}
	var value any
	if effective {
		resolved, resolveErr := config.ResolveBootstrap(config.BootstrapInput{ConfigPath: paths.ConfigPath, Environment: environment})
		if resolveErr != nil {
			return resolveErr
		}
		if resolved.Backend == "" {
			resolved.Backend = "file://" + filepath.Join(paths.DataRoot, "backend")
		}
		value = resolved
	} else {
		stored, readErr := config.NewStore(paths.ConfigPath).Read()
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		value = stored
	}
	body, marshalErr := config.MarshalRedacted(value)
	if marshalErr != nil {
		return marshalErr
	}
	return writeSuccess(out, mode, "config-show", json.RawMessage(body), string(body)+"\n")
}

func (p *ProductionLifecycle) ConfigSet(_ context.Context, key, value string, mode OutputMode, out io.Writer) error {
	environment := environmentMap(os.Environ())
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environment})
	if err != nil {
		return err
	}
	updated, err := config.NewStore(paths.ConfigPath).Modify(func(current *config.Persistent) error {
		switch key {
		case "backend":
			current.Backend = value
		case "devpodProvider":
			current.DevPodProvider = value
		case "devpodContext":
			current.DevPodContext = value
		default:
			return UsageError(fmt.Errorf("unsupported config key %q", key))
		}
		return nil
	})
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "config-set", updated, fmt.Sprintf("set %s\n", key))
}

func (p *ProductionLifecycle) Status(ctx context.Context, mode OutputMode, out io.Writer) error {
	composition, err := composeProduction(ctx)
	if err != nil {
		return err
	}
	services, err := composeServiceBundle(composition)
	if err != nil {
		return err
	}
	result, err := (app.OperationalQueries{
		Sessions: composition.journal,
		Observer: lifecycleadapter.NewSessionObserver(services.processes, services.units),
	}).Status(ctx, productionSessionSelector(ctx, composition.runtime))
	if err != nil {
		return err
	}
	return writeStatusResult(out, mode, result)
}

func (p *ProductionLifecycle) ImagesList(ctx context.Context, request SessionRequest, mode OutputMode, out io.Writer) error {
	base, err := composeProductionBase(ctx)
	if err != nil {
		return err
	}
	result, err := app.NewImageOperations(base.journal, nil, nil, nil, nil, nil, nil).List(ctx, operationalSelector(request))
	if err != nil {
		return err
	}
	return writeImageResult(out, mode, "images-list", result)
}

func (p *ProductionLifecycle) ImagesRestore(ctx context.Context, request SessionRequest, mode OutputMode, out io.Writer) error {
	operations, err := composeProductionImageOperations(ctx)
	if err != nil {
		return err
	}
	result, err := operations.Restore(ctx, operationalSelector(request))
	if err != nil {
		return err
	}
	return writeImageRestoreResult(out, mode, result)
}

func (p *ProductionLifecycle) ServeStatus(ctx context.Context, request ServeRequest, mode OutputMode, out io.Writer) error {
	serve, err := composeProductionServe(ctx)
	if err != nil {
		return err
	}
	result, err := serve.Status(ctx, operationalSelector(request.Session), request.Service)
	if err != nil {
		return err
	}
	return writeServeResult(out, mode, "serve-status", result)
}

func (p *ProductionLifecycle) ServeLogs(ctx context.Context, request ServeLogsRequest, mode OutputMode, out io.Writer) error {
	serve, err := composeProductionServe(ctx)
	if err != nil {
		return err
	}
	result, err := serve.Logs(ctx, operationalSelector(request.Session), request.Service, request.TailBytes)
	if err != nil {
		return err
	}
	return writeServeLogsResult(out, mode, result)
}

func (p *ProductionLifecycle) ServeRestart(ctx context.Context, request ServeRestartRequest, mode OutputMode, out io.Writer) error {
	serve, err := composeProductionServe(ctx)
	if err != nil {
		return err
	}
	result, err := serve.Restart(ctx, app.ServeRestartRequest{
		Selector: operationalSelector(request.Session), Service: request.Service, LaunchToken: request.LaunchToken,
	})
	if err != nil {
		return err
	}
	return writeServeResult(out, mode, "serve-restart", result)
}

func (p *ProductionLifecycle) ProvidersList(ctx context.Context, mode OutputMode, out io.Writer) error {
	composition, err := composeMachineProduction(ctx)
	if err != nil {
		return err
	}
	result, err := app.NewProviders(devpodProviderReader{
		client: composition.devpod, context: composition.runtime.DevPodContext,
	}).List(ctx)
	if err != nil {
		return err
	}
	return writeProvidersResult(out, mode, result)
}

func (p *ProductionLifecycle) ProviderAdd(ctx context.Context, request ProviderMutationRequest, mode OutputMode, out io.Writer) error {
	return p.configureProvider(ctx, request, mode, out, true)
}

func (p *ProductionLifecycle) ProviderUse(ctx context.Context, request ProviderMutationRequest, mode OutputMode, out io.Writer) error {
	return p.configureProvider(ctx, request, mode, out, false)
}

func (p *ProductionLifecycle) configureProvider(ctx context.Context, request ProviderMutationRequest, mode OutputMode, out io.Writer, add bool) error {
	composition, err := composeMachineProduction(ctx)
	if err != nil {
		return err
	}
	contextName := firstNonEmpty(request.Context, composition.runtime.DevPodContext, "default")
	operations := app.NewProviderConfiguration(devpodProviderConfigurer{client: composition.devpod})
	appRequest := app.ProviderMutationRequest{Name: request.Name, Context: contextName, Options: append([]string(nil), request.Options...)}
	var result app.ProviderMutationResult
	if add {
		result, err = operations.Add(ctx, appRequest)
	} else {
		result, err = operations.Use(ctx, appRequest)
	}
	if err != nil {
		return err
	}
	return writeProviderMutationResult(out, mode, result)
}

func (p *ProductionLifecycle) KitInspect(_ context.Context, path string, mode OutputMode, out io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	result, err := campkit.Inspect(file, campkit.DefaultArchiveLimits())
	if err != nil {
		return err
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "kit-inspect", result, "")
	}
	_, err = fmt.Fprintf(out, "inspect %s (capsule=%s branch=%s generation=%d integrity=%s)\n", filepath.Base(path), result.Manifest.Generation.Capsule, result.Manifest.Generation.Branch, result.Manifest.Generation.Ref.Generation, result.Integrity)
	return err
}

func (p *ProductionLifecycle) KitVerify(ctx context.Context, path string, mode OutputMode, out io.Writer) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	result, err := campkit.Verify(ctx, file, campkit.DefaultArchiveLimits(), nil)
	if err != nil {
		return err
	}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "kit-verify", result, "")
	}
	_, err = fmt.Fprintf(out, "verify %s (capsule=%s branch=%s generation=%d integrity=%s trust=%s payloads=%d)\n", filepath.Base(path), result.Manifest.Generation.Capsule, result.Manifest.Generation.Branch, result.Manifest.Generation.Ref.Generation, result.Integrity, result.Trust, len(result.Payloads))
	return err
}

func (p *ProductionLifecycle) KitImport(ctx context.Context, request KitImportRequest, mode OutputMode, out io.Writer) error {
	if !validKitCampName(request.Camp) {
		return UsageError(fmt.Errorf("invalid --as camp name %q", request.Camp))
	}
	paths, err := config.ResolveXDGPaths(config.XDGInput{Environment: environmentMap(os.Environ())})
	if err != nil {
		return err
	}
	destination := filepath.Join(paths.DataRoot, "imported-camps", request.Camp)
	result, err := campkit.ImportFile(ctx, request.File, destination, nil)
	if err != nil {
		return err
	}
	receipt := KitImportReceipt{Camp: request.Camp, Destination: result.Destination, Generation: result.Manifest.Generation.Ref}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "kit-import", receipt, "")
	}
	_, err = fmt.Fprintf(out, "imported generation %d to %s\n", receipt.Generation.Generation, receipt.Destination)
	return err
}

type KitImportReceipt struct {
	Camp        string                `json:"camp"`
	Destination string                `json:"destination"`
	Generation  campkit.GenerationRef `json:"generation"`
}

func validKitCampName(value string) bool {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9' && i > 0) || (r == '.' || r == '-' || r == '_') {
			continue
		}
		return false
	}
	return true
}

type devpodProviderReader struct {
	client  *devpod.Client
	context string
}

type devpodProviderConfigurer struct{ client *devpod.Client }

func (c devpodProviderConfigurer) AddProvider(ctx context.Context, request app.ProviderMutationRequest) error {
	return c.client.AddProvider(ctx, devpod.ProviderRequest{Context: request.Context, Name: request.Name, Options: request.Options})
}

func (c devpodProviderConfigurer) UseProvider(ctx context.Context, request app.ProviderMutationRequest) error {
	return c.client.UseProvider(ctx, devpod.ProviderRequest{Context: request.Context, Name: request.Name, Options: request.Options})
}

func (r devpodProviderReader) ListProviders(ctx context.Context) ([]app.Provider, error) {
	names, err := r.client.ListProviderNames(ctx, r.context)
	if err != nil {
		return nil, err
	}
	result := make([]app.Provider, 0, len(names))
	for _, name := range names {
		result = append(result, app.Provider{Name: name})
	}
	return result, nil
}

func operationalSelector(request SessionRequest) app.SessionSelector {
	return app.SessionSelector{SessionID: request.SessionID, Capsule: request.Capsule, Branch: request.Branch}
}

func writeStatusResult(out io.Writer, mode OutputMode, result app.SessionReadModel) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, "status", result, "")
	}
	if _, err := fmt.Fprintln(out, "SESSION\tCAPSULE\tBRANCH\tSTATE\tMODE\tRECOVERY"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\n", result.ID, result.Capsule, result.Branch, result.State, result.Mode, result.Recovery.Condition); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(out, "SERVICE\tLIVENESS"); err != nil {
		return err
	}
	for _, service := range result.Services {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", service.Name, service.Liveness); err != nil {
			return err
		}
	}
	return nil
}

func writeImageResult(out io.Writer, mode OutputMode, kind string, result app.ImageInventoryReadModel) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, kind, result, "")
	}
	table := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(table, "SESSION\tCAPSULE\tBRANCH\tIMAGES\n%s\t%s\t%s\t%d\n", result.SessionID, result.Capsule, result.Branch, len(result.Images))
	_, _ = fmt.Fprintln(table, "REFERENCE\tDIGEST\tSOURCE")
	for _, image := range result.Images {
		_, _ = fmt.Fprintf(table, "%s\t%s\t%s\n", image.CapturedReference, image.CapturedManifestDigest, image.Source)
	}
	return table.Flush()
}

func writeImageRestoreResult(out io.Writer, mode OutputMode, result app.ImageRestoreReadModel) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, "images-restore", result, "")
	}
	_, err := fmt.Fprintf(out, "Restored %d images and %d tags for %s (%s)\n", result.Restored, result.Tags, result.Capsule, result.SessionID)
	return err
}

func writeServeResult(out io.Writer, mode OutputMode, kind string, result app.ServiceReadModel) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, kind, result, "")
	}
	_, err := fmt.Fprintf(out, "%s\t%s\n", result.Name, result.Liveness)
	return err
}

func writeProvidersResult(out io.Writer, mode OutputMode, result []app.Provider) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, "provider-list", result, "")
	}
	if _, err := fmt.Fprintln(out, "PROVIDER"); err != nil {
		return err
	}
	for _, provider := range result {
		if _, err := fmt.Fprintln(out, provider.Name); err != nil {
			return err
		}
	}
	return nil
}

func writeProviderMutationResult(out io.Writer, mode OutputMode, result app.ProviderMutationResult) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, "provider-"+result.Action, result, "")
	}
	_, err := fmt.Fprintf(out, "provider %s %s in DevPod context %s\nnext: %s\n", result.Name, result.Action, result.Context, result.NextCommand)
	return err
}

func writeServeLogsResult(out io.Writer, mode OutputMode, result supervisor.LogChunk) error {
	if mode == ModeJSON {
		return writeSuccess(out, mode, "serve-logs", struct {
			Text      string `json:"text"`
			Truncated bool   `json:"truncated"`
		}{Text: string(result.Bytes), Truncated: result.Truncated}, "")
	}
	if result.Truncated {
		if _, err := io.WriteString(out, "[earlier log bytes omitted]\n"); err != nil {
			return err
		}
	}
	_, err := out.Write(result.Bytes)
	return err
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
	if request.Migrate {
		result, err := campconfig.Migrate(settings.paths.ConfigPath)
		if err != nil {
			return err
		}
		if mode == ModeJSON {
			return writeSuccess(out, mode, "init", result, "")
		}
		if !result.Migrated {
			_, err = fmt.Fprintln(out, "Legacy singleton configuration is already migrated.")
			return err
		}
		_, err = fmt.Fprintf(out, "Migrated %s\nnext: cd %s && camp open\n", result.Manifest.Manifest.ID, shellQuoteArgument(result.Manifest.Root))
		return err
	}
	root := request.Root
	if root == "" {
		root, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	manifest, err := resolveInitManifest(settings, request, root)
	if err != nil {
		return UsageError(err)
	}
	if err := reportInitActivity(ctx, "Writing camp manifest…"); err != nil {
		return err
	}
	path, err := campconfig.Create(root, manifest)
	if err != nil {
		return err
	}
	if err := reportInitActivity(ctx, "Camp manifest written."); err != nil {
		return err
	}
	runner := subprocess.NewRunner()
	initializer := capsule.NewInitializer(host.NewClock(), capsule.NewCommandDigestResolver("docker", runner))
	if err := reportInitActivity(ctx, "Initializing capsule…"); err != nil {
		return err
	}
	result, err := initializer.Initialize(ctx, root, manifest.ID)
	if err != nil {
		return err
	}
	if err := reportInitActivity(ctx, "Capsule initialized."); err != nil {
		return err
	}
	response := struct {
		ManifestPath   string                 `json:"manifestPath"`
		Manifest       campconfig.Manifest    `json:"manifest"`
		Initialization capsule.Initialization `json:"initialization"`
		Next           string                 `json:"next"`
	}{ManifestPath: path, Manifest: manifest, Initialization: result, Next: "cd " + shellQuoteArgument(root) + " && camp open"}
	if mode == ModeJSON {
		return writeSuccess(out, mode, "init", response, "")
	}
	_, err = fmt.Fprintf(out, "Initialized %s at %s\nnext: %s\n", result.Metadata.ID, root, response.Next)
	return err
}

func resolveInitManifest(settings productionSettings, request InitRequest, root string) (campconfig.Manifest, error) {
	backend := firstNonEmpty(request.Backend, settings.runtime.Backend)
	provider := firstNonEmpty(request.DevPodProvider, settings.runtime.DevPodProvider, "docker")
	contextName := firstNonEmpty(request.DevPodContext, settings.runtime.DevPodContext, "default")
	if request.Capsule == "" {
		return campconfig.Manifest{}, errors.New("camp name is required")
	}
	manifest := campconfig.Manifest{
		SchemaVersion: campconfig.SchemaVersion,
		ID:            request.Capsule, Source: ".", Backend: backend,
		Workspace: campconfig.Workspace{Provider: provider, Context: contextName},
	}
	// Validate the resolved backend against machine S3 compatibility defaults
	// before any filesystem mutation.
	if _, err := config.ResolveBackend(backend, settings.runtime.S3); err != nil {
		return campconfig.Manifest{}, err
	}
	return manifest, nil
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
	ctx = requiringManifest(ctx)
	if value != "" {
		if info, err := os.Stat(value); err == nil && info.IsDir() {
			ctx = withCampPath(ctx, value)
		}
	}
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
		SessionID: SelectionFromContext(ctx).Session,
		Capsule:   composition.runtime.Capsule, ExplicitRoot: explicitRoot, Target: landing,
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
		Selector: productionSessionSelector(ctx, composition.runtime),
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
	confinement := supervisor.NewConfinementResolver(composition.runner, exec.LookPath, func() string { return "host" })
	remoteDataPlane := app.NewRemoteDataPlanePreparer(app.RemoteDataPlaneDependencies{
		Root:     filepath.Join(composition.paths.DataRoot, "remote-data-planes"),
		Archiver: archive.NewTarZstd(), Hauler: composition.hauler,
		Builder: haulkit.NewBuilder(composition.hauler), Verifier: haulkit.NewVerifier(composition.hauler),
		Images: capsule.NewCommandDigestResolver("docker", composition.runner), Confinement: confinement,
		HaulerExecutable: composition.haulerExecutable, HaulerVersion: composition.haulerVersion,
	})
	return app.NewOpenWithBackend(ctx, app.OpenDependencies{
		Journal: composition.journal, Paths: composition.paths, ResolvedBackend: composition.backend,
		Ownership: composition.ownership, Initializer: composition.initializer,
		Services: services.starter, Forwarders: lifecycleadapter.NewForwarderManager(composition.devpod, services.processes),
		Hardlinks: workspace.NewHardlinkRestorer(composition.devpod),
		Images:    images.NewRestorer(composition.devpod, registry.NewCatalog(http.DefaultClient, 100)),
		Hydrator:  hydration.NewController(nil, composition.hauler, archive.NewTarZstd(), composition.ownership, hydration.Hooks{}),
		DevPod:    composition.devpod, Providers: composition.devpod, RemoteDataPlane: remoteDataPlane,
		Target: target.Resolver{Zoxide: target.NewCommandZoxide("zoxide", composition.runner)}, Clock: composition.clock,
	}, composition.backend, objectstore.Options{})
}

func (p *ProductionLifecycle) Sync(ctx context.Context, mode OutputMode, out io.Writer) error {
	operation, err := p.prepareProductionSync(ctx)
	if err != nil {
		return err
	}
	var result app.CheckpointResult
	if p.richLifecycleAvailable(mode, os.Stdin, out) {
		workflow := setupui.LifecycleWorkflow{
			Operation: "sync", ReadyLine: "checkpoint published", NextCommand: "camp status",
			Stages: []presentation.LifecycleStage{
				presentation.StageMirror, presentation.StageImageCapture, presentation.StageArchive,
				presentation.StageUpload, presentation.StagePointer,
			},
		}
		recovery, runErr := p.runRichLifecycle(ctx, out, workflow, func(workerCtx context.Context, reporter app.ProgressReporter) richLifecycleWorkerResult {
			var operationErr error
			result, operationErr = operation.run(workerCtx, reporter)
			return richSyncOutcome(result, operationErr, operation.sessionID)
		})
		if runErr != nil {
			if richLifecycleFailureWasRendered(runErr) {
				return renderedLifecycleFailure(runErr)
			}
			return lifecycleFailure(runErr, firstNonEmpty(recovery, syncFailureRecovery(result, operation.sessionID)))
		}
		return nil
	}
	result, err = operation.run(ctx, productionLifecycleProgressReporter(mode, out, "sync"))
	if err != nil {
		return lifecycleFailure(err, syncFailureRecovery(result, operation.sessionID))
	}
	if mode == ModeHuman {
		return writeHumanLifecycleResult(out, mode, "sync", syncTerminalEvents(result.Generation.Generation), "")
	}
	return writeSuccess(out, mode, "sync", result, fmt.Sprintf("Published checkpoint %d\n", result.Generation.Generation))
}

func syncFailureRecovery(result app.CheckpointResult, sessionID string) string {
	if result.RecoveryCommand != "" {
		return result.RecoveryCommand
	}
	return "camp sync --session " + shellQuoteArgument(sessionID)
}

func (p *ProductionLifecycle) Close(ctx context.Context, request CloseRequest, mode OutputMode, out io.Writer) error {
	operation, err := p.prepareProductionClose(ctx)
	if err != nil {
		return err
	}
	var result app.CloseResult
	if p.richLifecycleAvailable(mode, os.Stdin, out) {
		workflow := setupui.LifecycleWorkflow{
			Operation: "close", ReadyLine: "session closed", NextCommand: "camp reopen",
			Stages: closeRichLifecycleStages(operation.mode, request.Discard),
		}
		recovery, runErr := p.runRichLifecycle(ctx, out, workflow, func(workerCtx context.Context, reporter app.ProgressReporter) richLifecycleWorkerResult {
			var operationErr error
			result, operationErr = operation.run(workerCtx, request, reporter)
			return richCloseOutcome(result, operationErr)
		})
		if runErr != nil {
			if richLifecycleFailureWasRendered(runErr) {
				return renderedLifecycleFailure(runErr)
			}
			fallback := "camp close --session " + shellQuoteArgument(operation.sessionID)
			return lifecycleFailure(runErr, firstNonEmpty(recovery, result.RecoveryCommand, fallback))
		}
		return nil
	}
	result, err = operation.run(ctx, request, productionLifecycleProgressReporter(mode, out, "close"))
	if err != nil {
		return lifecycleFailure(err, result.RecoveryCommand)
	}
	if mode == ModeHuman {
		if request.Discard {
			return writeHumanLifecycleResult(out, mode, "close", closeDiscardTerminalEvents(), "")
		}
		return writeHumanLifecycleResult(out, mode, "close", closeTerminalEvents(result.Generation.Generation, result.CleanupSucceeded), "")
	}
	return writeSuccess(out, mode, "close", result, fmt.Sprintf("Closed %s\n", operation.sessionID))
}

func (p *ProductionLifecycle) Reopen(ctx context.Context, value string, mode OutputMode, out io.Writer) error {
	base, err := composeProductionBase(ctx)
	if err != nil {
		return err
	}
	selector := productionSessionSelector(ctx, base.runtime)
	if value != "" {
		selector.SessionID = value
	}
	return dispatchProductionReopen(ctx, base.journal, selector, mode, out, p.Open)
}

type reopenSessionLister interface {
	List(context.Context) ([]domain.JournalSnapshot, error)
}

type productionOpenFunc func(context.Context, string, OutputMode, io.Writer) error

func dispatchProductionReopen(ctx context.Context, sessions reopenSessionLister, selector app.SessionSelector, mode OutputMode, out io.Writer, open productionOpenFunc) error {
	closed, manifestFallback, err := app.SelectReopenSession(ctx, sessions, selector)
	if err != nil {
		return err
	}
	if manifestFallback {
		return open(ctx, "", mode, out)
	}
	ctx = withSelection(ctx, Selection{Camp: closed.Capsule})
	return open(ctx, "", mode, out)
}

func (p *ProductionLifecycle) Recover(ctx context.Context, value string, mode OutputMode, out io.Writer) error {
	c, err := composeLifecycle(ctx)
	if err != nil {
		return err
	}
	selector := productionSessionSelector(ctx, c.base.runtime)
	if value != "" {
		selector.SessionID = value
	}
	result, err := c.recover.Run(ctx, selector)
	if err != nil {
		return err
	}
	return writeSuccess(out, mode, "recover", result, fmt.Sprintf("Recovered %s\n", result.Session.ID))
}

func productionSessionSelector(ctx context.Context, runtime config.Runtime) app.SessionSelector {
	selection := SelectionFromContext(ctx)
	return app.SessionSelector{
		SessionID: selection.Session,
		Capsule:   firstNonEmpty(selection.Camp, runtime.Capsule),
		Branch:    "main",
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
	haulerVersion    string
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

func composeProductionOperationalDependencies(ctx context.Context) (productionComposition, serviceBundle, *journalstore.OperationLocker, *app.RecoverySafetyGuard, error) {
	composition, err := composeProduction(ctx)
	if err != nil {
		return productionComposition{}, serviceBundle{}, nil, nil, err
	}
	services, err := composeServiceBundle(composition)
	if err != nil {
		return productionComposition{}, serviceBundle{}, nil, nil, err
	}
	store, err := objectstore.NewWriter(ctx, composition.backend, objectstore.Options{})
	if err != nil {
		return productionComposition{}, serviceBundle{}, nil, nil, err
	}
	locks, err := journalstore.NewOperationLocker(composition.paths.DataRoot, host.NewIdentity())
	if err != nil {
		return productionComposition{}, serviceBundle{}, nil, nil, err
	}
	guard := app.NewRecoverySafetyGuard(composition.ownership, coordination.NewLeaseRepository(store), composition.clock)
	return composition, services, locks, guard, nil
}

func composeProductionImageOperations(ctx context.Context) (*app.ImageOperations, error) {
	composition, services, locks, guard, err := composeProductionOperationalDependencies(ctx)
	if err != nil {
		return nil, err
	}
	catalog := registry.NewCatalog(http.DefaultClient, 100)
	return app.NewImageOperations(
		composition.journal, locks, guard, services.units,
		images.NewCapturer(composition.devpod, catalog, composition.clock),
		images.NewRestorer(composition.devpod, catalog),
		composition.clock,
	), nil
}

func composeProductionServe(ctx context.Context) (*app.Serve, error) {
	composition, services, locks, guard, err := composeProductionOperationalDependencies(ctx)
	if err != nil {
		return nil, err
	}
	logs, err := supervisor.NewServiceLogReader(composition.paths.RuntimeRoot, 1024*1024)
	if err != nil {
		return nil, err
	}
	return app.NewServe(composition.journal, locks, guard, services.units, logs), nil
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
	ctx = withSelection(ctx, Selection{Session: sessionID})
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
	settings, err := resolveProductionSettingsForContext(ctx)
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

type campPathContextKey struct{}
type requireManifestContextKey struct{}

func withCampPath(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, campPathContextKey{}, path)
}

func requiringManifest(ctx context.Context) context.Context {
	return context.WithValue(ctx, requireManifestContextKey{}, true)
}

func resolveProductionSettingsForContext(ctx context.Context) (productionSettings, error) {
	settings, err := resolveProductionSettings()
	if err != nil {
		return productionSettings{}, err
	}
	selection := SelectionFromContext(ctx)
	if selection.Session != "" || selection.Camp != "" {
		store, err := journalstore.NewStore(settings.paths.DataRoot)
		if err != nil {
			return productionSettings{}, err
		}
		sessions, err := store.List(ctx)
		if err != nil {
			return productionSettings{}, err
		}
		var selected *domain.JournalSnapshot
		for index := range sessions {
			candidate := &sessions[index]
			if selection.Session != "" && candidate.SessionID != selection.Session {
				continue
			}
			if selection.Session == "" && candidate.Capsule != selection.Camp {
				continue
			}
			if selected == nil || candidate.UpdatedAt.After(selected.UpdatedAt) {
				selected = candidate
			}
		}
		if selected == nil {
			return productionSettings{}, fmt.Errorf("no matching Camp session; next: camp list")
		}
		required, _ := ctx.Value(requireManifestContextKey{}).(bool)
		if required && selection.Session == "" {
			resolved, proofErr := proveSelectedCampSource(selection.Camp, *selected)
			if proofErr != nil {
				return productionSettings{}, proofErr
			}
			return applyManifestSettings(settings, resolved)
		}
		return applySnapshotSettings(settings, *selected)
	}
	if settings.runtime.Source != "" {
		migrated, migrateErr := campconfig.Migrate(settings.paths.ConfigPath)
		if migrateErr != nil {
			return productionSettings{}, lifecycleFailure(migrateErr, campconfig.MigrationCommand)
		}
		if !migrated.Migrated {
			return productionSettings{}, lifecycleFailure(errors.New("legacy singleton configuration requires migration"), campconfig.MigrationCommand)
		}
		return applyManifestSettings(settings, migrated.Manifest)
	}
	path, _ := ctx.Value(campPathContextKey{}).(string)
	if path == "" {
		path, err = os.Getwd()
		if err != nil {
			return productionSettings{}, err
		}
	}
	resolved, err := campconfig.Discover(path)
	if err != nil {
		required, _ := ctx.Value(requireManifestContextKey{}).(bool)
		if required {
			return productionSettings{}, err
		}
		store, storeErr := journalstore.NewStore(settings.paths.DataRoot)
		if storeErr != nil {
			return productionSettings{}, storeErr
		}
		active, selectErr := app.SelectActiveSession(ctx, store, app.SessionSelector{})
		if selectErr != nil {
			return productionSettings{}, err
		}
		return applySnapshotSettings(settings, active)
	}
	return applyManifestSettings(settings, resolved)
}

func proveSelectedCampSource(camp string, snapshot domain.JournalSnapshot) (campconfig.Resolved, error) {
	source := snapshot.Recovery.Configuration.Source
	if source == "" {
		return campconfig.Resolved{}, fmt.Errorf("camp %q has no proven local source; next: camp status --camp %s", camp, shellQuoteArgument(camp))
	}
	resolved, err := campconfig.Discover(source)
	if err != nil {
		return campconfig.Resolved{}, fmt.Errorf("camp %q source %q has no current manifest: %w; next: camp init %s --name %s", camp, source, err, shellQuoteArgument(source), shellQuoteArgument(camp))
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil || filepath.Clean(canonical) != filepath.Clean(resolved.Root) {
		return campconfig.Resolved{}, fmt.Errorf("camp %q current manifest does not own durable source %q", camp, source)
	}
	if resolved.Manifest.ID != camp || snapshot.Capsule != camp {
		return campconfig.Resolved{}, fmt.Errorf("camp %q current manifest identity does not match durable session", camp)
	}
	record := snapshot.Recovery.Configuration
	if record.BackendURL != "" && resolved.Manifest.Backend != record.BackendURL ||
		snapshot.Workspace.Provider != "" && resolved.Manifest.Workspace.Provider != snapshot.Workspace.Provider ||
		snapshot.Workspace.Context != "" && resolved.Manifest.Workspace.Context != snapshot.Workspace.Context {
		return campconfig.Resolved{}, fmt.Errorf("camp %q current manifest does not match durable session settings", camp)
	}
	return resolved, nil
}

func applyManifestSettings(settings productionSettings, resolved campconfig.Resolved) (productionSettings, error) {
	settings.runtime.Capsule = resolved.Manifest.ID
	settings.runtime.Source = resolved.Root
	settings.runtime.Backend = resolved.Manifest.Backend
	settings.runtime.DevPodProvider = resolved.Manifest.Workspace.Provider
	settings.runtime.DevPodContext = resolved.Manifest.Workspace.Context
	backend, err := config.ResolveBackend(resolved.Manifest.Backend, settings.runtime.S3)
	if err != nil {
		return productionSettings{}, err
	}
	settings.backend = backend
	return settings, nil
}

func applySnapshotSettings(settings productionSettings, snapshot domain.JournalSnapshot) (productionSettings, error) {
	record := snapshot.Recovery.Configuration
	settings.runtime.Capsule = snapshot.Capsule
	if record.Source != "" {
		settings.runtime.Source = record.Source
	}
	if record.BackendURL != "" {
		settings.runtime.Backend = record.BackendURL
	}
	if snapshot.Workspace.Provider != "" {
		settings.runtime.DevPodProvider = snapshot.Workspace.Provider
	}
	if snapshot.Workspace.Context != "" {
		settings.runtime.DevPodContext = snapshot.Workspace.Context
	}
	backend, err := config.ResolveBackend(settings.runtime.Backend, settings.runtime.S3)
	if err != nil {
		return productionSettings{}, err
	}
	settings.backend = backend
	return settings, nil
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
	settings, err := resolveProductionSettingsForContext(ctx)
	if err != nil {
		return productionComposition{}, err
	}
	return composeProductionWithSettings(ctx, settings)
}

func composeMachineProduction(ctx context.Context) (productionComposition, error) {
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
	haulerVersion := toolPaths.haulerVersion
	return productionComposition{
		productionBase: base,
		initializer:    capsule.NewInitializer(base.clock, capsule.NewCommandDigestResolver("docker", runner)),
		runner:         runner, devpod: devpod.NewClient(devpodPath, runner), devpodExecutable: devpodPath,
		haulerExecutable: haulerPath, haulerVersion: haulerVersion,
		hauler: hauler.NewClientWithVersion(haulerPath, haulerVersion, runner),
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

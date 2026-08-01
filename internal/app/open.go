package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/adapters/objectstore"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	imageops "github.com/joshyorko/camp/internal/images"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
	"github.com/joshyorko/camp/internal/workspace"
)

var (
	ErrOpenDependencies              = errors.New("open dependencies are incomplete")
	ErrRecoveryRequired              = errors.New("session requires recovery before entry")
	ErrOpenRecoveryObjective         = errors.New("open recovery objective is missing or unsupported")
	ErrOpenReadOnlyLease             = errors.New("read-only session cannot reconcile a writer lease")
	ErrOpenSessionMismatch           = errors.New("open request does not match the selected session")
	ErrOpenIDEUnsupported            = errors.New("IDE entry is not implemented")
	workspaceOpaqueCredentialPattern = regexp.MustCompile(`(?i)\b(?:[A-Za-z0-9._~+/=-]{20,}|[A-Za-z0-9]{32,})\b`)
)

const (
	workspaceReadyTimeout = 30 * time.Second
	workspaceReadyPoll    = 100 * time.Millisecond
	workspaceUpDiagnostic = 8 << 10
)

type OpenPointerReader interface {
	Read(context.Context, string, domain.Lineage) (coordination.PointerRecord, error)
	Revalidate(context.Context, coordination.PointerRecord) error
}

type OpenGenerationReader interface {
	ReadMetadata(context.Context, string, domain.Lineage, domain.GenerationRef) (domain.GenerationMetadata, ports.ObjectMeta, error)
}

type OpenLeaseManager interface {
	Read(context.Context, string, domain.Lineage) (coordination.LeaseToken, error)
	Acquire(context.Context, string, domain.Lineage, coordination.LeaseOwner, *coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error)
	AcquireBranchFrom(context.Context, string, domain.Lineage, coordination.LeaseOwner, coordination.PointerRecord, time.Time, time.Duration) (coordination.LeaseToken, error)
}

type OpenHydrator interface {
	Hydrate(context.Context, hydration.Request) (hydration.Result, error)
}

type OpenHydratorWithHooks interface {
	WithHooks(hydration.Hooks) *hydration.Controller
}

type OpenInitializer interface {
	Initialize(context.Context, string, string) (capsule.Initialization, error)
}

type OpenDevPod interface {
	Up(context.Context, devpodadapter.UpOptions) (ports.Result, error)
	ListInContext(context.Context, string) ([]devpodadapter.Workspace, error)
	StatusInContext(context.Context, string, string) (devpodadapter.WorkspaceStatus, error)
	ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error)
	SSH(context.Context, devpodadapter.SSHOptions) (ports.Result, error)
	SSHWithStart(context.Context, devpodadapter.SSHOptions, func() error) (ports.Result, error)
}

type OpenProviderEnsurer interface {
	EnsureProvider(context.Context, string, string) error
}

type OpenTargetResolver interface {
	Resolve(context.Context, string, string) (target.Result, error)
}

type OpenServiceStarter interface {
	Start(context.Context, domain.JournalSnapshot) (domain.JournalSnapshot, error)
}

type OpenRemoteDataPlane interface {
	Prepare(context.Context, RemoteDataPlaneRequest) (RemoteDataPlaneResult, error)
}

type RemoteDataPlaneRequest struct {
	SessionID        string
	AttemptID        string
	Capsule          string
	Lineage          domain.Lineage
	Generation       *domain.GenerationRef
	Materialization  string
	DevcontainerPath string
}

type RemoteDataPlaneResult struct {
	BootstrapRoot string
	Record        domain.RemoteDataPlaneRecord
}

type OpenForwarderManager interface {
	Start(context.Context, domain.ForwardingRequest) (domain.ForwardingRecord, error)
	Observe(context.Context, domain.ForwardingRequest) (domain.ForwardingRecord, error)
	Stop(context.Context, domain.ForwardingRecord) error
}

type OpenHardlinkRestorer interface {
	Restore(context.Context, workspace.HardlinkRestoreRequest) error
}

type OpenImageRestorer interface {
	Restore(context.Context, imageops.RestoreRequest) (imageops.RestoreResult, error)
}

type OpenDependencies struct {
	Journal         ports.Journal
	Paths           config.XDGPaths
	Backend         config.FileBackend
	ResolvedBackend config.Backend
	Ownership       *capsule.Ownership
	Initializer     OpenInitializer
	Pointers        OpenPointerReader
	Generations     OpenGenerationReader
	Leases          OpenLeaseManager
	Hydrator        OpenHydrator
	DevPod          OpenDevPod
	Providers       OpenProviderEnsurer
	Target          OpenTargetResolver
	Services        OpenServiceStarter
	RemoteDataPlane OpenRemoteDataPlane
	Forwarders      OpenForwarderManager
	Hardlinks       OpenHardlinkRestorer
	Images          OpenImageRestorer
	Clock           ports.Clock
}

type OpenRequest struct {
	SessionID       string
	Capsule         string
	Branch          string
	Mode            domain.SessionMode
	ExplicitRoot    string
	ConfiguredRoot  string
	RemoteAvailable bool
	SourceLineage   domain.Lineage
	Target          string
	EntryMode       domain.EntryMode
	Context         string
	Provider        string
	LocalProvider   bool
	Machine         string
	LeaseTTL        time.Duration
	Runtime         config.Runtime
	Backend         config.FileBackend
	ResolvedBackend config.Backend
}

type OpenResult struct {
	Snapshot        domain.JournalSnapshot
	Target          target.Result
	MappedTarget    string
	DevPodResult    ports.Result
	WorkspaceID     string
	RecoveryCommand string
}

type openLeaseAcquisitionInput struct {
	Capsule          string                      `json:"capsule"`
	Lineage          domain.Lineage              `json:"lineage"`
	Owner            coordination.LeaseOwner     `json:"owner"`
	Observed         *coordination.PointerRecord `json:"observed,omitempty"`
	Source           *coordination.PointerRecord `json:"source,omitempty"`
	ObservedRevision string                      `json:"observedRevision,omitempty"`
	BranchSource     bool                        `json:"branchSource"`
	Now              time.Time                   `json:"now"`
	LeaseTTL         time.Duration               `json:"leaseTtl"`
}

type openLeaseReceipt struct {
	Capsule          string                `json:"capsule"`
	Lineage          domain.Lineage        `json:"lineage"`
	Session          string                `json:"sessionId"`
	Machine          string                `json:"machine"`
	Revision         string                `json:"revision"`
	OpenedGeneration *domain.GenerationRef `json:"openedGeneration,omitempty"`
	CreatedAt        time.Time             `json:"createdAt"`
	HeartbeatAt      time.Time             `json:"heartbeatAt"`
	ExpiresAt        time.Time             `json:"expiresAt"`
	BranchSource     bool                  `json:"branchSource"`
	ObservedRevision string                `json:"observedRevision"`
}

type openWorkspaceUpInput struct {
	ID         string                        `json:"id"`
	Context    string                        `json:"context"`
	Provider   string                        `json:"provider"`
	SourceRoot string                        `json:"sourceRoot"`
	Env        devpodadapter.CampEnvironment `json:"environment"`
}

type openWorkspaceRootInput struct {
	ID string `json:"id"`
}

type openSessionOpenedInput struct {
	ID string `json:"id"`
}

type openMaterializationPlanInput struct {
	Token string `json:"token"`
	Stage string `json:"stage"`
	Final string `json:"final"`
}

type openTerminalEntryInput struct {
	ID      string `json:"id"`
	Workdir string `json:"workdir"`
}

type Open struct {
	deps OpenDependencies
}

func NewOpen(deps OpenDependencies) *Open {
	return &Open{deps: deps}
}

func NewOpenWithBackend(ctx context.Context, deps OpenDependencies, backend config.Backend, options objectstore.Options) (*Open, error) {
	store, err := objectstore.NewWriter(ctx, backend, options)
	if err != nil {
		return nil, fmt.Errorf("compose open object store: %w", err)
	}
	deps.ResolvedBackend = backend
	deps.Pointers = coordination.NewPointerRepository(store)
	deps.Generations = coordination.NewGenerationRepository(store)
	deps.Leases = coordination.NewLeaseRepository(store)
	if deps.Hydrator != nil {
		hydrator, ok := deps.Hydrator.(interface {
			WithStore(hydration.GenerationStore) *hydration.Controller
		})
		if !ok {
			return nil, errors.New("compose open hydrator: hydrator cannot bind the selected object store")
		}
		deps.Hydrator = hydrator.WithStore(store)
	}
	return NewOpen(deps), nil
}

func (o *Open) Run(ctx context.Context, request OpenRequest) (OpenResult, error) {
	request = normalizeOpenRequest(request)
	if err := o.validate(request); err != nil {
		return OpenResult{}, err
	}
	if request.ExplicitRoot != "" {
		source, err := capsule.ResolveSource(capsule.SourceRequest{Capsule: request.Capsule, ExplicitPath: request.ExplicitRoot})
		if err != nil {
			return OpenResult{}, err
		}
		request.ExplicitRoot = source.Root
	}
	if request.SessionID == "" {
		selected, err := SelectActiveSession(ctx, o.deps.Journal, SessionSelector{Capsule: request.Capsule, Branch: request.Branch, CanonicalRoot: request.ExplicitRoot})
		if err == nil {
			loaded, pending, loadErr := o.deps.Journal.Load(ctx, selected.SessionID)
			if loadErr != nil {
				return OpenResult{}, loadErr
			}
			return o.reenterWithPending(ctx, loaded, pending, request)
		}
		if !errors.Is(err, ErrNoActiveSession) {
			return OpenResult{}, err
		}
	} else if snapshot, pending, err := o.deps.Journal.Load(ctx, request.SessionID); err == nil && activeSessionState(snapshot.State) {
		return o.reenterWithPending(ctx, snapshot, pending, request)
	}
	return o.create(ctx, request)
}

func (o *Open) reenterWithPending(ctx context.Context, snapshot domain.JournalSnapshot, pending []ports.PendingIntent, request OpenRequest) (OpenResult, error) {
	if snapshot.State == domain.SessionOpen && len(pending) != 0 {
		terminalPending := containsPendingTransition(pending, "TerminalEntryDispatched")
		reconciled, err := o.Reconcile(ctx, snapshot.SessionID)
		if err != nil {
			return OpenResult{}, err
		}
		if terminalPending {
			return OpenResult{Snapshot: reconciled, WorkspaceID: reconciled.Workspace.ID, RecoveryCommand: "camp recover " + reconciled.SessionID}, errors.New("workspace is open; previous terminal entry outcome is unknown; run camp attach to enter it")
		}
		snapshot = reconciled
	}
	return o.reenter(ctx, snapshot, request)
}

func (o *Open) Reconcile(ctx context.Context, sessionID string) (domain.JournalSnapshot, error) {
	if o == nil || o.deps.Journal == nil || o.deps.Clock == nil || sessionID == "" {
		return domain.JournalSnapshot{}, errors.New("open reconciliation dependencies or session are incomplete")
	}
	snapshot, _, err := o.deps.Journal.Load(ctx, sessionID)
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	if err := validateSnapshotBackend(snapshot, o.effectiveBackend(OpenRequest{})); err != nil {
		return snapshot, err
	}
	if err := validateOpenRecoveryObjective(snapshot); err != nil {
		return snapshot, err
	}
	reconciled, err := journalstore.Reconcile(ctx, o.deps.Journal, sessionID, map[string]journalstore.Observer{
		"LocalLeaseAcquisition":       withOpenRecoveryObjective(o.observeRemoteLeaseAcquisition),
		"RemoteLeaseAcquisition":      withOpenRecoveryObjective(o.observeRemoteLeaseAcquisition),
		"RemoteDataPlanePrepared":     withOpenRecoveryObjective(o.observeRemoteDataPlanePrepared),
		"WorkspaceUp":                 withOpenRecoveryObjective(o.observeWorkspaceUp),
		"WorkspaceRootResolved":       withOpenRecoveryObjective(o.observeWorkspaceRootResolved),
		"ForwarderStarted:registry":   withOpenRecoveryObjective(o.observeForwarderStarted),
		"ForwarderStarted:fileserver": withOpenRecoveryObjective(o.observeForwarderStarted),
		"WorkspaceHardlinksRestored":  withOpenRecoveryObjective(o.observeWorkspaceHardlinksRestored),
		"WorkspaceImagesRestored":     withOpenRecoveryObjective(o.observeWorkspaceImagesRestored),
		"SessionOpened":               withOpenRecoveryObjective(o.observeSessionOpened),
		"TerminalEntryDispatched":     withOpenRecoveryObjective(o.observeTerminalEntryDispatched),
	})
	if err != nil {
		return reconciled, err
	}
	if err := validateOpenRecoveryObjective(reconciled); err != nil {
		return reconciled, err
	}
	return reconciled, nil
}

func (o *Open) observeRemoteDataPlanePrepared(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.RemoteDataPlane == nil || snapshot.Recovery.RemoteDataPlane == nil {
		return ports.FactRecord{}, snapshot, errors.New("remote data-plane recovery dependency or selection is incomplete")
	}
	var selected domain.RemoteDataPlaneRecord
	if err := json.Unmarshal(intent.Input, &selected); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode remote data-plane intent: %w", err)
	}
	if selected != *snapshot.Recovery.RemoteDataPlane || selected.Mode != domain.DataPlaneHaulerKitV1 ||
		selected.AttemptID != snapshot.SessionID+"-hauler-kit-v1" || selected.BootstrapRoot != "" {
		return ports.FactRecord{}, snapshot, errors.New("remote data-plane intent does not match the pending session")
	}
	devcontainerPath, err := o.resolveRecoveryDevcontainer(ctx, snapshot)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	prepared, err := o.deps.RemoteDataPlane.Prepare(ctx, RemoteDataPlaneRequest{
		SessionID: snapshot.SessionID, AttemptID: selected.AttemptID, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage,
		Generation: cloneGeneration(snapshot.OpenedGeneration), Materialization: snapshot.Materialization.CanonicalPath,
		DevcontainerPath: devcontainerPath,
	})
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if !validRemoteDataPlaneResult(prepared, selected, snapshot.Materialization.CanonicalPath) {
		return ports.FactRecord{}, snapshot, errors.New("observed remote data plane does not match the pending intent")
	}
	next := snapshot
	next.Recovery.RemoteDataPlane = &prepared.Record
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("remote data-plane recovery clock returned zero time")
	}
	return ports.FactRecord{
		IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now, Output: safeJSON(prepared.Record),
	}, next, nil
}

func (o *Open) resolveRecoveryDevcontainer(ctx context.Context, snapshot domain.JournalSnapshot) (string, error) {
	root := snapshot.Materialization.CanonicalPath
	configured := snapshot.Recovery.Configuration.DevcontainerPath
	generated := filepath.Join(root, ".camp", "runtime", "devcontainer.json")
	explicit := configured
	if filepath.Clean(configured) == generated {
		if _, err := os.Lstat(configured); errors.Is(err, os.ErrNotExist) {
			explicit = ""
		} else if err != nil {
			return "", err
		}
	}
	initialization, err := o.deps.Initializer.Initialize(ctx, root, snapshot.Capsule)
	if err != nil {
		return "", err
	}
	resolved, err := capsule.ResolveDevcontainer(root, explicit, initialization.Lock)
	if err != nil {
		return "", err
	}
	if resolved.Path != configured {
		return "", errors.New("recovered devcontainer path does not match the pending session")
	}
	return resolved.Path, nil
}

func (o *Open) observeForwarderStarted(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.Forwarders == nil {
		return ports.FactRecord{}, snapshot, errors.New("workspace forwarder recovery dependency is incomplete")
	}
	var request domain.ForwardingRequest
	if err := json.Unmarshal(intent.Input, &request); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode workspace forwarder intent: %w", err)
	}
	registryPort, fileserverPort, err := committedServicePorts(snapshot)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	expectedLocalPort := 0
	expectedWorkspacePort := 0
	switch request.Name {
	case "registry":
		expectedLocalPort = registryPort
		expectedWorkspacePort = 5000
	case "fileserver":
		expectedLocalPort = fileserverPort
		expectedWorkspacePort = 8080
	default:
		return ports.FactRecord{}, snapshot, errors.New("workspace forwarder intent names an unsupported service")
	}
	expectedLocalEndpoint := endpoint(expectedLocalPort)
	expectedWorkspaceEndpoint := endpoint(expectedWorkspacePort)
	expectedLogPath := filepath.Join(snapshot.Recovery.Session.RuntimeRoot, request.Name+"-forward.log")
	expectedEvidencePath := filepath.Join(snapshot.Recovery.Session.RuntimeRoot, request.Name+"-forward.json")
	if intent.SessionID != snapshot.SessionID || intent.Transition != "ForwarderStarted:"+request.Name ||
		request.WorkspaceID == "" || request.WorkspaceID != snapshot.Workspace.ID || request.Context == "" || request.Context != snapshot.Workspace.Context ||
		request.LocalEndpoint != expectedLocalEndpoint || request.WorkspaceEndpoint != expectedWorkspaceEndpoint ||
		request.LogPath != expectedLogPath || request.EvidencePath != expectedEvidencePath {
		return ports.FactRecord{}, snapshot, errors.New("workspace forwarder intent does not match the pending session")
	}
	for _, existing := range snapshot.Recovery.Forwarding {
		if existing.Name == request.Name {
			return ports.FactRecord{}, snapshot, errors.New("pending workspace forwarder already has a committed record")
		}
	}
	record, err := o.deps.Forwarders.Observe(ctx, request)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if record.Name != request.Name || record.LocalEndpoint != request.LocalEndpoint || record.WorkspaceEndpoint != request.WorkspaceEndpoint ||
		record.EvidencePath != request.EvidencePath || record.EvidenceDevice == 0 || record.EvidenceInode == 0 ||
		record.Process.Identity.PID <= 0 || record.Process.Identity.BootID == "" || record.Process.Identity.StartTicks == 0 ||
		record.DesiredState != domain.RuntimeDesiredRunning || record.ObservedState != domain.RuntimeObservedReady {
		return ports.FactRecord{}, snapshot, errors.New("observed workspace forwarder does not match the pending intent")
	}
	next := snapshot
	next.Recovery.Forwarding = append(next.Recovery.Forwarding, record)
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("workspace forwarder recovery clock returned zero time")
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now, Output: safeJSON(record)}, next, nil
}

func withOpenRecoveryObjective(observer journalstore.Observer) journalstore.Observer {
	return func(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
		if err := validateOpenRecoveryObjective(snapshot); err != nil {
			return ports.FactRecord{}, snapshot, err
		}
		return observer(ctx, snapshot, intent)
	}
}

func (o *Open) observeWorkspaceUp(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.DevPod == nil || o.deps.Ownership == nil {
		return ports.FactRecord{}, snapshot, errors.New("workspace reconciliation dependencies are incomplete")
	}
	if snapshot.State != domain.SessionOpening && snapshot.State != domain.SessionRecovering {
		return ports.FactRecord{}, snapshot, fmt.Errorf("session %q is not awaiting workspace creation", snapshot.SessionID)
	}
	var input openWorkspaceUpInput
	if err := json.Unmarshal(intent.Input, &input); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode workspace up intent: %w", err)
	}
	root := snapshot.Materialization.CanonicalPath
	expectedID := workspace.DeterministicID(snapshot.Capsule, snapshot.Lineage.Branch, root)
	checkpoint := ""
	if snapshot.OpenedGeneration != nil {
		checkpoint = strconv.FormatUint(snapshot.OpenedGeneration.Generation, 10)
	}
	registryPort, fileserverPort, err := committedServicePorts(snapshot)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	expectedEnvironment := devpodadapter.CampEnvironment{
		Registry: endpoint(registryPort), Fileserver: endpoint(fileserverPort), Capsule: snapshot.Capsule, Checkpoint: checkpoint,
	}
	if intent.SessionID != snapshot.SessionID || input.ID == "" || input.ID != expectedID || input.Context == "" || input.Context != snapshot.Workspace.Context ||
		input.Provider == "" || input.Provider != snapshot.Workspace.Provider || input.Env != expectedEnvironment || root == "" {
		return ports.FactRecord{}, snapshot, errors.New("workspace up intent does not match the pending session")
	}
	expectedSourceRoot := root
	if record := snapshot.Recovery.RemoteDataPlane; record != nil && record.Mode == domain.DataPlaneHaulerKitV1 {
		expectedSourceRoot = record.BootstrapRoot
	}
	if input.SourceRoot != expectedSourceRoot || !validRoot(input.SourceRoot) {
		return ports.FactRecord{}, snapshot, errors.New("workspace up source does not match the pending session")
	}
	if err := o.deps.Ownership.Revalidate(snapshot.Materialization); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("revalidate workspace materialization: %w", err)
	}
	if err := o.revalidateWorkspaceSource(ctx, input); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	status, err := o.waitForWorkspaceReady(ctx, input.Context, input.ID, input.Provider)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if !matchingWorkspaceStatus(status, input.Context, input.ID, input.Provider) || status.State != devpodadapter.StateRunning {
		return ports.FactRecord{}, snapshot, fmt.Errorf("observed DevPod workspace is not ready in state %q: %w", status.State, devpodadapter.ErrUnknownWorkspaceState)
	}
	if err := o.revalidateWorkspaceSource(ctx, input); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if err := o.observeRemoteStartup(ctx, snapshot, input); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	next := snapshot
	next.Workspace.ID = input.ID
	next.Workspace.LocalFolder = input.SourceRoot
	next.Workspace.StagingRoot = root
	next.Workspace.Target = snapshot.Recovery.Entry.Target
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now, Output: safeJSON(status)}, next, nil
}

func (o *Open) revalidateWorkspaceSource(ctx context.Context, input openWorkspaceUpInput) error {
	workspaces, err := o.deps.DevPod.ListInContext(ctx, input.Context)
	if err != nil {
		return err
	}
	matches := 0
	for _, candidate := range workspaces {
		if candidate.ID != input.ID {
			continue
		}
		matches++
		if candidate.Context != input.Context || candidate.Provider.Name != input.Provider || candidate.Source.LocalFolder != input.SourceRoot {
			return errors.New("observed DevPod workspace source identity does not match the pending intent")
		}
	}
	if matches != 1 {
		return errors.New("pending DevPod workspace source identity is absent or ambiguous")
	}
	return nil
}

func (o *Open) waitForWorkspaceReady(ctx context.Context, devpodContext, workspaceID, provider string) (devpodadapter.WorkspaceStatus, error) {
	waitCtx, cancel := context.WithTimeout(ctx, workspaceReadyTimeout)
	defer cancel()
	status, err := o.deps.DevPod.StatusInContext(waitCtx, devpodContext, workspaceID)
	if err != nil || !matchingWorkspaceStatus(status, devpodContext, workspaceID, provider) || status.State != devpodadapter.StateBusy {
		return status, err
	}
	ticker := o.deps.Clock.NewTicker(workspaceReadyPoll)
	defer ticker.Stop()
	for status.State == devpodadapter.StateBusy {
		select {
		case <-waitCtx.Done():
			return status, fmt.Errorf("wait for DevPod workspace %q to leave state %q: %w", workspaceID, status.State, waitCtx.Err())
		case <-ticker.C():
			status, err = o.deps.DevPod.StatusInContext(waitCtx, devpodContext, workspaceID)
			if err != nil {
				return devpodadapter.WorkspaceStatus{}, err
			}
			if !matchingWorkspaceStatus(status, devpodContext, workspaceID, provider) {
				return status, nil
			}
		}
	}
	return status, nil
}

func matchingWorkspaceStatus(status devpodadapter.WorkspaceStatus, devpodContext, workspaceID, provider string) bool {
	return status.ID == workspaceID &&
		(status.Context == "" || status.Context == devpodContext) &&
		(status.Provider == "" || status.Provider == provider)
}

func committedServicePorts(snapshot domain.JournalSnapshot) (int, int, error) {
	if len(snapshot.Services) == 0 {
		return snapshot.Recovery.Configuration.RegistryPort, snapshot.Recovery.Configuration.FileserverPort, nil
	}
	portsByName := map[string]int{}
	for _, service := range snapshot.Services {
		guestPort := 0
		switch service.Name {
		case "registry":
			guestPort = 5000
		case "fileserver":
			guestPort = 8080
		default:
			continue
		}
		if _, exists := portsByName[service.Name]; exists || service.Mapping.HostAddress != "127.0.0.1" || service.Mapping.HostPort < 1 || service.Mapping.HostPort > 65535 || service.Mapping.HostPort == guestPort || service.Mapping.GuestPort != guestPort {
			return 0, 0, errors.New("committed service mapping is invalid")
		}
		portsByName[service.Name] = service.Mapping.HostPort
	}
	registryPort, registryOK := portsByName["registry"]
	fileserverPort, fileserverOK := portsByName["fileserver"]
	if !registryOK || !fileserverOK {
		return 0, 0, errors.New("committed service mappings are incomplete")
	}
	return registryPort, fileserverPort, nil
}

func (o *Open) observeWorkspaceRootResolved(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.DevPod == nil || o.deps.Ownership == nil {
		return ports.FactRecord{}, snapshot, errors.New("workspace root reconciliation dependencies are incomplete")
	}
	if snapshot.State != domain.SessionOpening && snapshot.State != domain.SessionRecovering {
		return ports.FactRecord{}, snapshot, fmt.Errorf("session %q is not awaiting workspace root resolution", snapshot.SessionID)
	}
	var input openWorkspaceRootInput
	if err := json.Unmarshal(intent.Input, &input); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode workspace root intent: %w", err)
	}
	if intent.SessionID != snapshot.SessionID || input.ID == "" || input.ID != snapshot.Workspace.ID {
		return ports.FactRecord{}, snapshot, errors.New("workspace root intent does not match the pending session")
	}
	if err := o.validateOpenSession(snapshot, false); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	effectiveRoot, err := o.deps.DevPod.ResolveWorkspaceFolderInContext(ctx, snapshot.Workspace.Context, snapshot.Workspace.ID)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if !filepath.IsAbs(effectiveRoot) || filepath.Clean(effectiveRoot) == string(filepath.Separator) {
		return ports.FactRecord{}, snapshot, errors.New("observed DevPod workspace root is unsafe")
	}
	next := snapshot
	next.Workspace.EffectiveRoot = filepath.Clean(effectiveRoot)
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now, Output: safeJSON(next.Workspace.EffectiveRoot)}, next, nil
}

func (o *Open) observeWorkspaceHardlinksRestored(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.Hardlinks == nil || snapshot.Workspace.LocalProvider {
		return ports.FactRecord{}, snapshot, errors.New("remote hardlink recovery dependencies are incomplete")
	}
	var request workspace.HardlinkRestoreRequest
	if err := json.Unmarshal(intent.Input, &request); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode hardlink restore intent: %w", err)
	}
	if request.WorkspaceID != snapshot.Workspace.ID || request.Context != snapshot.Workspace.Context || request.LocalRoot != snapshot.Materialization.CanonicalPath || request.RemoteRoot != snapshot.Workspace.EffectiveRoot {
		return ports.FactRecord{}, snapshot, errors.New("hardlink restore intent does not match the pending workspace")
	}
	if err := o.deps.Hardlinks.Restore(ctx, request); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	next := snapshot
	next.Workspace.HardlinksRestored = true
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now}, next, nil
}

func (o *Open) observeWorkspaceImagesRestored(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.Images == nil {
		return ports.FactRecord{}, snapshot, errors.New("workspace image recovery dependencies are incomplete")
	}
	var request imageops.RestoreRequest
	if err := json.Unmarshal(intent.Input, &request); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode workspace image restore intent: %w", err)
	}
	if request.Scope.WorkspaceID != snapshot.Workspace.ID || request.Scope.Context != snapshot.Workspace.Context {
		return ports.FactRecord{}, snapshot, errors.New("workspace image restore intent does not match the pending workspace")
	}
	registry, err := checkpointRegistryRuntime(snapshot)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if request.RegistryAuthority != registry.authority || request.RegistryEndpoint != registry.endpoint {
		return ports.FactRecord{}, snapshot, errors.New("workspace image restore intent does not match the committed registry")
	}
	inventory, err := loadOpenImageInventory(snapshot.Materialization.CanonicalPath)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	if string(safeJSON(request.Inventory)) != string(safeJSON(inventory)) {
		return ports.FactRecord{}, snapshot, errors.New("workspace image restore intent does not match the hydrated inventory")
	}
	restored, err := o.deps.Images.Restore(ctx, request)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	next := snapshot
	next.Images = inventory
	next.Workspace.ImagesRestored = true
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now, Output: safeJSON(restored)}, next, nil
}

func (o *Open) observeSessionOpened(_ context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.Ownership == nil {
		return ports.FactRecord{}, snapshot, errors.New("session readiness reconciliation dependencies are incomplete")
	}
	if snapshot.State != domain.SessionOpening && snapshot.State != domain.SessionRecovering {
		return ports.FactRecord{}, snapshot, fmt.Errorf("session %q is not awaiting readiness completion", snapshot.SessionID)
	}
	var input openSessionOpenedInput
	if err := json.Unmarshal(intent.Input, &input); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode session readiness intent: %w", err)
	}
	if intent.SessionID != snapshot.SessionID || input.ID == "" || input.ID != snapshot.Workspace.ID || snapshot.Recovery.Entry.Mode != domain.EntryTerminal {
		return ports.FactRecord{}, snapshot, errors.New("session readiness intent does not match the pending session")
	}
	if err := o.validateOpenSession(snapshot, true); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	next := snapshot
	next.State = domain.SessionOpen
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now}, next, nil
}

func (o *Open) observeTerminalEntryDispatched(_ context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.Ownership == nil {
		return ports.FactRecord{}, snapshot, errors.New("terminal entry reconciliation dependencies are incomplete")
	}
	if snapshot.State != domain.SessionOpen {
		return ports.FactRecord{}, snapshot, fmt.Errorf("session %q is not open for terminal entry reconciliation", snapshot.SessionID)
	}
	var input openTerminalEntryInput
	if err := json.Unmarshal(intent.Input, &input); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode terminal entry intent: %w", err)
	}
	if intent.SessionID != snapshot.SessionID || input.ID == "" || input.ID != snapshot.Workspace.ID || snapshot.Recovery.Entry.Mode != domain.EntryTerminal {
		return ports.FactRecord{}, snapshot, errors.New("terminal entry intent does not match the open session")
	}
	if err := o.validateOpenSession(snapshot, true); err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	workdir := filepath.Clean(input.Workdir)
	relative, err := filepath.Rel(snapshot.Workspace.EffectiveRoot, workdir)
	if err != nil || !filepath.IsAbs(input.Workdir) || workdir != input.Workdir || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ports.FactRecord{}, snapshot, errors.New("terminal entry intent workdir escapes the open workspace")
	}
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	return ports.FactRecord{
		IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now,
		Output: safeJSON(struct {
			Outcome string `json:"outcome"`
		}{Outcome: "unknown-after-process-restart"}),
	}, snapshot, nil
}

func (o *Open) observeRemoteLeaseAcquisition(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
	if o.deps.Leases == nil {
		return ports.FactRecord{}, snapshot, errors.New("lease reconciliation dependencies are incomplete")
	}
	if snapshot.Mode != domain.SessionReadWrite {
		return ports.FactRecord{}, snapshot, ErrOpenReadOnlyLease
	}
	var input openLeaseAcquisitionInput
	if err := json.Unmarshal(intent.Input, &input); err != nil {
		return ports.FactRecord{}, snapshot, fmt.Errorf("decode lease acquisition intent: %w", err)
	}
	if input.Capsule != snapshot.Capsule || input.Lineage != snapshot.Lineage || input.Owner.SessionID != snapshot.SessionID || input.Owner.Machine == "" || input.Now.IsZero() || input.LeaseTTL <= 0 {
		return ports.FactRecord{}, snapshot, errors.New("lease acquisition intent does not match the pending session")
	}
	if input.BranchSource {
		if input.Lineage.IsMain() || input.Source == nil || input.Observed == nil || input.ObservedRevision != string(input.Observed.Revision) ||
			input.Source.Pointer.Capsule != input.Capsule || input.Source.Pointer.Lineage == input.Lineage || !sameOpenPointerRecord(*input.Observed, *input.Source) {
			return ports.FactRecord{}, snapshot, errors.New("branch lease acquisition intent has an inconsistent source")
		}
		if _, err := o.deps.Pointers.Read(ctx, input.Capsule, input.Lineage); err == nil || !errors.Is(err, ports.ErrNotFound) {
			if err == nil {
				err = coordination.ErrPointerChanged
			}
			return ports.FactRecord{}, snapshot, fmt.Errorf("revalidate absent branch pointer: %w", err)
		}
		if err := o.deps.Pointers.Revalidate(ctx, *input.Source); err != nil {
			return ports.FactRecord{}, snapshot, err
		}
	} else if input.Observed != nil {
		if o.deps.Pointers == nil {
			return ports.FactRecord{}, snapshot, errors.New("pointer reconciliation dependency is incomplete")
		}
		if input.Source != nil || input.Observed == nil || input.ObservedRevision != string(input.Observed.Revision) || input.Observed.Pointer.Lineage != input.Lineage {
			return ports.FactRecord{}, snapshot, errors.New("lease acquisition intent has an inconsistent observed pointer")
		}
		if err := o.deps.Pointers.Revalidate(ctx, *input.Observed); err != nil {
			return ports.FactRecord{}, snapshot, err
		}
	} else if input.Source != nil || input.ObservedRevision != "" || input.BranchSource || snapshot.Recovery.Source.Kind != domain.SourceDecisionAdopted {
		return ports.FactRecord{}, snapshot, errors.New("local lease acquisition intent is inconsistent")
	}
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return ports.FactRecord{}, snapshot, errors.New("open reconciliation clock returned zero time")
	}
	token, err := o.deps.Leases.Read(ctx, input.Capsule, input.Lineage)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	openedFrom, err := validateOpenLeaseToken(input, token, now)
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	next := snapshot
	if openedFrom != nil {
		opened := openedFrom.Pointer.Generation
		next.OpenedGeneration = cloneGeneration(&opened)
		next.CurrentBase = cloneGeneration(&opened)
		openedLineage := openedFrom.Pointer.Lineage
		next.Recovery.Source = domain.SourceDecision{Kind: domain.SourceDecisionRemote, Lineage: &openedLineage, Generation: cloneGeneration(&opened)}
		if openedFrom.Pointer.Lineage == input.Lineage {
			next.CurrentPointer = clonePointer(&openedFrom.Pointer)
			next.ExpectedPointerRevision = string(openedFrom.Revision)
		} else {
			next.CurrentPointer = nil
			next.ExpectedPointerRevision = ""
		}
	}
	next.Lease = domain.LeaseRecord{Lease: &token.Lease, Revision: string(token.Revision)}
	receipt, err := json.Marshal(leaseReceipt(input, token))
	if err != nil {
		return ports.FactRecord{}, snapshot, err
	}
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now, Output: receipt}, next, nil
}

func validateOpenLeaseToken(input openLeaseAcquisitionInput, token coordination.LeaseToken, observedAt time.Time) (*coordination.PointerRecord, error) {
	if token.Lease.SessionID != input.Owner.SessionID || token.Lease.Machine != input.Owner.Machine {
		return nil, fmt.Errorf("lineage lease belongs to session %q: %w", token.Lease.SessionID, coordination.ErrLeaseHeld)
	}
	openedFrom := input.Observed
	if input.BranchSource {
		openedFrom = input.Source
	}
	var opened *domain.GenerationRef
	if openedFrom != nil {
		if openedFrom.Revision == "" {
			return nil, errors.New("lease acquisition intent lacks its opened pointer revision")
		}
		opened = &openedFrom.Pointer.Generation
	}
	expectedExpiry := input.Now.Add(input.LeaseTTL)
	if token.Lease.SchemaVersion != domain.SchemaVersion || token.Lease.Capsule != input.Capsule || token.Lease.Lineage != input.Lineage ||
		token.Lease.SessionID != input.Owner.SessionID || token.Lease.Machine != input.Owner.Machine || !sameGeneration(token.Lease.OpenedGeneration, opened) ||
		!token.Lease.CreatedAt.Equal(input.Now) || !token.Lease.HeartbeatAt.Equal(input.Now) || !token.Lease.ExpiresAt.Equal(expectedExpiry) ||
		token.Revision == "" || observedAt.Before(input.Now) || !expectedExpiry.After(observedAt) {
		return nil, errors.New("observed lease acquisition does not exactly match the pending intent")
	}
	return openedFrom, nil
}

func leaseReceipt(input openLeaseAcquisitionInput, token coordination.LeaseToken) openLeaseReceipt {
	return openLeaseReceipt{
		Capsule: input.Capsule, Lineage: input.Lineage, Session: token.Lease.SessionID, Machine: token.Lease.Machine,
		Revision: string(token.Revision), OpenedGeneration: cloneGeneration(token.Lease.OpenedGeneration),
		CreatedAt: token.Lease.CreatedAt, HeartbeatAt: token.Lease.HeartbeatAt, ExpiresAt: token.Lease.ExpiresAt,
		BranchSource: input.BranchSource, ObservedRevision: input.ObservedRevision,
	}
}

func (o *Open) validate(request OpenRequest) error {
	if o == nil || o.deps.Journal == nil || o.deps.Clock == nil || o.deps.Ownership == nil || o.deps.Initializer == nil || o.deps.DevPod == nil || o.deps.Target == nil || o.deps.Services == nil {
		return ErrOpenDependencies
	}
	if request.Capsule == "" {
		return errors.New("open capsule is empty")
	}
	if !strictXDGPaths(o.deps.Paths) {
		return errors.New("open XDG paths are incomplete or unsafe")
	}
	if _, err := (domain.Lineage{Branch: request.Branch}).PointerKey(request.Capsule); err != nil {
		return fmt.Errorf("open capsule or branch is unsafe: %w", err)
	}
	if _, err := request.SourceLineage.PointerKey(request.Capsule); err != nil {
		return fmt.Errorf("open source lineage is unsafe: %w", err)
	}
	backend := o.effectiveBackend(request)
	if !validResolvedBackend(backend) {
		return errors.New("open backend is not a strict credential-free backend descriptor")
	}
	if request.Backend.Root != "" || request.Backend.SanitizedURL != "" || request.Backend.Fingerprint != "" {
		requested := config.Backend{Kind: config.BackendFile, SanitizedURL: request.Backend.SanitizedURL, Fingerprint: request.Backend.Fingerprint, File: &request.Backend}
		if !sameBackendIdentity(requested, backend) {
			return fmt.Errorf("open request file backend identity does not match the constructor backend: %w", ErrOpenSessionMismatch)
		}
	}
	if request.ResolvedBackend.Kind != "" && o.deps.ResolvedBackend.Kind == "" {
		return fmt.Errorf("open request backend identity has no constructor-bound object store: %w", ErrOpenSessionMismatch)
	}
	if request.ResolvedBackend.Kind != "" && o.deps.ResolvedBackend.Kind != "" && !sameBackendIdentity(request.ResolvedBackend, o.deps.ResolvedBackend) {
		return fmt.Errorf("open request backend identity does not match the composed object store: %w", ErrOpenSessionMismatch)
	}
	if request.Mode != domain.SessionReadWrite && request.Mode != domain.SessionReadOnly {
		return fmt.Errorf("unsupported open session mode %q", request.Mode)
	}
	if request.EntryMode == domain.EntryIDE {
		return ErrOpenIDEUnsupported
	}
	if request.EntryMode != domain.EntryTerminal {
		return fmt.Errorf("unsupported open entry mode %q", request.EntryMode)
	}
	return nil
}

func normalizeOpenRequest(request OpenRequest) OpenRequest {
	if request.Capsule == "" {
		request.Capsule = request.Runtime.Capsule
	}
	if request.Branch == "" {
		request.Branch = "main"
	}
	if request.Mode == "" {
		request.Mode = domain.SessionReadWrite
	}
	if request.EntryMode == "" {
		request.EntryMode = domain.EntryTerminal
	}
	if request.Context == "" {
		request.Context = "default"
	}
	if request.Provider == "" {
		request.Provider = "docker"
		request.LocalProvider = true
	}
	if request.LeaseTTL <= 0 {
		request.LeaseTTL = 30 * time.Minute
	}
	if request.Machine == "" {
		request.Machine = "unknown-machine"
	}
	if request.SourceLineage.Branch == "" {
		request.SourceLineage = domain.Lineage{Branch: "main"}
	}
	return request
}

func (o *Open) create(ctx context.Context, request OpenRequest) (OpenResult, error) {
	sessionID := request.SessionID
	if sessionID == "" {
		var err error
		sessionID, err = newSessionID()
		if err != nil {
			return OpenResult{}, err
		}
	}
	if err := validateSessionIDForOpen(sessionID); err != nil {
		return OpenResult{}, err
	}
	request.SessionID = sessionID
	paths := o.deps.Paths
	sessionRoot := filepath.Join(paths.SessionRoot, sessionID)
	sessionRuntimeRoot := filepath.Join(sessionRoot, "runtime")
	if paths.RuntimeRoot != "" {
		sessionRuntimeRoot = filepath.Join(paths.RuntimeRoot, sessionID)
	}
	backend := o.effectiveBackend(request)
	runtime := request.Runtime
	runtime.Capsule = request.Capsule
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return OpenResult{}, errors.New("open clock returned zero time")
	}
	lineage := domain.Lineage{Branch: request.Branch}
	entryTarget := request.Target
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: sessionID, Capsule: request.Capsule, Lineage: lineage,
		Mode: request.Mode, State: domain.SessionOpening, CreatedAt: now, UpdatedAt: now,
		Workspace: domain.WorkspaceRecord{Context: request.Context, Provider: request.Provider, LocalProvider: request.LocalProvider},
		Cleanup:   domain.Cleanup{State: domain.CleanupPending},
		Recovery: domain.RecoveryRecord{
			Objective:     domain.RecoveryObjectiveOpen,
			Configuration: config.DurableBackendConfiguration(runtime, backend, paths),
			Session:       domain.SessionArtifactPaths{Root: sessionRoot, RuntimeRoot: sessionRuntimeRoot, HaulPath: filepath.Join(sessionRoot, "generation.tar.zst"), RegistryOverlay: filepath.Join(sessionRoot, "registry")},
			Entry:         domain.EntryRequestRecord{Mode: request.EntryMode, Target: entryTarget},
			Cleanup:       domain.CleanupPolicy{WorkspaceAction: domain.WorkspaceCleanupDelete, RemoveSessionArtifacts: true},
		},
	}
	if !request.LocalProvider && o.deps.RemoteDataPlane != nil {
		snapshot.Recovery.RemoteDataPlane = &domain.RemoteDataPlaneRecord{
			Mode:      domain.DataPlaneHaulerKitV1,
			AttemptID: sessionID + "-hauler-kit-v1",
		}
	}
	if err := o.deps.Journal.Create(ctx, snapshot); err != nil {
		return OpenResult{}, err
	}
	if err := ensureSessionRoot(sessionRoot); err != nil {
		return OpenResult{}, err
	}
	journal := newOpenJournal(o.deps.Journal, &snapshot, now)
	source, err := capsule.ResolveSource(capsule.SourceRequest{Capsule: request.Capsule, ExplicitPath: request.ExplicitRoot, ConfiguredPath: request.ConfiguredRoot, RemoteAvailable: request.RemoteAvailable})
	if err != nil {
		return OpenResult{}, err
	}
	var root string
	var observedPointer *coordination.PointerRecord
	var fetchLineage = lineage
	if source.Kind == capsule.SourceAdopted && o.deps.Pointers != nil {
		pointer, err := o.deps.Pointers.Read(ctx, request.Capsule, lineage)
		switch {
		case err == nil:
			observedPointer = &pointer
		case !errors.Is(err, ports.ErrNotFound):
			return OpenResult{}, err
		}
	}
	if source.Kind == capsule.SourceAdopted && observedPointer == nil {
		root = source.Root
		materialization, err := o.deps.Ownership.Adopt(root)
		if err != nil {
			return OpenResult{}, err
		}
		if err := journal.phase(ctx, "MaterializationAdopted", safeJSON(struct {
			Root string `json:"root"`
		}{Root: root}), materialization, func() error {
			snapshot.Materialization = materialization
			snapshot.Recovery.Source = domain.SourceDecision{Kind: domain.SourceDecisionAdopted, Root: root, Initialized: source.Initialized}
			snapshot.Recovery.Cleanup.RemoveOwnedMaterialization = false
			return nil
		}); err != nil {
			return OpenResult{}, err
		}
		if request.Mode == domain.SessionReadWrite {
			lease, err := o.acquireLocalLease(ctx, request, lineage, nil, journal)
			if err != nil {
				return OpenResult{}, err
			}
			snapshot.Lease = domain.LeaseRecord{Lease: &lease.Lease, Revision: string(lease.Revision)}
		}
	} else {
		if o.deps.Pointers == nil || o.deps.Generations == nil || o.deps.Hydrator == nil {
			return OpenResult{}, errors.New("remote open dependencies are incomplete")
		}
		var sourcePointer *coordination.PointerRecord
		if observedPointer == nil {
			pointer, resolvedSourcePointer, err := o.observeRemote(ctx, request, lineage, journal)
			if err != nil {
				return OpenResult{}, err
			}
			observedPointer = pointer
			sourcePointer = resolvedSourcePointer
		}
		if observedPointer == nil && sourcePointer == nil {
			return OpenResult{}, errors.New("remote capsule has no committed generation")
		}
		if sourcePointer != nil {
			observedPointer = sourcePointer
			fetchLineage = sourcePointer.Pointer.Lineage
		}
		opened := observedPointer.Pointer.Generation
		snapshot.OpenedGeneration = cloneGeneration(&opened)
		snapshot.CurrentBase = cloneGeneration(&opened)
		if observedPointer.Pointer.Lineage == lineage {
			snapshot.CurrentPointer = clonePointer(&observedPointer.Pointer)
			snapshot.ExpectedPointerRevision = string(observedPointer.Revision)
		}
		if request.Mode == domain.SessionReadWrite {
			lease, err := o.acquireRemoteLease(ctx, request, lineage, observedPointer, sourcePointer, journal)
			if err != nil {
				return OpenResult{}, err
			}
			snapshot.Lease = domain.LeaseRecord{Lease: &lease.Lease, Revision: string(lease.Revision)}
		}
		metadata, _, err := o.deps.Generations.ReadMetadata(ctx, request.Capsule, fetchLineage, opened)
		if err != nil {
			return OpenResult{}, err
		}
		token, err := capsule.NewOwnershipToken()
		if err != nil {
			return OpenResult{}, err
		}
		finalRoot := filepath.Join(o.deps.Ownership.MaterializationRoot(), request.Capsule, request.Branch, sessionID)
		stageRoot := filepath.Join(sessionRoot, "materialization-stage")
		hydrationRequest := hydration.Request{SessionID: sessionID, Capsule: request.Capsule, Generation: opened, Metadata: metadata, SessionRoot: sessionRoot, StageRoot: stageRoot, FinalRoot: finalRoot, HaulPath: snapshot.Recovery.Session.HaulPath, Token: token}
		hydrationPlan := domain.HydrationPlan{Token: token, StageRoot: stageRoot, FinalRoot: finalRoot}
		sourceLineage := fetchLineage
		snapshot.Recovery.Source = domain.SourceDecision{Kind: domain.SourceDecisionRemote, Lineage: &sourceLineage, Generation: cloneGeneration(&opened)}
		snapshot.Recovery.Cleanup.RemoveOwnedMaterialization = true
		planInput := openMaterializationPlanInput{Token: token, Stage: stageRoot, Final: finalRoot}
		if err := journal.phase(ctx, "MaterializationPlanned", planInput, nil, func() error {
			snapshot.Recovery.Hydration = &hydrationPlan
			return nil
		}); err != nil {
			return OpenResult{}, err
		}
		hydrator := o.deps.Hydrator
		if withHooks, ok := hydrator.(OpenHydratorWithHooks); ok {
			hydrator = withHooks.WithHooks(o.hydrationHooks(journal))
		}
		hydrated, err := hydrator.Hydrate(ctx, hydrationRequest)
		if err != nil {
			return OpenResult{}, err
		}
		if err := validateRemoteMaterialization(hydrated.Materialization, finalRoot, token); err != nil {
			return OpenResult{}, err
		}
		root = hydrated.Materialization.CanonicalPath
		snapshot.Materialization = hydrated.Materialization
	}
	return o.continueOpeningFromMaterialization(ctx, snapshot, request, journal)
}

func (o *Open) effectiveBackend(request OpenRequest) config.Backend {
	if o.deps.ResolvedBackend.Kind != "" {
		return o.deps.ResolvedBackend
	}
	file := o.deps.Backend
	if file.Root == "" && file.SanitizedURL == "" && file.Fingerprint == "" {
		return config.Backend{}
	}
	return config.Backend{Kind: config.BackendFile, SanitizedURL: file.SanitizedURL, Fingerprint: file.Fingerprint, File: &file}
}

func sameBackendIdentity(left, right config.Backend) bool {
	return validResolvedBackend(left) && validResolvedBackend(right) && left.Kind == right.Kind && left.SanitizedURL == right.SanitizedURL && left.Fingerprint == right.Fingerprint
}

func validResolvedBackend(backend config.Backend) bool {
	switch backend.Kind {
	case config.BackendFile:
		if backend.File == nil {
			return false
		}
		resolved, err := config.ResolveBackend(backend.SanitizedURL, config.S3Values{})
		return err == nil && resolved.Fingerprint == backend.Fingerprint && resolved.File != nil && *resolved.File == *backend.File
	case config.BackendS3:
		if backend.S3 == nil {
			return false
		}
		resolved, err := config.ResolveBackend(backend.SanitizedURL, config.S3Values{
			Endpoint: backend.S3.Endpoint, Region: backend.S3.Region, PathStyle: backend.S3.PathStyle, Insecure: backend.S3.Insecure,
		})
		return err == nil && resolved.Fingerprint == backend.Fingerprint && resolved.S3 != nil && *resolved.S3 == *backend.S3
	default:
		return false
	}
}

func validRemoteDataPlaneResult(result RemoteDataPlaneResult, selected domain.RemoteDataPlaneRecord, materialization string) bool {
	record := result.Record
	return validRoot(result.BootstrapRoot) && filepath.Clean(result.BootstrapRoot) != materialization &&
		result.BootstrapRoot == record.BootstrapRoot && record.Mode == domain.DataPlaneHaulerKitV1 &&
		record.Mode == selected.Mode && record.AttemptID == selected.AttemptID &&
		len(record.KitSHA256) == 64 && record.KitSize > 0 &&
		len(record.ManifestSHA256) == 64 && record.ManifestSize > 0 &&
		immutableImage(record.SourceImage) && localImageID(record.OuterImage) &&
		record.RequestSchema != 0 && strings.HasSuffix(record.AttemptID, "-hauler-kit-v1") &&
		record.RequestSession == strings.TrimSuffix(record.AttemptID, "-hauler-kit-v1") &&
		validRoot(record.WorkspaceRoot) && validRoot(record.RuntimeRoot) && validRoot(record.ManifestPath) &&
		strings.HasPrefix(record.Architecture, "linux/") && len(record.ConfigSHA256) == 64 && record.ConfigSize > 0
}

func validateSnapshotBackend(snapshot domain.JournalSnapshot, backend config.Backend) error {
	configuration := snapshot.Recovery.Configuration
	if configuration.BackendKind == "" && configuration.BackendURL == "" && configuration.BackendFingerprint == "" {
		return nil
	}
	if !validResolvedBackend(backend) || configuration.BackendKind != string(backend.Kind) || configuration.BackendURL != backend.SanitizedURL || configuration.BackendFingerprint != backend.Fingerprint {
		return fmt.Errorf("session %q backend identity does not match the composed object store: %w", snapshot.SessionID, ErrOpenSessionMismatch)
	}
	return nil
}

func (o *Open) continueOpeningFromMaterialization(ctx context.Context, snapshot domain.JournalSnapshot, request OpenRequest, journal *openJournal) (OpenResult, error) {
	journal.snapshot = &snapshot
	root := snapshot.Materialization.CanonicalPath
	if root == "" {
		return OpenResult{}, errors.New("opening session has no durable materialization")
	}
	initialization, err := o.deps.Initializer.Initialize(ctx, root, request.Capsule)
	if err != nil {
		return OpenResult{}, err
	}
	snapshot.Tools = initialization.Lock.Tools
	if err := journal.phase(ctx, "CapsuleInitialized", safeJSON(struct {
		Root string `json:"root"`
	}{Root: root}), initialization, func() error { snapshot.Recovery.Source.Initialized = true; return nil }); err != nil {
		return OpenResult{}, err
	}
	devcontainer, err := capsule.ResolveDevcontainer(root, request.Runtime.DevcontainerPath, initialization.Lock)
	if err != nil {
		return OpenResult{}, err
	}
	if err := journal.phase(ctx, "DevcontainerResolved", safeJSON(struct {
		Path string `json:"path"`
	}{Path: devcontainer.Path}), devcontainer, func() error { snapshot.Recovery.Configuration.DevcontainerPath = devcontainer.Path; return nil }); err != nil {
		return OpenResult{}, err
	}
	bootstrapRoot := ""
	dataPlane := snapshot.Recovery.RemoteDataPlane
	if !request.LocalProvider && dataPlane != nil && dataPlane.Mode == domain.DataPlaneHaulerKitV1 {
		if o.deps.RemoteDataPlane == nil {
			return OpenResult{}, errors.New("remote Hauler data plane dependency is incomplete")
		}
		if !validRoot(dataPlane.BootstrapRoot) {
			intent, err := journal.ensureIntent(ctx, "RemoteDataPlanePrepared", *dataPlane)
			if err != nil {
				return OpenResult{}, err
			}
			prepared, err := o.deps.RemoteDataPlane.Prepare(ctx, RemoteDataPlaneRequest{
				SessionID: snapshot.SessionID, AttemptID: dataPlane.AttemptID, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage, Generation: cloneGeneration(snapshot.OpenedGeneration),
				Materialization: root, DevcontainerPath: devcontainer.Path,
			})
			if err != nil {
				return OpenResult{}, fmt.Errorf("prepare remote Hauler data plane: %w", err)
			}
			if !validRemoteDataPlaneResult(prepared, *dataPlane, root) {
				return OpenResult{}, errors.New("remote Hauler data plane returned an invalid bootstrap root")
			}
			bootstrapRoot = prepared.BootstrapRoot
			snapshot.Recovery.RemoteDataPlane = &prepared.Record
			if err := journal.recordFact(ctx, intent, prepared.Record, nil); err != nil {
				return OpenResult{}, err
			}
		} else {
			prepared, err := o.deps.RemoteDataPlane.Prepare(ctx, RemoteDataPlaneRequest{
				SessionID: snapshot.SessionID, AttemptID: dataPlane.AttemptID, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage, Generation: cloneGeneration(snapshot.OpenedGeneration),
				Materialization: root, DevcontainerPath: devcontainer.Path,
			})
			if err != nil {
				return OpenResult{}, fmt.Errorf("verify recorded remote Hauler data plane: %w", err)
			}
			if !validRemoteDataPlaneResult(prepared, *dataPlane, root) || prepared.Record != *dataPlane {
				return OpenResult{}, errors.New("recorded remote Hauler data plane identity changed")
			}
			bootstrapRoot = prepared.BootstrapRoot
		}
	}
	targetResult, err := o.deps.Target.Resolve(ctx, root, request.Target)
	if err != nil {
		return OpenResult{}, err
	}
	if err := journal.phase(ctx, "TargetResolved", safeJSON(struct {
		Relative string `json:"relative"`
	}{Relative: targetResult.Relative}), targetResult, func() error { snapshot.Recovery.Entry.Target = targetResult.Relative; return nil }); err != nil {
		return OpenResult{}, err
	}
	snapshot, err = o.deps.Services.Start(ctx, snapshot)
	if err != nil {
		return OpenResult{}, fmt.Errorf("start production services: %w", err)
	}
	journal.snapshot = &snapshot
	workspaceID := workspace.DeterministicID(request.Capsule, request.Branch, root)
	checkpoint := ""
	if snapshot.OpenedGeneration != nil {
		checkpoint = strconv.FormatUint(snapshot.OpenedGeneration.Generation, 10)
	}
	devcontainerArgument, err := filepath.Rel(root, devcontainer.Path)
	if err != nil || devcontainerArgument == "." || devcontainerArgument == ".." || strings.HasPrefix(devcontainerArgument, ".."+string(filepath.Separator)) {
		return OpenResult{}, fmt.Errorf("derive capsule-relative devcontainer path: %w", capsule.ErrInvalidDevcontainer)
	}
	upOptions := devpodadapter.UpOptions{WorkspacePath: root, WorkspaceID: workspaceID, Context: request.Context, Provider: request.Provider, DevcontainerPath: devcontainerArgument, CampEnvironment: &devpodadapter.CampEnvironment{Registry: endpoint(snapshot.Recovery.Configuration.RegistryPort), Fileserver: endpoint(snapshot.Recovery.Configuration.FileserverPort), Capsule: request.Capsule, Checkpoint: checkpoint}}
	if bootstrapRoot != "" {
		upOptions.BootstrapPath = bootstrapRoot
		upOptions.SourceMode = devpodadapter.SourceModeBootstrap
		upOptions.DevcontainerPath = ".camp-bootstrap/devcontainer.json"
	}
	if o.deps.Providers != nil {
		if err := o.deps.Providers.EnsureProvider(ctx, request.Context, request.Provider); err != nil {
			return OpenResult{}, fmt.Errorf("ensure DevPod provider: %w", err)
		}
	}
	sourceRoot := root
	if bootstrapRoot != "" {
		sourceRoot = bootstrapRoot
	}
	workspaceRecord := domain.WorkspaceRecord{ID: workspaceID, Context: request.Context, Provider: request.Provider, LocalProvider: request.LocalProvider, LocalFolder: sourceRoot, Target: targetResult.Relative, StagingRoot: root}
	var upResult ports.Result
	upInput := openWorkspaceUpInput{ID: workspaceID, Context: request.Context, Provider: request.Provider, SourceRoot: sourceRoot, Env: *upOptions.CampEnvironment}
	intent, err := journal.ensureIntent(ctx, "WorkspaceUp", safeJSON(upInput))
	if err != nil {
		return OpenResult{}, err
	}
	upResult, err = o.deps.DevPod.Up(ctx, upOptions)
	if err != nil {
		if upResult.ExitCode > 0 {
			if settleErr := o.settleKnownWorkspaceUpFailure(ctx, intent, upInput, upResult); settleErr != nil {
				return OpenResult{}, errors.Join(workspaceUpFailureError(err, upResult), settleErr)
			}
		}
		return OpenResult{}, workspaceUpFailureError(err, upResult)
	}
	snapshot.Workspace = workspaceRecord
	if err := journal.recordFact(ctx, intent, &upResult, nil); err != nil {
		return OpenResult{}, err
	}
	snapshot.Recovery.Configuration.DevcontainerPath = devcontainer.Path
	return o.completeWorkspaceOpen(ctx, snapshot, request, targetResult)
}

func (o *Open) settleKnownWorkspaceUpFailure(ctx context.Context, intent ports.IntentRecord, input openWorkspaceUpInput, result ports.Result) error {
	ctx = context.WithoutCancel(ctx)
	workspaces, err := o.deps.DevPod.ListInContext(ctx, input.Context)
	if err != nil {
		return fmt.Errorf("verify failed WorkspaceUp absence: %w", err)
	}
	for _, candidate := range workspaces {
		if candidate.ID != input.ID {
			continue
		}
		if candidate.Context != input.Context || candidate.Provider.Name != input.Provider || candidate.Source.LocalFolder != input.SourceRoot {
			return errors.New("failed WorkspaceUp created a workspace with mismatched identity")
		}
		return ambiguousWorkspaceUpError(result)
	}
	snapshot, pending, err := o.deps.Journal.Load(ctx, intent.SessionID)
	if err != nil {
		return fmt.Errorf("reload failed WorkspaceUp intent: %w", err)
	}
	if !containsPendingIntent(pending, intent.ID) {
		return nil
	}
	fact := ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: o.deps.Clock.Now(), Output: safeJSON(struct {
		Failed   bool `json:"failed"`
		ExitCode int  `json:"exitCode"`
	}{Failed: true, ExitCode: result.ExitCode})}
	if err := o.deps.Journal.RecordFact(ctx, fact, snapshot); err != nil {
		return fmt.Errorf("record failed WorkspaceUp attempt: %w", err)
	}
	return nil
}

func ambiguousWorkspaceUpError(result ports.Result) error {
	diagnostic := workspaceUpDiagnosticText(result)
	if diagnostic == "" {
		return ports.ErrAmbiguous
	}
	return fmt.Errorf("%w; DevPod diagnostic: %s", ports.ErrAmbiguous, diagnostic)
}

func workspaceUpFailureError(cause error, result ports.Result) error {
	diagnostic := workspaceUpDiagnosticText(result)
	if diagnostic == "" {
		return cause
	}
	return fmt.Errorf("%w; DevPod diagnostic: %s", cause, diagnostic)
}

func workspaceUpDiagnosticText(result ports.Result) string {
	diagnostic, omitted := boundedWorkspaceUpDiagnostic(result.Stderr)
	if diagnostic == "" {
		diagnostic, omitted = boundedWorkspaceUpDiagnostic(result.Stdout)
	}
	if diagnostic == "" {
		return ""
	}
	if omitted {
		diagnostic = "[earlier DevPod output omitted]\n" + diagnostic
	}
	return limitWorkspaceUpDiagnosticRendered(diagnostic)
}

func boundedWorkspaceUpDiagnostic(raw []byte) (string, bool) {
	omitted := false
	if len(raw) > workspaceUpDiagnostic {
		raw = raw[len(raw)-workspaceUpDiagnostic:]
		omitted = true
	}
	return strings.TrimSpace(sanitizeWorkspaceUpDiagnostic(raw)), omitted
}

func limitWorkspaceUpDiagnosticRendered(rendered string) string {
	if len(rendered) <= workspaceUpDiagnostic {
		return rendered
	}
	const note = "[earlier DevPod output omitted]\n"
	allowance := workspaceUpDiagnostic - len(note)
	if allowance < 0 {
		allowance = 0
	}
	rendered = suffixAtRuneBoundary(rendered, allowance)
	if len(rendered) > allowance {
		rendered = suffixAtRuneBoundary(rendered, allowance)
	}
	return note + rendered
}

func suffixAtRuneBoundary(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	start := len(text) - maxBytes
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}

func sanitizeWorkspaceUpDiagnostic(raw []byte) string {
	var sanitized strings.Builder
	for _, line := range strings.SplitAfter(strings.ToValidUTF8(string(raw), "\uFFFD"), "\n") {
		if line == "" {
			continue
		}
		hasTrailingNewline := strings.HasSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\n")
		line = redactWorkspaceUpDiagnosticLine(line)
		for _, value := range line {
			if value == '\t' {
				sanitized.WriteRune(value)
				continue
			}
			if unicode.IsControl(value) || unicode.In(value, unicode.Cf) {
				if value <= 0xff {
					fmt.Fprintf(&sanitized, `\x%02x`, value)
				} else if value <= 0xffff {
					fmt.Fprintf(&sanitized, `\u%04x`, value)
				} else {
					fmt.Fprintf(&sanitized, `\U%08x`, value)
				}
				continue
			}
			sanitized.WriteRune(value)
		}
		if hasTrailingNewline {
			sanitized.WriteByte('\n')
		}
	}
	return sanitized.String()
}

func redactWorkspaceUpDiagnosticLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return trimmed
	}
	if key, _, ok := strings.Cut(trimmed, "="); ok && diagnosticKeyLooksSecret(key) {
		return strings.TrimSpace(key) + "=[redacted]"
	}
	if key, _, ok := strings.Cut(trimmed, ":"); ok && diagnosticKeyLooksSecret(key) {
		return strings.TrimSpace(key) + ": [redacted]"
	}
	if redacted := workspaceOpaqueCredentialPattern.ReplaceAllString(trimmed, "[redacted DevPod secret]"); redacted != trimmed {
		return redacted
	}
	if diagnosticContainsSecretMarker(trimmed) {
		return "[redacted DevPod secret]"
	}
	return trimmed
}

func diagnosticKeyLooksSecret(key string) bool {
	key = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(key, "export ")))
	for _, marker := range []string{"secret", "token", "password", "passphrase", "credential", "api_key", "api-key", "apikey", "access_key", "access-key", "private_key", "private-key", "client_secret", "client-secret"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func diagnosticContainsSecretMarker(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{"secret", "token", "password", "passphrase", "credential", "api_key", "api-key", "apikey", "access_key", "access-key", "private_key", "private-key", "client_secret", "client-secret"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func (o *Open) reenter(ctx context.Context, snapshot domain.JournalSnapshot, request OpenRequest) (OpenResult, error) {
	if err := validateSnapshotBackend(snapshot, o.effectiveBackend(request)); err != nil {
		return OpenResult{}, err
	}
	if (request.SessionID != "" && request.SessionID != snapshot.SessionID) || request.Capsule != snapshot.Capsule || (domain.Lineage{Branch: request.Branch}) != snapshot.Lineage ||
		(request.Mode != "" && request.Mode != snapshot.Mode) || (request.Context != "" && request.Context != snapshot.Workspace.Context) ||
		(request.Provider != "" && request.Provider != snapshot.Workspace.Provider) {
		return OpenResult{}, fmt.Errorf("session %q identity does not match the open request: %w", snapshot.SessionID, ErrOpenSessionMismatch)
	}
	if err := validateOpenRecoveryObjective(snapshot); err != nil {
		return OpenResult{}, err
	}
	if snapshot.State == domain.SessionOpening || snapshot.State == domain.SessionRecovering {
		return o.resumeOpening(ctx, snapshot, request)
	}
	if snapshot.State != domain.SessionOpen {
		return OpenResult{}, fmt.Errorf("session %q is %s: %w", snapshot.SessionID, snapshot.State, ErrRecoveryRequired)
	}
	if err := o.validateOpenSession(snapshot, true); err != nil {
		return OpenResult{}, err
	}
	root := snapshot.Materialization.CanonicalPath
	if root == "" {
		root = snapshot.Workspace.StagingRoot
	}
	targetName := request.Target
	if targetName == "" {
		targetName = snapshot.Recovery.Entry.Target
	}
	targetResult, err := o.deps.Target.Resolve(ctx, root, targetName)
	if err != nil {
		return OpenResult{}, err
	}
	effectiveRoot := snapshot.Workspace.EffectiveRoot
	mapped, err := workspace.MapTarget(snapshot.Workspace.StagingRoot, effectiveRoot, targetResult.Relative)
	if err != nil {
		return OpenResult{}, err
	}
	var entryResult ports.Result
	if request.EntryMode == domain.EntryIDE {
		return OpenResult{}, errors.New("IDE re-entry is downstream of Task 3B")
	}
	entryResult, err = o.deps.DevPod.SSH(ctx, devpodadapter.SSHOptions{WorkspaceID: snapshot.Workspace.ID, Context: snapshot.Workspace.Context, Workdir: mapped, StartServices: true})
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Snapshot: snapshot, Target: targetResult, MappedTarget: mapped, DevPodResult: entryResult, WorkspaceID: snapshot.Workspace.ID, RecoveryCommand: "camp recover " + snapshot.SessionID}, nil
}

func validateOpenRecoveryObjective(snapshot domain.JournalSnapshot) error {
	if snapshot.Recovery.Objective != domain.RecoveryObjectiveOpen {
		return fmt.Errorf("session %q recovery objective %q is invalid: %w", snapshot.SessionID, snapshot.Recovery.Objective, ErrOpenRecoveryObjective)
	}
	return nil
}

func (o *Open) resumeOpening(ctx context.Context, snapshot domain.JournalSnapshot, request OpenRequest) (OpenResult, error) {
	loaded, pending, err := o.deps.Journal.Load(ctx, snapshot.SessionID)
	if err != nil {
		return OpenResult{}, err
	}
	if hasPendingHydration(pending) {
		loaded, err = o.resumeHydration(ctx, loaded, pending)
		if err != nil {
			return OpenResult{}, err
		}
	}
	reconciled, err := o.Reconcile(ctx, snapshot.SessionID)
	if err != nil {
		return OpenResult{}, err
	}
	if reconciled.Workspace.ID == "" || reconciled.Workspace.LocalFolder == "" || reconciled.Workspace.StagingRoot == "" {
		if reconciled.Materialization.CanonicalPath == "" {
			return OpenResult{}, fmt.Errorf("session %q has not reached durable workspace readiness: %w", snapshot.SessionID, ErrRecoveryRequired)
		}
		request = openRequestFromRecovery(request, reconciled)
		now := o.deps.Clock.Now().UTC()
		if now.IsZero() {
			return OpenResult{}, errors.New("open recovery clock returned zero time")
		}
		return o.continueOpeningFromMaterialization(ctx, reconciled, request, newOpenJournal(o.deps.Journal, &reconciled, now))
	}
	if err := o.validateOpenSession(reconciled, false); err != nil {
		return OpenResult{}, err
	}
	root := reconciled.Materialization.CanonicalPath
	targetName := request.Target
	if targetName == "" {
		targetName = reconciled.Recovery.Entry.Target
	}
	targetResult, err := o.deps.Target.Resolve(ctx, root, targetName)
	if err != nil {
		return OpenResult{}, err
	}
	return o.completeWorkspaceOpen(ctx, reconciled, request, targetResult)
}

func hasPendingHydration(pending []ports.PendingIntent) bool {
	for _, item := range pending {
		if strings.HasPrefix(item.Intent.Transition, "Hydration") {
			return true
		}
	}
	return false
}

func (o *Open) resumeHydration(ctx context.Context, snapshot domain.JournalSnapshot, pending []ports.PendingIntent) (domain.JournalSnapshot, error) {
	plan := snapshot.Recovery.Hydration
	source := snapshot.Recovery.Source
	if o.deps.Hydrator == nil || o.deps.Generations == nil || o.deps.Clock == nil || plan == nil || source.Kind != domain.SourceDecisionRemote || source.Lineage == nil || source.Generation == nil {
		return snapshot, errors.New("pending hydration recovery dependencies or durable plan are incomplete")
	}
	if snapshot.OpenedGeneration == nil || *snapshot.OpenedGeneration != *source.Generation {
		return snapshot, errors.New("pending hydration generation does not match the opened generation")
	}
	metadata, _, err := o.deps.Generations.ReadMetadata(ctx, snapshot.Capsule, *source.Lineage, *source.Generation)
	if err != nil {
		return snapshot, err
	}
	request := hydration.Request{
		SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Generation: *source.Generation, Metadata: metadata,
		SessionRoot: snapshot.Recovery.Session.Root, StageRoot: plan.StageRoot, FinalRoot: plan.FinalRoot,
		HaulPath: snapshot.Recovery.Session.HaulPath, Token: plan.Token,
	}
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return snapshot, errors.New("open recovery clock returned zero time")
	}
	journal := newOpenJournal(o.deps.Journal, &snapshot, now)
	for _, item := range pending {
		if !strings.HasPrefix(item.Intent.Transition, "Hydration") {
			continue
		}
		if err := journal.adoptPending(item.Intent); err != nil {
			return snapshot, err
		}
	}
	withHooks, ok := o.deps.Hydrator.(OpenHydratorWithHooks)
	if !ok {
		return snapshot, errors.New("pending hydration recovery requires durable phase hooks")
	}
	result, err := withHooks.WithHooks(o.hydrationHooks(journal)).Hydrate(ctx, request)
	if err != nil {
		return snapshot, err
	}
	if err := validateRemoteMaterialization(result.Materialization, plan.FinalRoot, plan.Token); err != nil {
		return snapshot, err
	}
	stageID := transitionID(snapshot.SessionID, "Hydration"+string(hydration.PhaseStageCreated))
	if intent, ok := journal.pending[stageID]; ok {
		phaseResult := hydration.Result{StageRoot: plan.StageRoot, FinalRoot: plan.FinalRoot, Token: plan.Token}
		if err := journal.recordFact(ctx, intent, phaseResult, nil); err != nil {
			return snapshot, err
		}
	}
	snapshot.Materialization = result.Materialization
	return snapshot, nil
}

func openRequestFromRecovery(request OpenRequest, snapshot domain.JournalSnapshot) OpenRequest {
	request.SessionID = snapshot.SessionID
	request.Capsule = snapshot.Capsule
	request.Branch = snapshot.Lineage.Branch
	request.Mode = snapshot.Mode
	request.Context = snapshot.Workspace.Context
	request.Provider = snapshot.Workspace.Provider
	request.LocalProvider = snapshot.Workspace.LocalProvider
	request.EntryMode = snapshot.Recovery.Entry.Mode
	request.Target = snapshot.Recovery.Entry.Target
	request.Runtime.Capsule = snapshot.Capsule
	request.Runtime.RegistryPort = snapshot.Recovery.Configuration.RegistryPort
	request.Runtime.FileserverPort = snapshot.Recovery.Configuration.FileserverPort
	request.Runtime.DevcontainerPath = snapshot.Recovery.Configuration.DevcontainerPath
	return request
}

func (o *Open) completeWorkspaceOpen(ctx context.Context, snapshot domain.JournalSnapshot, request OpenRequest, targetResult target.Result) (OpenResult, error) {
	now := o.deps.Clock.Now().UTC()
	if now.IsZero() {
		return OpenResult{}, errors.New("open clock returned zero time")
	}
	journal := newOpenJournal(o.deps.Journal, &snapshot, now)
	root := snapshot.Materialization.CanonicalPath
	effectiveRoot := snapshot.Workspace.EffectiveRoot
	if effectiveRoot == "" {
		if err := journal.phase(ctx, "WorkspaceRootResolved", safeJSON(openWorkspaceRootInput{ID: snapshot.Workspace.ID}), &effectiveRoot, func() error {
			var err error
			effectiveRoot, err = o.deps.DevPod.ResolveWorkspaceFolderInContext(ctx, snapshot.Workspace.Context, snapshot.Workspace.ID)
			if err == nil {
				snapshot.Workspace.EffectiveRoot = effectiveRoot
			}
			return err
		}); err != nil {
			return OpenResult{}, err
		}
	}
	if !filepath.IsAbs(effectiveRoot) || filepath.Clean(effectiveRoot) == string(filepath.Separator) {
		return OpenResult{}, errors.New("observed DevPod workspace root is unsafe")
	}
	effectiveRoot = filepath.Clean(effectiveRoot)
	snapshot.Workspace.EffectiveRoot = effectiveRoot
	if !snapshot.Workspace.LocalProvider && !snapshot.Workspace.HardlinksRestored && o.deps.Hardlinks != nil {
		restore := workspace.HardlinkRestoreRequest{WorkspaceID: snapshot.Workspace.ID, Context: snapshot.Workspace.Context, LocalRoot: root, RemoteRoot: effectiveRoot}
		if err := journal.phase(ctx, "WorkspaceHardlinksRestored", restore, nil, func() error {
			if err := o.deps.Hardlinks.Restore(ctx, restore); err != nil {
				return err
			}
			snapshot.Workspace.HardlinksRestored = true
			return nil
		}); err != nil {
			return OpenResult{}, fmt.Errorf("restore remote workspace hardlinks: %w", err)
		}
	}
	mapped, err := workspace.MapTarget(root, effectiveRoot, targetResult.Relative)
	if err != nil {
		return OpenResult{}, err
	}
	if o.deps.Forwarders != nil {
		registryPort, fileserverPort, err := committedServicePorts(snapshot)
		if err != nil {
			return OpenResult{}, err
		}
		indexByName := make(map[string]int, len(snapshot.Recovery.Forwarding))
		for index, record := range snapshot.Recovery.Forwarding {
			indexByName[record.Name] = index
		}
		for _, item := range []struct {
			name          string
			localPort     int
			workspacePort int
		}{{"registry", registryPort, 5000}, {"fileserver", fileserverPort, 8080}} {
			request := domain.ForwardingRequest{
				Name: item.name, WorkspaceID: snapshot.Workspace.ID, Context: snapshot.Workspace.Context,
				LocalEndpoint: endpoint(item.localPort), WorkspaceEndpoint: endpoint(item.workspacePort),
				LogPath:      filepath.Join(snapshot.Recovery.Session.RuntimeRoot, item.name+"-forward.log"),
				EvidencePath: filepath.Join(snapshot.Recovery.Session.RuntimeRoot, item.name+"-forward.json"),
			}
			if index, ok := indexByName[item.name]; ok {
				record, err := o.deps.Forwarders.Observe(ctx, request)
				if err == nil {
					snapshot.Recovery.Forwarding[index] = record
					continue
				}
				if stopErr := o.deps.Forwarders.Stop(context.WithoutCancel(ctx), snapshot.Recovery.Forwarding[index]); stopErr != nil {
					return OpenResult{}, fmt.Errorf("reconcile existing %s workspace forwarder: %w", request.Name, stopErr)
				}
				snapshot.Recovery.Forwarding = append(snapshot.Recovery.Forwarding[:index], snapshot.Recovery.Forwarding[index+1:]...)
				delete(indexByName, item.name)
				for i := 0; i < len(snapshot.Recovery.Forwarding); i++ {
					indexByName[snapshot.Recovery.Forwarding[i].Name] = i
				}
			}
			var record domain.ForwardingRecord
			if err := journal.phase(ctx, "ForwarderStarted:"+item.name, request, &record, func() error {
				var err error
				record, err = o.deps.Forwarders.Start(ctx, request)
				if err == nil {
					snapshot.Recovery.Forwarding = append(snapshot.Recovery.Forwarding, record)
				}
				return err
			}); err != nil {
				for index := len(snapshot.Recovery.Forwarding) - 1; index >= 0; index-- {
					_ = o.deps.Forwarders.Stop(context.WithoutCancel(ctx), snapshot.Recovery.Forwarding[index])
				}
				return OpenResult{}, fmt.Errorf("start %s workspace forwarder: %w", item.name, err)
			}
		}
	}
	if !snapshot.Workspace.ImagesRestored && o.deps.Images != nil {
		inventory, err := loadOpenImageInventory(root)
		if err != nil {
			return OpenResult{}, err
		}
		snapshot.Images = inventory
		if len(inventory.Images) > 0 {
			registry, err := checkpointRegistryRuntime(snapshot)
			if err != nil {
				return OpenResult{}, err
			}
			restore := imageops.RestoreRequest{
				Scope:             imageops.EngineScope{Context: snapshot.Workspace.Context, WorkspaceID: snapshot.Workspace.ID},
				RegistryAuthority: registry.authority, RegistryEndpoint: registry.endpoint, Inventory: inventory,
			}
			var restored imageops.RestoreResult
			if err := journal.phase(ctx, "WorkspaceImagesRestored", restore, &restored, func() error {
				var err error
				restored, err = o.deps.Images.Restore(ctx, restore)
				if err == nil {
					snapshot.Workspace.ImagesRestored = true
				}
				return err
			}); err != nil {
				return OpenResult{}, fmt.Errorf("restore workspace images: %w", err)
			}
		}
	}
	snapshot.State = domain.SessionOpen
	snapshot.Recovery.Entry.Mode = request.EntryMode
	if err := journal.phase(ctx, "SessionOpened", safeJSON(openSessionOpenedInput{ID: snapshot.Workspace.ID}), nil, func() error { return nil }); err != nil {
		return OpenResult{}, err
	}
	var entryResult ports.Result
	if request.EntryMode == domain.EntryTerminal {
		intent, err := journal.ensureIntent(ctx, "TerminalEntryDispatched", safeJSON(openTerminalEntryInput{ID: snapshot.Workspace.ID, Workdir: mapped}))
		if err != nil {
			return OpenResult{}, err
		}
		var entryErr error
		entryStarted := false
		entryResult, entryErr = o.deps.DevPod.SSHWithStart(ctx, devpodadapter.SSHOptions{WorkspaceID: snapshot.Workspace.ID, Context: snapshot.Workspace.Context, Workdir: mapped, StartServices: true}, func() error {
			entryStarted = true
			return journal.recordFact(ctx, intent, struct {
				Started bool `json:"started"`
			}{Started: true}, nil)
		})
		if entryErr != nil {
			if err := o.settleFailedTerminalEntry(ctx, intent, now, entryStarted); err != nil {
				return OpenResult{}, fmt.Errorf("workspace is open; run camp attach to enter it; terminal entry outcome could not be durably settled: %w", errors.Join(entryErr, err))
			}
			return OpenResult{}, fmt.Errorf("workspace is open; run camp attach to enter it: %w", entryErr)
		}
	}
	return OpenResult{Snapshot: snapshot, Target: targetResult, MappedTarget: mapped, DevPodResult: entryResult, WorkspaceID: snapshot.Workspace.ID, RecoveryCommand: "camp recover " + snapshot.SessionID}, nil
}

func loadOpenImageInventory(root string) (domain.ImageInventory, error) {
	body, err := os.ReadFile(filepath.Join(root, ".camp", "images.json"))
	if err != nil {
		return domain.ImageInventory{}, fmt.Errorf("read hydrated image inventory: %w", err)
	}
	var inventory domain.ImageInventory
	if err := json.Unmarshal(body, &inventory); err != nil {
		return domain.ImageInventory{}, fmt.Errorf("decode hydrated image inventory: %w", err)
	}
	if err := validateCapturedInventory(inventory); err != nil {
		return domain.ImageInventory{}, fmt.Errorf("validate hydrated image inventory: %w", err)
	}
	return inventory, nil
}

func (o *Open) settleFailedTerminalEntry(ctx context.Context, intent ports.IntentRecord, now time.Time, started bool) error {
	ctx = context.WithoutCancel(ctx)
	snapshot, pending, err := o.deps.Journal.Load(ctx, intent.SessionID)
	if err != nil {
		return fmt.Errorf("reload terminal entry intent: %w", err)
	}
	if !containsPendingIntent(pending, intent.ID) {
		return nil
	}
	fact := ports.FactRecord{
		IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: now,
		Output: safeJSON(struct {
			Started bool `json:"started"`
		}{Started: started}),
	}
	if err := o.deps.Journal.RecordFact(ctx, fact, snapshot); err != nil {
		_, after, loadErr := o.deps.Journal.Load(ctx, intent.SessionID)
		if loadErr != nil {
			return errors.Join(err, fmt.Errorf("reload terminal entry intent after fact error: %w", loadErr))
		}
		if containsPendingIntent(after, intent.ID) {
			return fmt.Errorf("record terminal entry failure fact: %w", err)
		}
	}
	return nil
}

func containsPendingIntent(pending []ports.PendingIntent, intentID string) bool {
	for _, item := range pending {
		if item.Intent.ID == intentID {
			return true
		}
	}
	return false
}

func containsPendingTransition(pending []ports.PendingIntent, transition string) bool {
	for _, item := range pending {
		if item.Intent.Transition == transition {
			return true
		}
	}
	return false
}

func (o *Open) validateOpenSession(snapshot domain.JournalSnapshot, requireEffectiveRoot bool) error {
	if err := validateOpenReentrySource(snapshot); err != nil {
		return fmt.Errorf("session %q recovery source is inconsistent: %w", snapshot.SessionID, err)
	}
	if err := o.deps.Ownership.Revalidate(snapshot.Materialization); err != nil {
		return fmt.Errorf("revalidate session materialization: %w", err)
	}
	if snapshot.Materialization.Mode == domain.MaterializationCreated {
		expectedRoot := filepath.Join(o.deps.Ownership.MaterializationRoot(), snapshot.Capsule, snapshot.Lineage.Branch, snapshot.SessionID)
		if snapshot.Materialization.CanonicalPath != filepath.Clean(expectedRoot) {
			return fmt.Errorf("created session materialization path changed: %w", capsule.ErrOwnershipMismatch)
		}
	}
	if snapshot.Workspace.StagingRoot != snapshot.Materialization.CanonicalPath {
		return errors.New("session workspace staging root does not match its materialization")
	}
	expectedSourceRoot := snapshot.Materialization.CanonicalPath
	if record := snapshot.Recovery.RemoteDataPlane; record != nil && record.Mode == domain.DataPlaneHaulerKitV1 {
		expectedSourceRoot = record.BootstrapRoot
	}
	if snapshot.Workspace.LocalFolder != expectedSourceRoot {
		return errors.New("session workspace local folder does not match its selected data plane")
	}
	if requireEffectiveRoot && !filepath.IsAbs(snapshot.Workspace.EffectiveRoot) {
		return errors.New("session effective workspace root is missing or not absolute")
	}
	if snapshot.Workspace.ID != workspace.DeterministicID(snapshot.Capsule, snapshot.Lineage.Branch, snapshot.Materialization.CanonicalPath) {
		return errors.New("session workspace ID does not match its materialization")
	}
	return nil
}

func validateOpenReentrySource(snapshot domain.JournalSnapshot) error {
	if snapshot.Materialization.Mode == domain.MaterializationAdopted {
		source := snapshot.Recovery.Source
		if source.Kind != domain.SourceDecisionAdopted || source.Root != snapshot.Materialization.CanonicalPath || snapshot.Recovery.Cleanup.RemoveOwnedMaterialization {
			return ErrOpenSessionMismatch
		}
		return nil
	}
	if snapshot.Materialization.Mode != domain.MaterializationCreated {
		return ErrOpenSessionMismatch
	}
	source := snapshot.Recovery.Source
	if source.Kind != domain.SourceDecisionRemote || source.Lineage == nil || source.Generation == nil ||
		snapshot.OpenedGeneration == nil || snapshot.CurrentBase == nil || *source.Generation != *snapshot.OpenedGeneration ||
		!snapshot.Recovery.Cleanup.RemoveOwnedMaterialization {
		return ErrOpenSessionMismatch
	}
	hasLease := snapshot.Lease.Lease != nil
	if (snapshot.Mode == domain.SessionReadWrite && !hasLease) || (snapshot.Mode == domain.SessionReadOnly && hasLease) ||
		(snapshot.Mode != domain.SessionReadWrite && snapshot.Mode != domain.SessionReadOnly) {
		return ErrOpenSessionMismatch
	}
	return nil
}

func (o *Open) observeRemote(ctx context.Context, request OpenRequest, lineage domain.Lineage, log *openJournal) (*coordination.PointerRecord, *coordination.PointerRecord, error) {
	var branchPointer *coordination.PointerRecord
	pointer, err := o.deps.Pointers.Read(ctx, request.Capsule, lineage)
	if err == nil {
		return &pointer, nil, nil
	}
	if !errors.Is(err, ports.ErrNotFound) {
		return nil, nil, err
	}
	if lineage.IsMain() {
		return nil, nil, nil
	}
	source, err := o.deps.Pointers.Read(ctx, request.Capsule, request.SourceLineage)
	if err != nil {
		return nil, nil, err
	}
	branchPointer = &source
	return nil, branchPointer, nil
}

func (o *Open) acquireRemoteLease(ctx context.Context, request OpenRequest, lineage domain.Lineage, observed, source *coordination.PointerRecord, log *openJournal) (coordination.LeaseToken, error) {
	if o.deps.Leases == nil || log == nil {
		return coordination.LeaseToken{}, errors.New("remote lease manager is missing")
	}
	owner := coordination.LeaseOwner{SessionID: request.SessionID, Machine: request.Machine}
	if owner.SessionID == "" {
		return coordination.LeaseToken{}, errors.New("remote session id is missing")
	}
	now := o.deps.Clock.Now().UTC()
	input := openLeaseAcquisitionInput{Capsule: request.Capsule, Lineage: lineage, Owner: owner, Observed: observed, Source: source, BranchSource: source != nil, Now: now, LeaseTTL: request.LeaseTTL}
	if observed != nil {
		input.ObservedRevision = string(observed.Revision)
	}
	var token coordination.LeaseToken
	receipt := openLeaseReceipt{}
	err := log.phase(ctx, "RemoteLeaseAcquisition", input, &receipt, func() error {
		var err error
		if source != nil && !lineage.IsMain() && observed == source {
			token, err = o.deps.Leases.AcquireBranchFrom(ctx, request.Capsule, lineage, owner, *source, now, request.LeaseTTL)
		} else {
			token, err = o.deps.Leases.Acquire(ctx, request.Capsule, lineage, owner, observed, now, request.LeaseTTL)
		}
		if err != nil {
			return err
		}
		if _, err := validateOpenLeaseToken(input, token, now); err != nil {
			return err
		}
		log.snapshot.Lease = domain.LeaseRecord{Lease: &token.Lease, Revision: string(token.Revision)}
		receipt = leaseReceipt(input, token)
		return nil
	})
	if err != nil {
		return coordination.LeaseToken{}, err
	}
	return token, nil
}

func (o *Open) acquireLocalLease(ctx context.Context, request OpenRequest, lineage domain.Lineage, observed *coordination.PointerRecord, log *openJournal) (coordination.LeaseToken, error) {
	if o.deps.Leases == nil {
		return coordination.LeaseToken{}, errors.New("local writer lease dependency is incomplete")
	}
	owner := coordination.LeaseOwner{SessionID: request.SessionID, Machine: request.Machine}
	now := o.deps.Clock.Now().UTC()
	input := openLeaseAcquisitionInput{Capsule: request.Capsule, Lineage: lineage, Owner: owner, Observed: observed, Now: now, LeaseTTL: request.LeaseTTL}
	var token coordination.LeaseToken
	receipt := openLeaseReceipt{}
	if err := log.phase(ctx, "LocalLeaseAcquisition", input, &receipt, func() error {
		var err error
		token, err = o.deps.Leases.Acquire(ctx, request.Capsule, lineage, owner, observed, now, request.LeaseTTL)
		if err != nil {
			return err
		}
		if _, err := validateOpenLeaseToken(input, token, now); err != nil {
			return err
		}
		log.snapshot.Lease = domain.LeaseRecord{Lease: &token.Lease, Revision: string(token.Revision)}
		receipt = leaseReceipt(input, token)
		return nil
	}); err != nil {
		return coordination.LeaseToken{}, err
	}
	return token, nil
}

func (o *Open) hydrationHooks(log *openJournal) hydration.Hooks {
	return hydration.Hooks{
		Before: func(ctx context.Context, phase hydration.Phase, request hydration.Request) error {
			return log.begin(ctx, "Hydration"+string(phase), request)
		},
		After: func(ctx context.Context, phase hydration.Phase, result hydration.Result) error {
			return log.complete(ctx, "Hydration"+string(phase), result, func() {
				if result.Materialization.Mode == domain.MaterializationCreated {
					log.snapshot.Materialization = result.Materialization
					log.snapshot.Recovery.Cleanup.RemoveOwnedMaterialization = true
				}
			})
		},
	}
}

type openJournal struct {
	log      ports.Journal
	snapshot *domain.JournalSnapshot
	now      time.Time
	pending  map[string]ports.IntentRecord
}

func newOpenJournal(log ports.Journal, snapshot *domain.JournalSnapshot, now time.Time) *openJournal {
	return &openJournal{log: log, snapshot: snapshot, now: now, pending: make(map[string]ports.IntentRecord)}
}

func (j *openJournal) phase(ctx context.Context, transition string, input, output any, effect func() error) error {
	intent, err := j.ensureIntent(ctx, transition, input)
	if err != nil {
		return err
	}
	if err := effect(); err != nil {
		return err
	}
	return j.recordFact(ctx, intent, output, nil)
}

func (j *openJournal) begin(ctx context.Context, transition string, input any) error {
	_, err := j.ensureIntent(ctx, transition, input)
	return err
}

func (j *openJournal) adoptPending(intent ports.IntentRecord) error {
	if intent.SessionID != j.snapshot.SessionID || intent.ID != transitionID(j.snapshot.SessionID, intent.Transition) || intent.Transition == "" {
		return errors.New("pending hydration intent does not match the opening session")
	}
	if _, exists := j.pending[intent.ID]; exists {
		return errors.New("duplicate pending hydration intent")
	}
	j.pending[intent.ID] = intent
	return nil
}

func (j *openJournal) complete(ctx context.Context, transition string, output any, apply func()) error {
	intent, ok := j.pending[transitionID(j.snapshot.SessionID, transition)]
	if !ok {
		var err error
		intent, err = j.ensureIntent(ctx, transition, nil)
		if err != nil {
			return err
		}
	}
	apply()
	return j.recordFact(ctx, intent, output, nil)
}

func (j *openJournal) ensureIntent(ctx context.Context, transition string, input any) (ports.IntentRecord, error) {
	id := transitionID(j.snapshot.SessionID, transition)
	if intent, ok := j.pending[id]; ok {
		return intent, nil
	}
	body, err := json.Marshal(input)
	if err != nil {
		return ports.IntentRecord{}, err
	}
	intent := ports.IntentRecord{ID: id, SessionID: j.snapshot.SessionID, Transition: transition, Attempt: 1, Timestamp: j.now, Input: body}
	if err := j.log.RecordIntent(ctx, intent); err != nil {
		return ports.IntentRecord{}, err
	}
	j.pending[id] = intent
	return intent, nil
}

func (j *openJournal) recordFact(ctx context.Context, intent ports.IntentRecord, output any, pointer *ports.PointerCommit) error {
	body, err := json.Marshal(output)
	if err != nil {
		return err
	}
	fact := ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: j.now, Output: body, Pointer: pointer}
	if err := j.log.RecordFact(context.WithoutCancel(ctx), fact, *j.snapshot); err != nil {
		return err
	}
	delete(j.pending, intent.ID)
	return nil
}

func transitionID(sessionID, transition string) string {
	return sessionID + "-open-" + transition
}

func clonePointer(pointer *domain.LatestPointer) *domain.LatestPointer {
	if pointer == nil {
		return nil
	}
	copy := *pointer
	copy.Parent = cloneGeneration(pointer.Parent)
	return &copy
}

func sameOpenPointerRecord(left, right coordination.PointerRecord) bool {
	leftPointer, rightPointer := left.Pointer, right.Pointer
	leftParent, rightParent := leftPointer.Parent, rightPointer.Parent
	leftPointer.Parent, rightPointer.Parent = nil, nil
	return left.Revision == right.Revision && leftPointer == rightPointer && sameGeneration(leftParent, rightParent)
}

func newSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "session-" + hex.EncodeToString(value), nil
}

func validRoot(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) != string(filepath.Separator) && !strings.ContainsRune(path, '\x00')
}

func validateRemoteMaterialization(materialization domain.Materialization, plannedRoot, token string) error {
	if materialization.SchemaVersion != domain.SchemaVersion || materialization.Mode != domain.MaterializationCreated || !materialization.CleanupPermitted || materialization.OwnershipMarker != token || materialization.CanonicalPath != filepath.Clean(plannedRoot) || materialization.Device == 0 || materialization.Inode == 0 {
		return fmt.Errorf("hydration returned an incomplete or mismatched materialization: %w", hydration.ErrUnsafeMaterialization)
	}
	info, err := os.Lstat(materialization.CanonicalPath)
	if err != nil {
		return fmt.Errorf("inspect hydrated materialization: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("hydrated materialization is not a real directory: %w", hydration.ErrUnsafeMaterialization)
	}
	canonical, err := filepath.EvalSymlinks(materialization.CanonicalPath)
	if err != nil || canonical != materialization.CanonicalPath {
		return fmt.Errorf("hydrated materialization has unexplained symlinks: %w", hydration.ErrUnsafeMaterialization)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || uint64(stat.Dev) != materialization.Device || stat.Ino != materialization.Inode {
		return fmt.Errorf("hydrated materialization identity does not match: %w", hydration.ErrUnsafeMaterialization)
	}
	return nil
}

func strictXDGPaths(paths config.XDGPaths) bool {
	if !validRoot(paths.DataRoot) || !validRoot(paths.WorkRoot) || !validRoot(paths.StoreRoot) || !validRoot(paths.SessionRoot) || !validRoot(paths.CacheRoot) || !validAbsolutePath(paths.ConfigPath) {
		return false
	}
	if !withinOrEqual(paths.DataRoot, paths.WorkRoot) || !withinOrEqual(paths.DataRoot, paths.StoreRoot) || !withinOrEqual(paths.DataRoot, paths.SessionRoot) {
		return false
	}
	if filepath.Clean(paths.DataRoot) == filepath.Clean(paths.WorkRoot) || filepath.Clean(paths.DataRoot) == filepath.Clean(paths.StoreRoot) || filepath.Clean(paths.DataRoot) == filepath.Clean(paths.SessionRoot) {
		return false
	}
	if pathsOverlap(paths.WorkRoot, paths.StoreRoot) || pathsOverlap(paths.WorkRoot, paths.SessionRoot) || pathsOverlap(paths.StoreRoot, paths.SessionRoot) {
		return false
	}
	configRoot := filepath.Dir(filepath.Dir(filepath.Clean(paths.ConfigPath)))
	dataHome := filepath.Dir(filepath.Clean(paths.DataRoot))
	cacheHome := filepath.Dir(filepath.Clean(paths.CacheRoot))
	return !pathsOverlap(configRoot, dataHome) && !pathsOverlap(configRoot, cacheHome) && !pathsOverlap(dataHome, cacheHome)
}

func validAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && !strings.ContainsRune(path, '\x00')
}

func withinOrEqual(root, candidate string) bool {
	root, candidate = filepath.Clean(root), filepath.Clean(candidate)
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsOverlap(left, right string) bool {
	return withinOrEqual(left, right) || withinOrEqual(right, left)
}

func validateSessionIDForOpen(value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, "/\\\x00") {
		return fmt.Errorf("invalid open session id %q", value)
	}
	return nil
}

func ensureSessionRoot(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("session root is not a real directory")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return nil
}

func endpoint(port int) string {
	if port <= 0 {
		return ""
	}
	return "127.0.0.1:" + strconv.Itoa(port)
}

func safeJSON(value any) json.RawMessage {
	body, _ := json.Marshal(value)
	return body
}

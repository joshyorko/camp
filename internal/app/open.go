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
	"strconv"
	"strings"
	"syscall"
	"time"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/adapters/hydration"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	journalstore "github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
	"github.com/joshyorko/camp/internal/workspace"
)

var (
	ErrOpenDependencies    = errors.New("open dependencies are incomplete")
	ErrRecoveryRequired    = errors.New("session requires recovery before entry")
	ErrOpenReadOnlyLease   = errors.New("read-only session cannot reconcile a writer lease")
	ErrOpenSessionMismatch = errors.New("open request does not match the selected session")
	ErrOpenIDEUnsupported  = errors.New("IDE entry is not implemented")
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
	ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error)
	SSH(context.Context, devpodadapter.SSHOptions) (ports.Result, error)
}

type OpenTargetResolver interface {
	Resolve(context.Context, string, string) (target.Result, error)
}

type OpenDependencies struct {
	Journal     ports.Journal
	Paths       config.XDGPaths
	Backend     config.FileBackend
	Ownership   *capsule.Ownership
	Initializer OpenInitializer
	Pointers    OpenPointerReader
	Generations OpenGenerationReader
	Leases      OpenLeaseManager
	Hydrator    OpenHydrator
	DevPod      OpenDevPod
	Target      OpenTargetResolver
	Clock       ports.Clock
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

type Open struct {
	deps OpenDependencies
}

func NewOpen(deps OpenDependencies) *Open {
	return &Open{deps: deps}
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
			return o.reenter(ctx, selected, request)
		}
		if !errors.Is(err, ErrNoActiveSession) {
			return OpenResult{}, err
		}
	} else if snapshot, _, err := o.deps.Journal.Load(ctx, request.SessionID); err == nil && activeSessionState(snapshot.State) {
		return o.reenter(ctx, snapshot, request)
	}
	return o.create(ctx, request)
}

func (o *Open) Reconcile(ctx context.Context, sessionID string) (domain.JournalSnapshot, error) {
	if o == nil || o.deps.Journal == nil || o.deps.Pointers == nil || o.deps.Leases == nil || o.deps.Clock == nil || sessionID == "" {
		return domain.JournalSnapshot{}, errors.New("open reconciliation dependencies or session are incomplete")
	}
	return journalstore.Reconcile(ctx, o.deps.Journal, sessionID, map[string]journalstore.Observer{
		"RemoteLeaseAcquisition": o.observeRemoteLeaseAcquisition,
	})
}

func (o *Open) observeRemoteLeaseAcquisition(ctx context.Context, snapshot domain.JournalSnapshot, intent ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error) {
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
	} else {
		if input.Source != nil || input.Observed == nil || input.ObservedRevision != string(input.Observed.Revision) || input.Observed.Pointer.Lineage != input.Lineage {
			return ports.FactRecord{}, snapshot, errors.New("lease acquisition intent has an inconsistent observed pointer")
		}
		if err := o.deps.Pointers.Revalidate(ctx, *input.Observed); err != nil {
			return ports.FactRecord{}, snapshot, err
		}
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
	opened := openedFrom.Pointer.Generation
	next := snapshot
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
	if openedFrom == nil || openedFrom.Revision == "" {
		return nil, errors.New("lease acquisition intent lacks its opened pointer")
	}
	opened := openedFrom.Pointer.Generation
	expectedExpiry := input.Now.Add(input.LeaseTTL)
	if token.Lease.SchemaVersion != domain.SchemaVersion || token.Lease.Capsule != input.Capsule || token.Lease.Lineage != input.Lineage ||
		token.Lease.SessionID != input.Owner.SessionID || token.Lease.Machine != input.Owner.Machine || !sameGeneration(token.Lease.OpenedGeneration, &opened) ||
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
	if o == nil || o.deps.Journal == nil || o.deps.Clock == nil || o.deps.Ownership == nil || o.deps.Initializer == nil || o.deps.DevPod == nil || o.deps.Target == nil {
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
	backend := request.Backend
	if backend.Root == "" && backend.SanitizedURL == "" && backend.Fingerprint == "" {
		backend = o.deps.Backend
	}
	if backend.Root == "" || backend.SanitizedURL == "" || backend.Fingerprint == "" {
		return errors.New("open file backend is not a strict credential-free file URL")
	}
	resolved, err := config.ResolveFileBackend(backend.SanitizedURL)
	if err != nil || resolved.Root != backend.Root || resolved.Fingerprint != backend.Fingerprint {
		return errors.New("open file backend is not a strict credential-free file URL")
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
	backend := request.Backend
	if backend.Root == "" {
		backend = o.deps.Backend
	}
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
			Configuration: config.DurableConfiguration(runtime, backend, paths),
			Session:       domain.SessionArtifactPaths{Root: sessionRoot, RuntimeRoot: filepath.Join(sessionRoot, "runtime"), HaulPath: filepath.Join(sessionRoot, "generation.tar.zst"), RegistryOverlay: filepath.Join(sessionRoot, "registry")},
			Entry:         domain.EntryRequestRecord{Mode: request.EntryMode, Target: entryTarget},
			Cleanup:       domain.CleanupPolicy{WorkspaceAction: domain.WorkspaceCleanupDelete, RemoveSessionArtifacts: true},
		},
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
	var initialization capsule.Initialization
	var root string
	var openedGeneration *domain.GenerationRef
	var observedPointer *coordination.PointerRecord
	var fetchLineage = lineage
	if source.Kind == capsule.SourceAdopted {
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
	} else {
		if o.deps.Pointers == nil || o.deps.Generations == nil || o.deps.Hydrator == nil {
			return OpenResult{}, errors.New("remote open dependencies are incomplete")
		}
		pointer, sourcePointer, err := o.observeRemote(ctx, request, lineage, journal)
		if err != nil {
			return OpenResult{}, err
		}
		if pointer == nil && sourcePointer == nil {
			return OpenResult{}, errors.New("remote capsule has no committed generation")
		}
		observedPointer = pointer
		if sourcePointer != nil {
			observedPointer = sourcePointer
			fetchLineage = sourcePointer.Pointer.Lineage
		}
		opened := observedPointer.Pointer.Generation
		openedGeneration = &opened
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
		sourceLineage := fetchLineage
		snapshot.Recovery.Source = domain.SourceDecision{Kind: domain.SourceDecisionRemote, Lineage: &sourceLineage, Generation: cloneGeneration(&opened)}
		snapshot.Recovery.Cleanup.RemoveOwnedMaterialization = true
		if err := journal.phase(ctx, "MaterializationPlanned", safeJSON(struct {
			Token string `json:"token"`
			Stage string `json:"stage"`
			Final string `json:"final"`
		}{Token: token, Stage: stageRoot, Final: finalRoot}), nil, func() error { return nil }); err != nil {
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
	initialization, err = o.deps.Initializer.Initialize(ctx, root, request.Capsule)
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
	targetResult, err := o.deps.Target.Resolve(ctx, root, entryTarget)
	if err != nil {
		return OpenResult{}, err
	}
	if err := journal.phase(ctx, "TargetResolved", safeJSON(struct {
		Relative string `json:"relative"`
	}{Relative: targetResult.Relative}), targetResult, func() error { snapshot.Recovery.Entry.Target = targetResult.Relative; return nil }); err != nil {
		return OpenResult{}, err
	}
	workspaceID := workspace.DeterministicID(request.Capsule, request.Branch, root)
	checkpoint := ""
	if openedGeneration != nil {
		checkpoint = strconv.FormatUint(openedGeneration.Generation, 10)
	}
	upOptions := devpodadapter.UpOptions{WorkspacePath: root, WorkspaceID: workspaceID, Context: request.Context, Provider: request.Provider, DevcontainerPath: devcontainer.Path, CampEnvironment: &devpodadapter.CampEnvironment{Registry: endpoint(request.Runtime.RegistryPort), Fileserver: endpoint(request.Runtime.FileserverPort), Capsule: request.Capsule, Checkpoint: checkpoint}}
	snapshot.Workspace = domain.WorkspaceRecord{ID: workspaceID, Context: request.Context, Provider: request.Provider, LocalProvider: request.LocalProvider, LocalFolder: root, Target: targetResult.Relative, StagingRoot: root}
	var upResult ports.Result
	if err := journal.phase(ctx, "WorkspaceUp", safeJSON(struct {
		ID       string                        `json:"id"`
		Context  string                        `json:"context"`
		Provider string                        `json:"provider"`
		Env      devpodadapter.CampEnvironment `json:"environment"`
	}{ID: workspaceID, Context: request.Context, Provider: request.Provider, Env: *upOptions.CampEnvironment}), &upResult, func() error { var err error; upResult, err = o.deps.DevPod.Up(ctx, upOptions); return err }); err != nil {
		return OpenResult{}, err
	}
	var effectiveRoot string
	if err := journal.phase(ctx, "WorkspaceRootResolved", safeJSON(struct {
		ID string `json:"id"`
	}{ID: workspaceID}), &effectiveRoot, func() error {
		var err error
		effectiveRoot, err = o.deps.DevPod.ResolveWorkspaceFolderInContext(ctx, request.Context, workspaceID)
		if err == nil {
			snapshot.Workspace.EffectiveRoot = effectiveRoot
		}
		return err
	}); err != nil {
		return OpenResult{}, err
	}
	mapped, err := workspace.MapTarget(root, effectiveRoot, targetResult.Relative)
	if err != nil {
		return OpenResult{}, err
	}
	snapshot.Recovery.Configuration.DevcontainerPath = devcontainer.Path
	var entryResult ports.Result
	if request.EntryMode == domain.EntryTerminal {
		if err := journal.phase(ctx, "TerminalEntryDispatched", safeJSON(struct{ ID, Workdir string }{ID: workspaceID, Workdir: mapped}), &entryResult, func() error {
			var err error
			entryResult, err = o.deps.DevPod.SSH(ctx, devpodadapter.SSHOptions{WorkspaceID: workspaceID, Context: request.Context, Workdir: mapped, StartServices: true})
			return err
		}); err != nil {
			return OpenResult{}, err
		}
	}
	snapshot.State = domain.SessionOpen
	snapshot.Recovery.Entry.Mode = request.EntryMode
	if err := journal.phase(ctx, "SessionOpened", safeJSON(struct {
		ID string `json:"id"`
	}{ID: workspaceID}), nil, func() error { return nil }); err != nil {
		return OpenResult{}, err
	}
	return OpenResult{Snapshot: snapshot, Target: targetResult, MappedTarget: mapped, DevPodResult: entryResult, WorkspaceID: workspaceID, RecoveryCommand: "camp recover " + sessionID}, nil
}

func (o *Open) reenter(ctx context.Context, snapshot domain.JournalSnapshot, request OpenRequest) (OpenResult, error) {
	if (request.SessionID != "" && request.SessionID != snapshot.SessionID) || request.Capsule != snapshot.Capsule || (domain.Lineage{Branch: request.Branch}) != snapshot.Lineage ||
		(request.Mode != "" && request.Mode != snapshot.Mode) || (request.Context != "" && request.Context != snapshot.Workspace.Context) ||
		(request.Provider != "" && request.Provider != snapshot.Workspace.Provider) {
		return OpenResult{}, fmt.Errorf("session %q identity does not match the open request: %w", snapshot.SessionID, ErrOpenSessionMismatch)
	}
	if snapshot.State != domain.SessionOpen {
		return OpenResult{}, fmt.Errorf("session %q is %s: %w", snapshot.SessionID, snapshot.State, ErrRecoveryRequired)
	}
	if err := validateOpenReentrySource(snapshot); err != nil {
		return OpenResult{}, fmt.Errorf("session %q recovery source is inconsistent: %w", snapshot.SessionID, err)
	}
	if err := o.deps.Ownership.Revalidate(snapshot.Materialization); err != nil {
		return OpenResult{}, fmt.Errorf("revalidate session materialization: %w", err)
	}
	if snapshot.Materialization.Mode == domain.MaterializationCreated {
		expectedRoot := filepath.Join(o.deps.Ownership.MaterializationRoot(), snapshot.Capsule, snapshot.Lineage.Branch, snapshot.SessionID)
		if snapshot.Materialization.CanonicalPath != filepath.Clean(expectedRoot) {
			return OpenResult{}, fmt.Errorf("created session materialization path changed: %w", capsule.ErrOwnershipMismatch)
		}
	}
	if snapshot.Workspace.StagingRoot != snapshot.Materialization.CanonicalPath {
		return OpenResult{}, errors.New("session workspace staging root does not match its materialization")
	}
	if snapshot.Workspace.LocalFolder != snapshot.Materialization.CanonicalPath {
		return OpenResult{}, errors.New("session workspace local folder does not match its materialization")
	}
	if !filepath.IsAbs(snapshot.Workspace.EffectiveRoot) {
		return OpenResult{}, errors.New("session effective workspace root is missing or not absolute")
	}
	if snapshot.Workspace.ID != workspace.DeterministicID(snapshot.Capsule, snapshot.Lineage.Branch, snapshot.Materialization.CanonicalPath) {
		return OpenResult{}, errors.New("session workspace ID does not match its materialization")
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

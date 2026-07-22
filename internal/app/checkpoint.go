package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/checkpoint"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/images"
	"github.com/joshyorko/camp/internal/ports"
	registryadapter "github.com/joshyorko/camp/internal/registry"
	"github.com/joshyorko/camp/internal/workspace"
)

type checkpointBuilder interface {
	Build(context.Context, checkpoint.BuildRequest) (checkpoint.BuildResult, error)
}

type checkpointLeaseValidator interface {
	Revalidate(context.Context, coordination.LeaseToken, time.Time) error
}

type checkpointCapturer interface {
	Capture(context.Context, images.CaptureRequest) (domain.ImageInventory, error)
}

type checkpointRegistrySealer interface {
	Seal(context.Context, registryadapter.SnapshotRequest) (registryadapter.Snapshot, error)
}

type checkpointServingRefresher interface {
	Refresh(context.Context, ServingRefreshRequest) error
}

type CheckpointPipeline struct {
	Capturer  checkpointCapturer
	Sealer    checkpointRegistrySealer
	Refresher checkpointServingRefresher
}

type ServingRefreshRequest struct {
	SessionID            string               `json:"sessionId"`
	Generation           domain.GenerationRef `json:"generation"`
	HaulPath             string               `json:"haulPath"`
	RegistrySnapshotRoot string               `json:"registrySnapshotRoot"`
}

type CheckpointPublisher struct {
	journal     ports.Journal
	locks       ports.OperationTokenValidator
	leases      checkpointLeaseValidator
	mirror      *workspace.Selector
	pipeline    CheckpointPipeline
	builder     checkpointBuilder
	generations *coordination.GenerationRepository
	pointers    *coordination.PointerRepository
	clock       ports.Clock
}

type CheckpointTransports struct {
	Local  ports.WorkspaceTransport
	Remote ports.WorkspaceTransport
}

type CheckpointDisposition string

const (
	CheckpointDispositionPublished       CheckpointDisposition = "published"
	CheckpointDispositionSkippedReadOnly CheckpointDisposition = "skippedReadOnly"
)

type CheckpointResult struct {
	Disposition     CheckpointDisposition      `json:"disposition,omitempty"`
	Published       bool                       `json:"published"`
	Generation      domain.GenerationRef       `json:"generation,omitempty"`
	Pointer         coordination.PointerRecord `json:"pointer,omitempty"`
	RefreshError    string                     `json:"refreshError,omitempty"`
	RecoveryCommand string                     `json:"recoveryCommand,omitempty"`
}

func NewCheckpointPublisher(journal ports.Journal, locks ports.OperationTokenValidator, leases checkpointLeaseValidator, transports CheckpointTransports, pipeline CheckpointPipeline, builder checkpointBuilder, generations *coordination.GenerationRepository, pointers *coordination.PointerRepository, clock ports.Clock) *CheckpointPublisher {
	return &CheckpointPublisher{journal: journal, locks: locks, leases: leases, mirror: workspace.NewSelector(transports.Local, transports.Remote), pipeline: pipeline, builder: builder, generations: generations, pointers: pointers, clock: clock}
}

func (p *CheckpointPublisher) Publish(ctx context.Context, operation ports.OperationToken, sessionID string) (CheckpointResult, error) {
	if p == nil || p.journal == nil || p.locks == nil || p.leases == nil || p.mirror == nil || p.pipeline.Capturer == nil || p.pipeline.Sealer == nil || p.pipeline.Refresher == nil || p.builder == nil || p.generations == nil || p.pointers == nil || p.clock == nil {
		return CheckpointResult{}, errors.New("checkpoint publisher dependencies are incomplete")
	}
	if operation.Owner.SessionID != sessionID || sessionID == "" {
		return CheckpointResult{}, errors.New("checkpoint operation does not own the session")
	}
	if err := p.locks.Validate(ctx, operation); err != nil {
		return CheckpointResult{}, fmt.Errorf("validate checkpoint operation lock: %w", err)
	}
	snapshot, pending, err := p.journal.Load(ctx, sessionID)
	if err != nil {
		return CheckpointResult{}, err
	}
	if snapshot.Mode != domain.SessionReadWrite || snapshot.Lease.Lease == nil || snapshot.Lease.Revision == "" {
		return CheckpointResult{}, errors.New("checkpoint publication requires an active writer lease")
	}
	if err := validateCheckpointSnapshot(snapshot); err != nil {
		return CheckpointResult{}, err
	}
	parent := cloneGeneration(snapshot.CurrentBase)
	generation := uint64(1)
	if parent != nil {
		if parent.Generation == ^uint64(0) {
			return CheckpointResult{}, errors.New("checkpoint generation overflow")
		}
		generation = parent.Generation + 1
	}
	now := p.clock.Now().UTC()
	lease := coordination.LeaseToken{Lease: *snapshot.Lease.Lease, Revision: ports.Revision(snapshot.Lease.Revision)}
	if err := p.leases.Revalidate(ctx, lease, now); err != nil {
		return CheckpointResult{}, fmt.Errorf("validate checkpoint writer lease: %w", err)
	}
	if hasPendingTransition(pending, "ServingContentRefreshed") {
		return p.resumeServingRefresh(ctx, snapshot, pending, now)
	}
	attemptID := ""
	root := ""
	inventory := registryadapter.ExcludeInternalArtifacts(snapshot.Images)
	snapshot.Images = inventory
	cutRoot := snapshot.RegistryCutRoot
	var buildIntent ports.IntentRecord
	if len(pending) == 1 && pending[0].Intent.Transition == "RootSnapshotStable" {
		attemptID, root, buildIntent, err = preparePendingRootSnapshot(snapshot, pending[0].Intent, generation)
		if err != nil {
			return CheckpointResult{}, err
		}
	} else {
		prepared, err := p.prepareRegistrySeal(ctx, snapshot, pending, lease, generation, now)
		if err != nil {
			return CheckpointResult{}, err
		}
		snapshot = prepared.snapshot
		attemptID = prepared.attemptID
		root = prepared.root
		inventory = prepared.inventory
		runtime := prepared.runtime
		cutRoot = prepared.request.SnapshotRoot
		sealed, err := p.pipeline.Sealer.Seal(ctx, prepared.request)
		if err != nil {
			return CheckpointResult{}, err
		}
		if sealed.Root != cutRoot {
			return CheckpointResult{}, errors.New("registry sealer returned an unexpected cut root")
		}
		current, remaining, err := p.journal.Load(context.WithoutCancel(ctx), sessionID)
		if err != nil {
			return CheckpointResult{}, err
		}
		if len(remaining) != 1 || remaining[0].Intent.ID != prepared.intent.ID || remaining[0].Intent.Transition != prepared.intent.Transition {
			return CheckpointResult{}, errors.New("registry seal left unexpected pending reconciliation work")
		}
		stable := current
		stable.Services = snapshot.Services
		stable.UpdatedAt = snapshot.UpdatedAt
		if !reflect.DeepEqual(stable, snapshot) {
			return CheckpointResult{}, errors.New("registry seal changed unrelated durable session state")
		}
		snapshot = current
		inventory, err = registryadapter.MergeCatalog(inventory, runtime.authority, sealed.References, now)
		if err != nil {
			return CheckpointResult{}, err
		}
		snapshot.Images = inventory
		snapshot.RegistryCutRoot = sealed.Root
		if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(prepared.intent, now), snapshot); err != nil {
			return CheckpointResult{}, err
		}
		buildIntent = checkpointAttemptIntent(sessionID, attemptID, "RootSnapshotStable", 4, now, struct {
			Root       string `json:"root"`
			Generation uint64 `json:"generation"`
		}{Root: root, Generation: generation})
		if err := p.journal.RecordIntent(ctx, buildIntent); err != nil {
			return CheckpointResult{}, err
		}
	}

	tools := snapshot.Tools
	if tools == (domain.ToolVersions{}) && snapshot.CurrentPointer != nil {
		tools = snapshot.CurrentPointer.Tools
	}
	built, err := p.builder.Build(ctx, checkpoint.BuildRequest{
		Capsule: snapshot.Capsule, Root: root, Inventory: inventory, Lineage: snapshot.Lineage,
		Generation: generation, Parent: parent, SessionID: sessionID,
		CreatedAt: now, Tools: tools,
	})
	if err != nil {
		return CheckpointResult{}, err
	}
	result := CheckpointResult{Generation: built.Metadata.Generation, RecoveryCommand: "camp recover " + sessionID}
	snapshot.Checkpoint = domain.Checkpoint{State: domain.CheckpointVerified, Generation: cloneGeneration(&built.Metadata.Generation), LocalHaulPath: built.Artifact.Path, ObjectKey: built.Metadata.ObjectKey}
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(buildIntent, now), snapshot); err != nil {
		return result, err
	}

	uploadIntent := checkpointAttemptIntent(sessionID, attemptID, "GenerationUploaded", 5, now, struct {
		ObjectKey string `json:"objectKey"`
		SHA256    string `json:"sha256"`
		Size      int64  `json:"size"`
	}{ObjectKey: built.Metadata.ObjectKey, SHA256: built.Artifact.SHA256, Size: built.Artifact.Size})
	if err := p.journal.RecordIntent(ctx, uploadIntent); err != nil {
		return result, err
	}
	if _, err := p.generations.PutAndVerify(ctx, built.Metadata, restartableFile(built.Artifact.Path)); err != nil {
		return result, err
	}
	snapshot.Checkpoint.State = domain.CheckpointUploaded
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(uploadIntent, now), snapshot); err != nil {
		return result, err
	}

	next := domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion, Capsule: snapshot.Capsule, Lineage: snapshot.Lineage,
		Generation: built.Metadata.Generation, Parent: parent, ObjectKey: built.Metadata.ObjectKey,
		Size: built.Metadata.Size, CreatedAt: built.Metadata.CreatedAt, Tools: built.Metadata.Tools, SessionID: sessionID,
	}
	pointerIntent := checkpointAttemptIntent(sessionID, attemptID, "PointerCommitted", 6, now, next)
	if err := p.journal.RecordIntent(ctx, pointerIntent); err != nil {
		return result, err
	}
	var published coordination.PointerRecord
	if snapshot.CurrentPointer == nil {
		published, err = p.pointers.Create(ctx, next)
	} else {
		published, err = p.pointers.CompareAndSwap(ctx, coordination.PointerRecord{Pointer: *snapshot.CurrentPointer, Revision: ports.Revision(snapshot.ExpectedPointerRevision)}, next)
	}
	if err != nil {
		return result, err
	}
	result.Published = true
	result.Disposition = CheckpointDispositionPublished
	result.Pointer = published
	result.Generation = published.Pointer.Generation
	snapshot.Checkpoint.State = domain.CheckpointPublished
	snapshot.Checkpoint.PublicationSucceeded = true
	fact := checkpointFact(pointerIntent, now)
	fact.Pointer = &ports.PointerCommit{Pointer: published.Pointer, Revision: string(published.Revision)}
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), fact, snapshot); err != nil {
		return result, err
	}
	committedGeneration := published.Pointer.Generation
	committedPointer := published.Pointer
	snapshot.CurrentBase = &committedGeneration
	snapshot.CurrentPointer = &committedPointer
	snapshot.ExpectedPointerRevision = string(published.Revision)
	refreshRequest := ServingRefreshRequest{
		SessionID: sessionID, Generation: result.Generation, HaulPath: built.Artifact.Path,
		RegistrySnapshotRoot: cutRoot,
	}
	refreshIntent := checkpointAttemptIntent(sessionID, attemptID, "ServingContentRefreshed", 7, now, refreshRequest)
	if err := p.journal.RecordIntent(context.WithoutCancel(ctx), refreshIntent); err != nil {
		result.RefreshError = err.Error()
		return result, nil
	}
	if err := p.pipeline.Refresher.Refresh(context.WithoutCancel(ctx), refreshRequest); err != nil {
		result.RefreshError = err.Error()
		return result, nil
	}
	refreshed, pending, err := p.journal.Load(context.WithoutCancel(ctx), sessionID)
	if err != nil {
		result.RefreshError = err.Error()
		return result, nil
	}
	if !containsPendingIntent(pending, refreshIntent.ID) {
		result.RefreshError = "serving refresh left unexpected pending reconciliation work"
		return result, nil
	}
	snapshot = refreshed
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(refreshIntent, now), snapshot); err != nil {
		result.RefreshError = err.Error()
		return result, nil
	}
	return result, nil
}

func (p *CheckpointPublisher) resumeServingRefresh(ctx context.Context, snapshot domain.JournalSnapshot, pending []ports.PendingIntent, now time.Time) (CheckpointResult, error) {
	result := CheckpointResult{Disposition: CheckpointDispositionPublished, Published: true, RecoveryCommand: "camp recover " + snapshot.SessionID}
	if snapshot.Checkpoint.State != domain.CheckpointPublished || !snapshot.Checkpoint.PublicationSucceeded || snapshot.Checkpoint.Generation == nil || snapshot.CurrentPointer == nil || snapshot.ExpectedPointerRevision == "" {
		return CheckpointResult{}, errors.New("pending serving refresh lacks a published checkpoint")
	}
	result.Generation = *snapshot.Checkpoint.Generation
	result.Pointer = coordination.PointerRecord{Pointer: *snapshot.CurrentPointer, Revision: ports.Revision(snapshot.ExpectedPointerRevision)}
	var refreshIntent ports.IntentRecord
	var request ServingRefreshRequest
	for _, item := range pending {
		intent := item.Intent
		if intent.Transition == "ServingContentRefreshed" {
			if refreshIntent.ID != "" || json.Unmarshal(intent.Input, &request) != nil {
				return CheckpointResult{}, errors.New("pending serving refresh is ambiguous")
			}
			refreshIntent = intent
			continue
		}
		if intent.Transition != "ServiceStart" || intent.SessionID != snapshot.SessionID || (intent.ID != "registry-"+snapshot.SessionID+"-generation-"+strconv.FormatUint(result.Generation.Generation, 10)+"-registry" && intent.ID != "fileserver-"+snapshot.SessionID+"-generation-"+strconv.FormatUint(result.Generation.Generation, 10)+"-fileserver") {
			return CheckpointResult{}, errors.New("pending serving refresh has unrelated reconciliation work")
		}
	}
	if refreshIntent.ID == "" || refreshIntent.SessionID != snapshot.SessionID || request.SessionID != snapshot.SessionID || request.Generation != result.Generation || request.HaulPath != snapshot.Checkpoint.LocalHaulPath || request.RegistrySnapshotRoot != snapshot.RegistryCutRoot {
		return CheckpointResult{}, errors.New("pending serving refresh request drifted from published checkpoint")
	}
	if err := p.pipeline.Refresher.Refresh(context.WithoutCancel(ctx), request); err != nil {
		result.RefreshError = err.Error()
		return result, nil
	}
	refreshed, remaining, err := p.journal.Load(context.WithoutCancel(ctx), snapshot.SessionID)
	if err != nil {
		result.RefreshError = err.Error()
		return result, nil
	}
	if len(remaining) != 1 || remaining[0].Intent.ID != refreshIntent.ID {
		result.RefreshError = "serving refresh left unexpected pending reconciliation work"
		return result, nil
	}
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(refreshIntent, now), refreshed); err != nil {
		result.RefreshError = err.Error()
	}
	return result, nil
}

func hasPendingTransition(pending []ports.PendingIntent, transition string) bool {
	for _, item := range pending {
		if item.Intent.Transition == transition {
			return true
		}
	}
	return false
}

func preparePendingRootSnapshot(snapshot domain.JournalSnapshot, intent ports.IntentRecord, generation uint64) (string, string, ports.IntentRecord, error) {
	attemptID := snapshot.Workspace.Mirror.AttemptID
	root := snapshot.Workspace.Mirror.Root
	if attemptID == "" || snapshot.Workspace.Mirror.State != domain.MirrorCompleted || root == "" || intent.ID != attemptID+"-4" || intent.SessionID != snapshot.SessionID {
		return "", "", ports.IntentRecord{}, errors.New("pending root snapshot does not match the durable checkpoint attempt")
	}
	var request struct {
		Root       string `json:"root"`
		Generation uint64 `json:"generation"`
	}
	if err := json.Unmarshal(intent.Input, &request); err != nil || request.Root != root || request.Generation != generation {
		return "", "", ports.IntentRecord{}, errors.New("pending root snapshot request drifted from durable session state")
	}
	expectedCutRoot := filepath.Join(root, ".camp", "build", "registry-cut-"+strconv.FormatUint(generation, 10))
	if snapshot.RegistryCutRoot != expectedCutRoot {
		return "", "", ports.IntentRecord{}, errors.New("pending root snapshot lacks its sealed registry cut")
	}
	return attemptID, root, intent, nil
}

type preparedRegistrySeal struct {
	snapshot  domain.JournalSnapshot
	attemptID string
	root      string
	inventory domain.ImageInventory
	runtime   checkpointRegistry
	request   registryadapter.SnapshotRequest
	intent    ports.IntentRecord
}

func (p *CheckpointPublisher) prepareRegistrySeal(ctx context.Context, snapshot domain.JournalSnapshot, pending []ports.PendingIntent, lease coordination.LeaseToken, generation uint64, now time.Time) (preparedRegistrySeal, error) {
	runtime, err := checkpointRegistryRuntime(snapshot)
	if err != nil {
		return preparedRegistrySeal{}, err
	}
	if len(pending) != 0 {
		sealIndex := -1
		for index, item := range pending {
			if item.Intent.Transition == "RegistrySnapshotSealed" {
				if sealIndex >= 0 {
					return preparedRegistrySeal{}, errors.New("checkpoint has multiple pending registry seals")
				}
				sealIndex = index
			}
		}
		if sealIndex < 0 || len(pending) > 3 {
			return preparedRegistrySeal{}, errors.New("checkpoint has pending reconciliation work")
		}
		intent := pending[sealIndex].Intent
		attemptID := snapshot.Workspace.Mirror.AttemptID
		root := snapshot.Workspace.Mirror.Root
		if attemptID == "" || snapshot.Workspace.Mirror.State != domain.MirrorCompleted || root == "" || intent.ID != attemptID+"-3" || intent.SessionID != snapshot.SessionID {
			return preparedRegistrySeal{}, errors.New("pending registry seal does not match the durable checkpoint attempt")
		}
		var request registryadapter.SnapshotRequest
		if err := json.Unmarshal(intent.Input, &request); err != nil {
			return preparedRegistrySeal{}, errors.New("pending registry seal request is invalid")
		}
		expected := registryadapter.SnapshotRequest{
			SessionID: snapshot.SessionID, OverlayRoot: runtime.overlay,
			SnapshotRoot:    filepath.Join(root, ".camp", "build", "registry-cut-"+strconv.FormatUint(generation, 10)),
			CatalogEndpoint: runtime.endpoint, RegistryLaunchToken: runtime.launchToken,
		}
		if request != expected {
			return preparedRegistrySeal{}, errors.New("pending registry seal request drifted from durable session state")
		}
		restartFound := false
		for index, item := range pending {
			if index == sealIndex {
				continue
			}
			child := item.Intent
			restartID := hauleradapter.RegistryServiceName + "-" + request.RegistryLaunchToken + "-restart"
			startID := hauleradapter.RegistryServiceName + "-" + request.RegistryLaunchToken
			if child.SessionID != snapshot.SessionID || !((child.Transition == "ServiceRestart" && child.ID == restartID) || (child.Transition == "ServiceStart" && child.ID == startID)) {
				return preparedRegistrySeal{}, errors.New("checkpoint has unrelated pending reconciliation work")
			}
			restartFound = restartFound || child.Transition == "ServiceRestart"
		}
		if len(pending) > 1 && !restartFound {
			return preparedRegistrySeal{}, errors.New("checkpoint service start lacks its pending restart parent")
		}
		return preparedRegistrySeal{snapshot: snapshot, attemptID: attemptID, root: root, inventory: snapshot.Images, runtime: runtime, request: request, intent: intent}, nil
	}
	if snapshot.Workspace.Mirror.LogicalAttempt == ^uint64(0) {
		return preparedRegistrySeal{}, errors.New("checkpoint mirror attempt overflow")
	}
	logicalAttempt := snapshot.Workspace.Mirror.LogicalAttempt + 1
	attemptID := snapshot.SessionID + "-checkpoint-" + strconv.FormatUint(logicalAttempt, 10)
	mirrorRequest := ports.MirrorRequest{
		Provider: snapshot.Workspace.Provider, LocalProvider: snapshot.Workspace.LocalProvider,
		StagingRoot: snapshot.Workspace.StagingRoot, WorkspaceLocalFolder: snapshot.Workspace.LocalFolder,
		WorkspaceID: snapshot.Workspace.ID, Context: snapshot.Workspace.Context, AttemptID: attemptID,
	}
	if err := p.mirror.Validate(mirrorRequest); err != nil {
		return preparedRegistrySeal{}, err
	}
	mirrorIntent := checkpointAttemptIntent(snapshot.SessionID, attemptID, "WorkspaceMirrored", 1, now, mirrorRequest)
	if err := p.journal.RecordIntent(ctx, mirrorIntent); err != nil {
		return preparedRegistrySeal{}, err
	}
	mirrored, err := p.mirror.ReturnToStaging(ctx, mirrorRequest)
	if err != nil {
		var unknown *workspace.MirrorOutcomeUnknown
		if errors.As(err, &unknown) {
			if !validRemoteMirrorResult(unknown.Result, mirrorRequest.AttemptID) {
				return preparedRegistrySeal{}, errors.Join(err, errors.New("ambiguous workspace mirror returned an invalid attempt identity"))
			}
			snapshot.Workspace.Mirror = mirrorAttemptRecord(logicalAttempt, unknown.Result, domain.MirrorAmbiguous)
			if factErr := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(mirrorIntent, now), snapshot); factErr != nil {
				return preparedRegistrySeal{}, errors.Join(err, factErr)
			}
		}
		return preparedRegistrySeal{}, err
	}
	if mirrored.Root == "" {
		return preparedRegistrySeal{}, errors.New("workspace mirror returned an empty staging root")
	}
	if snapshot.Workspace.LocalProvider {
		if mirrored.Mode != ports.MirrorLocalNoop || mirrored.Root != snapshot.Workspace.StagingRoot || mirrored.AttemptID != mirrorRequest.AttemptID {
			return preparedRegistrySeal{}, errors.New("local workspace mirror did not preserve the staging root")
		}
	} else if !validRemoteMirrorResult(mirrored, mirrorRequest.AttemptID) {
		return preparedRegistrySeal{}, errors.New("remote workspace mirror did not return a DevPod SSH staging root")
	}
	snapshot.Workspace.Mirror = mirrorAttemptRecord(logicalAttempt, mirrored, domain.MirrorCompleted)
	if err := p.leases.Revalidate(ctx, lease, now); err != nil {
		return preparedRegistrySeal{}, fmt.Errorf("revalidate checkpoint writer lease after workspace mirror: %w", err)
	}
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(mirrorIntent, now), snapshot); err != nil {
		return preparedRegistrySeal{}, err
	}
	captureRequest := images.CaptureRequest{
		Scope: images.EngineScope{Context: snapshot.Workspace.Context, WorkspaceID: snapshot.Workspace.ID}, Capsule: snapshot.Capsule,
		RegistryAuthority: runtime.authority, RegistryEndpoint: runtime.endpoint, Previous: snapshot.Images,
	}
	captureIntent := checkpointAttemptIntent(snapshot.SessionID, attemptID, "WorkspaceImagesInventoried", 2, now, captureRequest)
	if err := p.journal.RecordIntent(ctx, captureIntent); err != nil {
		return preparedRegistrySeal{}, err
	}
	inventory, err := p.pipeline.Capturer.Capture(ctx, captureRequest)
	if err != nil {
		return preparedRegistrySeal{}, err
	}
	snapshot.Images = inventory
	if err := p.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(captureIntent, now), snapshot); err != nil {
		return preparedRegistrySeal{}, err
	}
	request := registryadapter.SnapshotRequest{
		SessionID: snapshot.SessionID, OverlayRoot: runtime.overlay,
		SnapshotRoot:    filepath.Join(mirrored.Root, ".camp", "build", "registry-cut-"+strconv.FormatUint(generation, 10)),
		CatalogEndpoint: runtime.endpoint, RegistryLaunchToken: runtime.launchToken,
	}
	intent := checkpointAttemptIntent(snapshot.SessionID, attemptID, "RegistrySnapshotSealed", 3, now, request)
	if err := p.journal.RecordIntent(ctx, intent); err != nil {
		return preparedRegistrySeal{}, err
	}
	return preparedRegistrySeal{snapshot: snapshot, attemptID: attemptID, root: mirrored.Root, inventory: inventory, runtime: runtime, request: request, intent: intent}, nil
}

func checkpointIntent(sessionID, transition string, sequence int, timestamp time.Time, input any) ports.IntentRecord {
	body, _ := json.Marshal(input)
	return ports.IntentRecord{ID: sessionID + "-checkpoint-" + strconv.Itoa(sequence), SessionID: sessionID, Transition: transition, Attempt: 1, Timestamp: timestamp, Input: body}
}

func checkpointAttemptIntent(sessionID, attemptID, transition string, sequence int, timestamp time.Time, input any) ports.IntentRecord {
	body, _ := json.Marshal(input)
	return ports.IntentRecord{ID: attemptID + "-" + strconv.Itoa(sequence), SessionID: sessionID, Transition: transition, Attempt: 1, Timestamp: timestamp, Input: body}
}

func checkpointFact(intent ports.IntentRecord, timestamp time.Time) ports.FactRecord {
	return ports.FactRecord{IntentID: intent.ID, SessionID: intent.SessionID, Transition: intent.Transition, Timestamp: timestamp}
}

func cloneGeneration(reference *domain.GenerationRef) *domain.GenerationRef {
	if reference == nil {
		return nil
	}
	copy := *reference
	return &copy
}

func mirrorAttemptRecord(logicalAttempt uint64, result ports.MirrorResult, state domain.MirrorState) domain.MirrorAttemptRecord {
	return domain.MirrorAttemptRecord{
		LogicalAttempt: logicalAttempt, AttemptID: result.AttemptID, State: state, Root: result.Root, RemoteRoot: result.RemoteRoot,
		Method: result.Method, Exclusions: append([]string(nil), result.Exclusions...),
	}
}

func validRemoteMirrorResult(result ports.MirrorResult, attemptID string) bool {
	return result.Mode == workspace.MirrorDevPodSSH && result.Root != "" &&
		(result.AttemptID == attemptID+"-rsync" || result.AttemptID == attemptID+"-tar")
}

func validateCheckpointSnapshot(snapshot domain.JournalSnapshot) error {
	lease := snapshot.Lease.Lease
	if snapshot.SchemaVersion != domain.SchemaVersion || snapshot.SessionID == "" || snapshot.Capsule == "" || snapshot.Lineage.Branch == "" {
		return errors.New("checkpoint snapshot identity is incomplete")
	}
	if lease.SchemaVersion != domain.SchemaVersion || lease.SessionID != snapshot.SessionID || lease.Capsule != snapshot.Capsule || lease.Lineage != snapshot.Lineage || lease.Machine == "" || lease.CreatedAt.IsZero() || lease.HeartbeatAt.IsZero() || lease.ExpiresAt.IsZero() || !sameGeneration(lease.OpenedGeneration, snapshot.OpenedGeneration) {
		return errors.New("checkpoint writer lease does not match the session")
	}
	if snapshot.CurrentPointer == nil {
		if snapshot.ExpectedPointerRevision != "" || (snapshot.Lineage.IsMain() && snapshot.CurrentBase != nil) {
			return errors.New("checkpoint pointer baseline is inconsistent")
		}
		return nil
	}
	if snapshot.CurrentBase == nil || snapshot.ExpectedPointerRevision == "" || snapshot.CurrentPointer.Capsule != snapshot.Capsule || snapshot.CurrentPointer.Lineage != snapshot.Lineage || snapshot.CurrentPointer.Generation != *snapshot.CurrentBase {
		return errors.New("checkpoint pointer baseline is inconsistent")
	}
	return nil
}

func sameGeneration(left, right *domain.GenerationRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

type checkpointRegistry struct {
	authority   string
	endpoint    string
	overlay     string
	launchToken string
}

func checkpointRegistryRuntime(snapshot domain.JournalSnapshot) (checkpointRegistry, error) {
	if snapshot.Workspace.Context == "" || snapshot.Workspace.ID == "" {
		return checkpointRegistry{}, errors.New("checkpoint workspace execution scope is incomplete")
	}
	for _, service := range snapshot.Services {
		if service.Name != "registry" {
			continue
		}
		if service.ObservedState != domain.RuntimeObservedReady || service.DesiredState != domain.RuntimeDesiredRunning || service.Mapping.HostAddress != "127.0.0.1" || service.Mapping.HostPort < 1 {
			return checkpointRegistry{}, errors.New("checkpoint registry is not a committed loopback service")
		}
		overlay := ""
		for index, argument := range service.Child.Argv {
			if argument == "--directory" && index+1 < len(service.Child.Argv) {
				overlay = service.Child.Argv[index+1]
				break
			}
		}
		if !filepath.IsAbs(overlay) || strings.ContainsRune(overlay, '\x00') {
			return checkpointRegistry{}, errors.New("checkpoint registry overlay is not durable")
		}
		authority := net.JoinHostPort(service.Mapping.HostAddress, strconv.Itoa(service.Mapping.HostPort))
		return checkpointRegistry{authority: authority, endpoint: "http://" + authority, overlay: overlay, launchToken: service.LaunchToken}, nil
	}
	return checkpointRegistry{}, errors.New("checkpoint registry service is missing")
}

type restartableFile string

func (path restartableFile) Open() (io.ReadCloser, error) {
	return os.Open(string(path))
}

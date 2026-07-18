package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	imageops "github.com/joshyorko/camp/internal/images"
	"github.com/joshyorko/camp/internal/ports"
)

type imageCapturer interface {
	Capture(context.Context, imageops.CaptureRequest) (domain.ImageInventory, error)
}

type imageRestorer interface {
	Restore(context.Context, imageops.RestoreRequest) (imageops.RestoreResult, error)
}

type imageServiceObserver interface {
	Observe(context.Context, domain.ServiceUnitRecord) (supervisor.UnitObservation, error)
}

type ImageOperations struct {
	journal  ports.Journal
	locks    operationLocker
	guard    recoveryGuard
	services imageServiceObserver
	capturer imageCapturer
	restorer imageRestorer
	clock    ports.Clock
}

type ImageReadModel struct {
	EngineImageID          string                 `json:"engineImageId,omitempty"`
	OriginalTags           []string               `json:"originalTags"`
	OriginalRepoDigests    []string               `json:"originalRepoDigests,omitempty"`
	CapturedReference      string                 `json:"capturedReference"`
	CapturedManifestDigest string                 `json:"capturedManifestDigest"`
	Platform               ImagePlatformReadModel `json:"platform"`
	Source                 string                 `json:"source"`
	CreatedAt              time.Time              `json:"createdAt"`
}

type ImagePlatformReadModel struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant,omitempty"`
}

type ImageInventoryReadModel struct {
	SessionID      string           `json:"sessionId"`
	Capsule        string           `json:"capsule"`
	Branch         string           `json:"branch"`
	BaseGeneration uint64           `json:"baseGeneration,omitempty"`
	BaseDigest     string           `json:"baseDigest,omitempty"`
	GeneratedAt    time.Time        `json:"generatedAt"`
	Images         []ImageReadModel `json:"images"`
}

type ImageRestoreReadModel struct {
	SessionID string `json:"sessionId"`
	Capsule   string `json:"capsule"`
	Branch    string `json:"branch"`
	Restored  int    `json:"restored"`
	Tags      int    `json:"tags"`
}

func NewImageOperations(journal ports.Journal, locks operationLocker, guard recoveryGuard, services imageServiceObserver, capturer imageCapturer, restorer imageRestorer, clock ports.Clock) *ImageOperations {
	return &ImageOperations{journal: journal, locks: locks, guard: guard, services: services, capturer: capturer, restorer: restorer, clock: clock}
}

func (u *ImageOperations) List(ctx context.Context, selector SessionSelector) (ImageInventoryReadModel, error) {
	if u == nil || u.journal == nil {
		return ImageInventoryReadModel{}, errors.New("image list journal is nil")
	}
	selected, err := SelectSession(ctx, u.journal, selector, SelectionHistory)
	if err != nil {
		return ImageInventoryReadModel{}, err
	}
	loaded, _, err := u.journal.Load(ctx, selected.SessionID)
	if err != nil {
		return ImageInventoryReadModel{}, err
	}
	if !sameRecoveryIdentity(selected, loaded) {
		return ImageInventoryReadModel{}, ErrRecoveryIdentityChanged
	}
	return buildImageInventoryReadModel(loaded), nil
}

func (u *ImageOperations) Capture(ctx context.Context, selector SessionSelector, excludeTags []string) (result ImageInventoryReadModel, resultErr error) {
	selected, token, loaded, runtime, pendingIntent, err := u.prepareMutation(ctx, selector, "images-capture", "ImagesCaptured")
	if err != nil {
		return ImageInventoryReadModel{}, err
	}
	defer u.release(ctx, token, &resultErr)
	intent := imageIntent(selected.SessionID, "ImagesCaptured", u.clock.Now().UTC())
	if pendingIntent != nil {
		intent = *pendingIntent
	} else if err := u.journal.RecordIntent(ctx, intent); err != nil {
		return ImageInventoryReadModel{}, err
	}
	inventory, err := u.capturer.Capture(ctx, imageops.CaptureRequest{
		Scope:   imageops.EngineScope{Context: loaded.Workspace.Context, WorkspaceID: loaded.Workspace.ID},
		Capsule: loaded.Capsule, RegistryAuthority: runtime.authority, RegistryEndpoint: runtime.endpoint,
		ExcludeTags: append([]string(nil), excludeTags...), Previous: loaded.Images,
	})
	if err != nil {
		return ImageInventoryReadModel{}, err
	}
	if err := validateCapturedInventory(inventory); err != nil {
		return ImageInventoryReadModel{}, err
	}
	loaded.Images = inventory
	if err := u.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(intent, u.clock.Now().UTC()), loaded); err != nil {
		return ImageInventoryReadModel{}, err
	}
	return buildImageInventoryReadModel(loaded), nil
}

func (u *ImageOperations) Restore(ctx context.Context, selector SessionSelector) (result ImageRestoreReadModel, resultErr error) {
	selected, token, loaded, runtime, pendingIntent, err := u.prepareMutation(ctx, selector, "images-restore", "ImagesRestored")
	if err != nil {
		return ImageRestoreReadModel{}, err
	}
	defer u.release(ctx, token, &resultErr)
	if err := validateCapturedInventory(loaded.Images); err != nil {
		return ImageRestoreReadModel{}, err
	}
	intent := imageIntent(selected.SessionID, "ImagesRestored", u.clock.Now().UTC())
	if pendingIntent != nil {
		intent = *pendingIntent
	} else if err := u.journal.RecordIntent(ctx, intent); err != nil {
		return ImageRestoreReadModel{}, err
	}
	restored, err := u.restorer.Restore(ctx, imageops.RestoreRequest{
		Scope:             imageops.EngineScope{Context: loaded.Workspace.Context, WorkspaceID: loaded.Workspace.ID},
		RegistryAuthority: runtime.authority, RegistryEndpoint: runtime.endpoint, Inventory: loaded.Images,
	})
	if err != nil {
		return ImageRestoreReadModel{}, err
	}
	if err := u.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(intent, u.clock.Now().UTC()), loaded); err != nil {
		return ImageRestoreReadModel{}, err
	}
	return ImageRestoreReadModel{SessionID: loaded.SessionID, Capsule: loaded.Capsule, Branch: loaded.Lineage.Branch, Restored: restored.Restored, Tags: restored.Tags}, nil
}

func (u *ImageOperations) prepareMutation(ctx context.Context, selector SessionSelector, operation, transition string) (domain.JournalSnapshot, ports.OperationToken, domain.JournalSnapshot, checkpointRegistry, *ports.IntentRecord, error) {
	if u == nil || u.journal == nil || u.locks == nil || u.guard == nil || u.services == nil || u.capturer == nil || u.restorer == nil || u.clock == nil {
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, errors.New("image operation dependencies are incomplete")
	}
	selected, err := SelectSession(ctx, u.journal, selector, SelectionActiveMutation)
	if err != nil {
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, err
	}
	token, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: selected.SessionID, Operation: operation})
	if err != nil {
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, err
	}
	loaded, pending, err := u.journal.Load(ctx, selected.SessionID)
	if err != nil {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, err
	}
	if !sameRecoveryIdentity(selected, loaded) {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, errors.New("image operation session changed")
	}
	var matching *ports.IntentRecord
	if len(pending) > 0 {
		if len(pending) != 1 || pending[0].Intent.SessionID != loaded.SessionID || pending[0].Intent.Transition != transition {
			_ = u.locks.Release(context.WithoutCancel(ctx), token)
			return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, errors.New("image operation has unrelated pending recovery")
		}
		copy := pending[0].Intent
		matching = &copy
	}
	if err := u.guard.Revalidate(ctx, loaded, pending); err != nil {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, err
	}
	runtime, err := checkpointRegistryRuntime(loaded)
	if err != nil {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, err
	}
	registry, ok := recordedServiceForApp(loaded, "registry")
	if !ok {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, errors.New("image operation registry service is missing")
	}
	observation, err := u.services.Observe(ctx, registry)
	if err != nil || observation.State != supervisor.UnitLive {
		_ = u.locks.Release(context.WithoutCancel(ctx), token)
		return domain.JournalSnapshot{}, ports.OperationToken{}, domain.JournalSnapshot{}, checkpointRegistry{}, nil, errors.Join(err, errors.New("image operation registry is not live"))
	}
	return selected, token, loaded, runtime, matching, nil
}

func (u *ImageOperations) release(ctx context.Context, token ports.OperationToken, resultErr *error) {
	if err := u.locks.Release(context.WithoutCancel(ctx), token); err != nil {
		*resultErr = errors.Join(*resultErr, err)
	}
}

func imageIntent(sessionID, transition string, timestamp time.Time) ports.IntentRecord {
	return ports.IntentRecord{ID: sessionID + "-" + transition + "-" + strconv.FormatInt(timestamp.UnixNano(), 10), SessionID: sessionID, Transition: transition, Attempt: 1, Timestamp: timestamp}
}

func validateCapturedInventory(inventory domain.ImageInventory) error {
	if inventory.SchemaVersion != domain.SchemaVersion {
		return errors.New("captured image inventory schema is unsupported")
	}
	for _, image := range inventory.Images {
		if image.CapturedReference == "" || image.CapturedManifestDigest == "" {
			return fmt.Errorf("captured image inventory contains incomplete content")
		}
	}
	return nil
}

func buildImageInventoryReadModel(snapshot domain.JournalSnapshot) ImageInventoryReadModel {
	result := ImageInventoryReadModel{SessionID: snapshot.SessionID, Capsule: snapshot.Capsule, Branch: snapshot.Lineage.Branch, GeneratedAt: snapshot.Images.GeneratedAt, Images: imageReadModels(snapshot.Images.Images)}
	if snapshot.CurrentBase != nil {
		result.BaseGeneration = snapshot.CurrentBase.Generation
		result.BaseDigest = snapshot.CurrentBase.ArchiveSHA256
	}
	return result
}

func imageReadModels(images []domain.Image) []ImageReadModel {
	result := make([]ImageReadModel, 0, len(images))
	for _, image := range images {
		tags := append([]string(nil), image.OriginalTags...)
		digests := append([]string(nil), image.OriginalRepoDigests...)
		sort.Strings(tags)
		sort.Strings(digests)
		result = append(result, ImageReadModel{EngineImageID: image.EngineImageID, OriginalTags: tags, OriginalRepoDigests: digests, CapturedReference: image.CapturedReference, CapturedManifestDigest: image.CapturedManifestDigest, Platform: ImagePlatformReadModel{OS: image.Platform.OS, Architecture: image.Platform.Architecture, Variant: image.Platform.Variant}, Source: string(image.Source), CreatedAt: image.CreatedAt})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CapturedReference < result[j].CapturedReference })
	return result
}

package hydration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"golang.org/x/sys/unix"
)

var (
	ErrUnsafeMaterialization = errors.New("unsafe materialization state")
	ErrHydrationIntegrity    = errors.New("hydration integrity check failed")
)

type Phase string

const (
	PhaseStageCreated            Phase = "MaterializationStageCreated"
	PhaseGenerationFetched       Phase = "GenerationFetched"
	PhaseGenerationLoaded        Phase = "GenerationLoaded"
	PhaseExtractComplete         Phase = "MaterializationExtractComplete"
	PhaseHydrationRootPublished  Phase = "HydrationRootPublished"
	PhaseHydrationMarkerPrepared Phase = "HydrationMarkerPrepared"
	PhaseRenameComplete          Phase = "MaterializationRenameComplete"
	PhaseOwnershipFact           Phase = "MaterializationOwnershipFact"
)

type Request struct {
	SessionID   string
	Capsule     string
	Generation  domain.GenerationRef
	Metadata    domain.GenerationMetadata
	SessionRoot string
	StageRoot   string
	FinalRoot   string
	HaulPath    string
	Token       string
}

type Result struct {
	Materialization domain.Materialization
	StageRoot       string
	FinalRoot       string
	Token           string
}

type GenerationStore interface {
	Get(context.Context, string) (io.ReadCloser, ports.ObjectMeta, error)
}

type Hauler interface {
	Load(context.Context, string, []string) (ports.Result, error)
	Extract(context.Context, string, string, string) (ports.Result, error)
}

type ArchiveExtractor interface {
	Extract(context.Context, string, string) error
}

type ProvenanceArchiveExtractor interface {
	ExtractWithProvenance(context.Context, string, string, func(archiveadapter.ExtractionRoot) ([]byte, error)) error
}

type Hooks struct {
	Before func(context.Context, Phase, Request) error
	After  func(context.Context, Phase, Result) error
	Cut    func(Phase) error
}

type Controller struct {
	store     GenerationStore
	hauler    Hauler
	archive   ArchiveExtractor
	ownership *capsule.Ownership
	hooks     Hooks
}

func NewController(store GenerationStore, hauler Hauler, archive ArchiveExtractor, ownership *capsule.Ownership, hooks Hooks) *Controller {
	return &Controller{store: store, hauler: hauler, archive: archive, ownership: ownership, hooks: hooks}
}

func (c *Controller) WithHooks(hooks Hooks) *Controller {
	if c == nil {
		return nil
	}
	copy := *c
	copy.hooks = composeHooks(c.hooks, hooks)
	return &copy
}

func composeHooks(base, extra Hooks) Hooks {
	result := base
	if extra.Before != nil {
		before := result.Before
		result.Before = func(ctx context.Context, phase Phase, request Request) error {
			if before != nil {
				if err := before(ctx, phase, request); err != nil {
					return err
				}
			}
			return extra.Before(ctx, phase, request)
		}
	}
	if extra.After != nil {
		after := result.After
		result.After = func(ctx context.Context, phase Phase, outcome Result) error {
			if after != nil {
				if err := after(ctx, phase, outcome); err != nil {
					return err
				}
			}
			return extra.After(ctx, phase, outcome)
		}
	}
	if extra.Cut != nil {
		cut := result.Cut
		result.Cut = func(phase Phase) error {
			if cut != nil {
				if err := cut(phase); err != nil {
					return err
				}
			}
			return extra.Cut(phase)
		}
	}
	return result
}

func (c *Controller) Hydrate(ctx context.Context, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	if c == nil || c.store == nil || c.hauler == nil || c.archive == nil || c.ownership == nil {
		return Result{}, errors.New("hydration dependencies are incomplete")
	}
	if err := c.validateRoots(request); err != nil {
		return Result{}, err
	}
	state, stageExists, finalExists, err := c.loadOrCreateState(request)
	if err != nil {
		return Result{}, err
	}
	result := Result{StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, Token: request.Token}

	if finalExists {
		if err := c.validateFinal(request, state); err != nil {
			return Result{}, err
		}
		state.Renamed = true
		state.FinalDevice, state.FinalInode, err = directoryIdentity(request.FinalRoot)
		if err != nil {
			return Result{}, err
		}
		if err := c.writeState(request.StageRoot, state); err != nil && stageExists {
			return Result{}, err
		}
	} else {
		if !stageExists {
			if err := c.run(ctx, PhaseStageCreated, request, &state, func() error {
				if err := mkdirOwned(request.StageRoot); err != nil {
					return err
				}
				state.StageCreated = true
				var err error
				state.StageDevice, state.StageInode, err = directoryIdentity(request.StageRoot)
				if err != nil {
					return err
				}
				return c.writeState(request.StageRoot, state)
			}); err != nil {
				return Result{}, err
			}
			stageExists = true
		} else if !state.StageCreated {
			return Result{}, fmt.Errorf("materialization stage marker is incomplete: %w", ErrUnsafeMaterialization)
		}
		if err := c.run(ctx, PhaseGenerationFetched, request, &state, func() error {
			if state.GenerationFetched {
				return nil
			}
			if err := c.fetch(ctx, request); err != nil {
				return err
			}
			state.GenerationFetched = true
			return c.writeState(request.StageRoot, state)
		}); err != nil {
			return Result{}, err
		}
		storeRoot := filepath.Join(request.StageRoot, "hauler-store")
		if err := c.run(ctx, PhaseGenerationLoaded, request, &state, func() error {
			if state.GenerationLoaded {
				return nil
			}
			if err := mkdirOwned(storeRoot); err != nil {
				return err
			}
			result, err := c.hauler.Load(ctx, storeRoot, []string{request.HaulPath})
			if err != nil {
				return fmt.Errorf("load generation with Hauler: %w", err)
			}
			if result.ExitCode != 0 {
				return fmt.Errorf("load generation with Hauler exited %d", result.ExitCode)
			}
			state.GenerationLoaded = true
			return c.writeState(request.StageRoot, state)
		}); err != nil {
			return Result{}, err
		}
		outerRoot := filepath.Join(request.StageRoot, "hauler-extract")
		innerArchive := filepath.Join(outerRoot, request.Capsule+".tar.zst")
		if err := c.run(ctx, PhaseExtractComplete, request, &state, func() error {
			if state.ExtractComplete {
				return nil
			}
			if info, statErr := os.Lstat(innerArchive); statErr == nil {
				if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
					return fmt.Errorf("extracted generation payload is unsafe: %w", ErrUnsafeMaterialization)
				}
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return statErr
			} else {
				if _, err := os.Lstat(outerRoot); err == nil {
					return fmt.Errorf("unexplained Hauler extraction directory: %w", ErrUnsafeMaterialization)
				} else if !errors.Is(err, os.ErrNotExist) {
					return err
				}
				if result, err := c.hauler.Extract(ctx, storeRoot, "hauler/"+request.Capsule+".tar.zst", outerRoot); err != nil {
					return fmt.Errorf("extract generation with Hauler: %w", err)
				} else if result.ExitCode != 0 {
					return fmt.Errorf("extract generation with Hauler exited %d", result.ExitCode)
				}
			}
			rootStage := filepath.Join(request.StageRoot, "root")
			if info, statErr := os.Lstat(rootStage); statErr == nil {
				if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
					return fmt.Errorf("materialization extraction root is unsafe: %w", ErrUnsafeMaterialization)
				}
				if err := c.completeHydrationMarker(rootStage, request); err != nil {
					return err
				}
			} else if errors.Is(statErr, os.ErrNotExist) {
				partial := rootStage + ".partial"
				if err := removeOwnedArchivePartial(partial, request); err != nil {
					return err
				}
				extractor, ok := c.archive.(ProvenanceArchiveExtractor)
				if !ok {
					return fmt.Errorf("archive extractor cannot persist hydration provenance: %w", ErrUnsafeMaterialization)
				}
				if err := extractor.ExtractWithProvenance(ctx, innerArchive, rootStage, func(root archiveadapter.ExtractionRoot) ([]byte, error) {
					return extractionProvenanceBytesForIdentity(root.Device, root.Inode, request), nil
				}); err != nil {
					return fmt.Errorf("securely extract generation: %w", err)
				}
				if c.hooks.Cut != nil {
					if err := c.hooks.Cut(PhaseHydrationRootPublished); err != nil {
						return err
					}
				}
				if err := c.completeHydrationMarker(rootStage, request); err != nil {
					return err
				}
			} else {
				return statErr
			}
			state.ExtractComplete = true
			return c.writeState(request.StageRoot, state)
		}); err != nil {
			return Result{}, err
		}
		if err := c.run(ctx, PhaseRenameComplete, request, &state, func() error {
			if state.Renamed {
				return nil
			}
			if err := c.commit(ctx, request); err != nil {
				return err
			}
			state.Renamed = true
			state.FinalDevice, state.FinalInode, err = directoryIdentity(request.FinalRoot)
			if err != nil {
				return err
			}
			return c.writeState(request.StageRoot, state)
		}); err != nil {
			return Result{}, err
		}
	}

	if c.hooks.Before != nil {
		if err := c.hooks.Before(ctx, PhaseOwnershipFact, request); err != nil {
			return Result{}, err
		}
	}
	materialization, err := c.ownership.MarkCreatedWithToken(request.FinalRoot, request.Token)
	if err != nil {
		return Result{}, fmt.Errorf("record created materialization ownership: %w", err)
	}
	state.OwnershipFact = true
	if err := c.writeState(request.StageRoot, state); err != nil && stageExists {
		return Result{}, err
	}
	if err := c.afterCut(ctx, PhaseOwnershipFact, request, Result{Materialization: materialization, StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, Token: request.Token}); err != nil {
		return Result{}, err
	}
	if stageExists {
		if err := removeOwnedStage(request.StageRoot, request); err != nil {
			return Result{}, err
		}
	}
	result.Materialization = materialization
	return result, nil
}

func (c *Controller) run(ctx context.Context, phase Phase, request Request, state *stageState, effect func() error) error {
	if c.hooks.Before != nil {
		if err := c.hooks.Before(ctx, phase, request); err != nil {
			return err
		}
	}
	if err := effect(); err != nil {
		return err
	}
	if c.hooks.Cut != nil {
		if err := c.hooks.Cut(phase); err != nil {
			return err
		}
	}
	return c.after(ctx, phase, request, Result{StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, Token: request.Token})
}

func (c *Controller) after(ctx context.Context, phase Phase, request Request, result Result) error {
	if c.hooks.After == nil {
		return nil
	}
	return c.hooks.After(ctx, phase, result)
}

func (c *Controller) afterCut(ctx context.Context, phase Phase, request Request, result Result) error {
	if c.hooks.Cut != nil {
		if err := c.hooks.Cut(phase); err != nil {
			return err
		}
	}
	return c.after(ctx, phase, request, result)
}

type stageState struct {
	Version           int    `json:"version"`
	SessionID         string `json:"sessionId"`
	Capsule           string `json:"capsule"`
	Token             string `json:"token"`
	StageRoot         string `json:"stageRoot"`
	FinalRoot         string `json:"finalRoot"`
	HaulPath          string `json:"haulPath"`
	Generation        uint64 `json:"generation"`
	ArchiveSHA256     string `json:"archiveSha256"`
	StageCreated      bool   `json:"stageCreated"`
	StageDevice       uint64 `json:"stageDevice,omitempty"`
	StageInode        uint64 `json:"stageInode,omitempty"`
	GenerationFetched bool   `json:"generationFetched"`
	GenerationLoaded  bool   `json:"generationLoaded"`
	ExtractComplete   bool   `json:"extractComplete"`
	Renamed           bool   `json:"renamed"`
	OwnershipFact     bool   `json:"ownershipFact"`
	FinalDevice       uint64 `json:"finalDevice,omitempty"`
	FinalInode        uint64 `json:"finalInode,omitempty"`
}

type hydrationMarker struct {
	Version       int    `json:"version"`
	SessionID     string `json:"sessionId"`
	Token         string `json:"token"`
	CanonicalPath string `json:"canonicalPath"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

type extractionProvenance struct {
	Version       int    `json:"version"`
	SessionID     string `json:"sessionId"`
	Capsule       string `json:"capsule"`
	Token         string `json:"token"`
	StageRoot     string `json:"stageRoot"`
	FinalRoot     string `json:"finalRoot"`
	Generation    uint64 `json:"generation"`
	ArchiveSHA256 string `json:"archiveSha256"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

func validateRequest(request Request) error {
	if request.SessionID == "" || strings.ContainsAny(request.SessionID, "/\\\x00") || request.Capsule == "" || strings.ContainsAny(request.Capsule, "/\\\x00") || request.SessionRoot == "" || request.StageRoot == "" || request.FinalRoot == "" || request.HaulPath == "" || request.Token == "" {
		return errors.New("hydration request is incomplete")
	}
	if !filepath.IsAbs(request.SessionRoot) || !filepath.IsAbs(request.StageRoot) || !filepath.IsAbs(request.FinalRoot) || !filepath.IsAbs(request.HaulPath) {
		return errors.New("hydration paths must be absolute")
	}
	decoded, err := hex.DecodeString(request.Token)
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("hydration ownership token is invalid: %w", ErrUnsafeMaterialization)
	}
	if request.Generation.Generation == 0 || len(request.Generation.ArchiveSHA256) != sha256.Size*2 || request.Generation.ArchiveSHA256 != strings.ToLower(request.Generation.ArchiveSHA256) || request.Metadata.ObjectKey == "" || request.Metadata.Size < 0 {
		return fmt.Errorf("hydration generation identity is incomplete: %w", ErrHydrationIntegrity)
	}
	if request.Metadata.SchemaVersion != domain.SchemaVersion || request.Metadata.Capsule != request.Capsule || request.Metadata.Lineage.Branch == "" || request.Metadata.Generation != request.Generation || request.Metadata.Generation.ArchiveSHA256 != strings.ToLower(request.Metadata.Generation.ArchiveSHA256) || !request.Metadata.Verified.LocalHaulLoadable || !request.Metadata.Verified.RemoteBytesVerified {
		return fmt.Errorf("hydration generation metadata does not match request: %w", ErrHydrationIntegrity)
	}
	wantObject, err := coordination.GenerationObjectKey(request.Capsule, request.Metadata.Lineage, request.Generation)
	if err != nil || request.Metadata.ObjectKey != wantObject {
		return fmt.Errorf("hydration generation object key does not match request: %w", ErrHydrationIntegrity)
	}
	wantMetadata, err := coordination.GenerationMetadataKey(request.Capsule, request.Metadata.Lineage, request.Generation)
	if err != nil || request.Metadata.MetadataKey != wantMetadata {
		return fmt.Errorf("hydration generation metadata key does not match request: %w", ErrHydrationIntegrity)
	}
	return nil
}

func (c *Controller) validateRoots(request Request) error {
	ownershipRoot := c.ownership.MaterializationRoot()
	if ownershipRoot == "" || !within(ownershipRoot, request.FinalRoot) || filepath.Clean(request.FinalRoot) == filepath.Clean(ownershipRoot) {
		return fmt.Errorf("final materialization is outside the owned XDG root: %w", ErrUnsafeMaterialization)
	}
	if !within(request.SessionRoot, request.StageRoot) || filepath.Clean(request.StageRoot) == filepath.Clean(request.SessionRoot) || !within(request.SessionRoot, request.HaulPath) {
		return fmt.Errorf("hydration stage or haul is outside the session root: %w", ErrUnsafeMaterialization)
	}
	if err := secureExistingDirectory(request.SessionRoot); err != nil {
		return err
	}
	if err := secureParent(request.StageRoot); err != nil {
		return err
	}
	if err := secureParent(request.FinalRoot); err != nil {
		return err
	}
	return nil
}

func (c *Controller) loadOrCreateState(request Request) (stageState, bool, bool, error) {
	stageInfo, stageErr := os.Lstat(request.StageRoot)
	stageExists := stageErr == nil
	if stageErr != nil && !errors.Is(stageErr, os.ErrNotExist) {
		return stageState{}, false, false, stageErr
	}
	finalInfo, finalErr := os.Lstat(request.FinalRoot)
	finalExists := finalErr == nil
	if finalErr != nil && !errors.Is(finalErr, os.ErrNotExist) {
		return stageState{}, stageExists, false, finalErr
	}
	if stageExists && (stageInfo.Mode()&os.ModeSymlink != 0 || !stageInfo.IsDir()) {
		return stageState{}, true, finalExists, fmt.Errorf("materialization stage is not a real directory: %w", ErrUnsafeMaterialization)
	}
	if finalExists && (finalInfo.Mode()&os.ModeSymlink != 0 || !finalInfo.IsDir()) {
		return stageState{}, stageExists, true, fmt.Errorf("materialization final root is not a real directory: %w", ErrUnsafeMaterialization)
	}
	if !stageExists {
		return stageState{Version: 1, SessionID: request.SessionID, Capsule: request.Capsule, Token: request.Token, StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, HaulPath: request.HaulPath, Generation: request.Generation.Generation, ArchiveSHA256: request.Generation.ArchiveSHA256}, false, finalExists, nil
	}
	state, err := readState(request.StageRoot)
	if err != nil {
		return stageState{}, true, finalExists, err
	}
	want := stageState{Version: 1, SessionID: request.SessionID, Capsule: request.Capsule, Token: request.Token, StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, HaulPath: request.HaulPath, Generation: request.Generation.Generation, ArchiveSHA256: request.Generation.ArchiveSHA256}
	if state.Version != want.Version || state.SessionID != want.SessionID || state.Capsule != want.Capsule || state.Token != want.Token || state.StageRoot != want.StageRoot || state.FinalRoot != want.FinalRoot || state.HaulPath != want.HaulPath || state.Generation != want.Generation || state.ArchiveSHA256 != want.ArchiveSHA256 {
		return stageState{}, true, finalExists, fmt.Errorf("materialization stage marker does not match request: %w", ErrUnsafeMaterialization)
	}
	if state.StageCreated {
		device, inode, err := directoryIdentity(request.StageRoot)
		if err != nil {
			return stageState{}, true, finalExists, err
		}
		if state.StageDevice != device || state.StageInode != inode {
			return stageState{}, true, finalExists, fmt.Errorf("materialization stage identity changed: %w", ErrUnsafeMaterialization)
		}
	}
	return state, true, finalExists, nil
}

func (c *Controller) validateFinal(request Request, state stageState) error {
	if err := c.validateHydrationMarker(request.FinalRoot, request); err != nil {
		return err
	}
	device, inode, err := directoryIdentity(request.FinalRoot)
	if err != nil {
		return err
	}
	if state.FinalDevice != 0 && (state.FinalDevice != device || state.FinalInode != inode) {
		return fmt.Errorf("materialization final identity changed: %w", ErrUnsafeMaterialization)
	}
	return nil
}

func (c *Controller) fetch(ctx context.Context, request Request) error {
	if info, err := os.Lstat(request.HaulPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("existing generation haul is unsafe: %w", ErrUnsafeMaterialization)
		}
		return verifyFile(request.HaulPath, request.Metadata.Size, request.Generation.ArchiveSHA256)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	reader, meta, err := c.store.Get(ctx, request.Metadata.ObjectKey)
	if err != nil {
		return err
	}
	if reader == nil {
		return fmt.Errorf("generation object returned nil reader: %w", ErrHydrationIntegrity)
	}
	partial := request.HaulPath + ".partial"
	if _, err := os.Lstat(partial); err == nil {
		_ = reader.Close()
		return fmt.Errorf("unexplained generation haul partial exists: %w", ErrUnsafeMaterialization)
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = reader.Close()
		return err
	}
	if meta.Size != 0 && meta.Size != request.Metadata.Size {
		_ = reader.Close()
		return fmt.Errorf("remote generation size differs: %w", ErrHydrationIntegrity)
	}
	if meta.SHA256 != "" && meta.SHA256 != request.Generation.ArchiveSHA256 {
		_ = reader.Close()
		return fmt.Errorf("remote generation digest differs: %w", ErrHydrationIntegrity)
	}
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = reader.Close()
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, hash), reader)
	closeReaderErr := reader.Close()
	syncErr := file.Sync()
	closeFileErr := file.Close()
	if copyErr != nil || closeReaderErr != nil || syncErr != nil || closeFileErr != nil {
		_ = os.Remove(partial)
		return fmt.Errorf("write generation haul: %w", errors.Join(copyErr, closeReaderErr, syncErr, closeFileErr))
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if size != request.Metadata.Size || got != request.Generation.ArchiveSHA256 {
		_ = os.Remove(partial)
		return fmt.Errorf("downloaded generation has size %d and digest %s: %w", size, got, ErrHydrationIntegrity)
	}
	if err := renameNoReplace(partial, request.HaulPath); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return syncDirectory(filepath.Dir(request.HaulPath))
}

func (c *Controller) commit(ctx context.Context, request Request) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rootStage := filepath.Join(request.StageRoot, "root")
	if err := c.validateHydrationMarker(rootStage, request); err != nil {
		if _, finalErr := os.Lstat(request.FinalRoot); finalErr == nil {
			return c.validateFinal(request, stageState{})
		}
		return err
	}
	if _, err := os.Lstat(request.FinalRoot); err == nil {
		return c.validateFinal(request, stageState{})
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := mkdirOwned(filepath.Dir(request.FinalRoot)); err != nil {
		return err
	}
	if err := renameNoReplace(rootStage, request.FinalRoot); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return c.validateFinal(request, stageState{})
		}
		return fmt.Errorf("atomically commit materialization: %w", err)
	}
	return syncDirectory(filepath.Dir(request.FinalRoot))
}

const (
	hydrationMarkerName     = "hydration.json"
	hydrationPartialName    = "hydration.json.partial"
	extractionOwnerName     = ".camp-extract-owner"
	maxHydrationMarkerBytes = 4096
)

func (c *Controller) validateHydrationMarker(root string, request Request) error {
	rootFD, rootStat, err := openPinnedDirectory(root)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	runtimeFD, runtimeStat, err := openMarkerRuntime(rootFD, false)
	if err != nil {
		return fmt.Errorf("open hydration marker directory: %w: %w", err, ErrUnsafeMaterialization)
	}
	defer unix.Close(runtimeFD)
	if _, err := validateExactMarkerAt(runtimeFD, hydrationMarkerName, hydrationMarkerBytes(rootStat, request)); err != nil {
		return fmt.Errorf("validate hydration marker: %w: %w", err, ErrUnsafeMaterialization)
	}
	if err := verifyDirectoryAt(rootFD, runtimeStat); err != nil {
		return err
	}
	return verifyPinnedDirectory(root, rootStat)
}

func hydrationMarkerBytes(root unix.Stat_t, request Request) []byte {
	body, _ := json.Marshal(hydrationMarker{Version: 1, SessionID: request.SessionID, Token: request.Token, CanonicalPath: request.FinalRoot, Device: uint64(root.Dev), Inode: root.Ino})
	return body
}

func extractionProvenanceBytes(root unix.Stat_t, request Request) []byte {
	return extractionProvenanceBytesForIdentity(uint64(root.Dev), root.Ino, request)
}

func extractionProvenanceBytesForIdentity(device, inode uint64, request Request) []byte {
	body, _ := json.Marshal(extractionProvenance{
		Version: 1, SessionID: request.SessionID, Capsule: request.Capsule, Token: request.Token,
		StageRoot: request.StageRoot, FinalRoot: request.FinalRoot, Generation: request.Generation.Generation,
		ArchiveSHA256: request.Generation.ArchiveSHA256, Device: device, Inode: inode,
	})
	return body
}

func (c *Controller) completeHydrationMarker(root string, request Request) error {
	rootFD, rootStat, err := openPinnedDirectory(root)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)

	runtimeFD, runtimeStat, runtimeErr := openMarkerRuntime(rootFD, false)
	if runtimeErr != nil && !errors.Is(runtimeErr, syscall.ENOENT) {
		return runtimeErr
	}
	defer func() {
		if runtimeFD >= 0 {
			unix.Close(runtimeFD)
		}
	}()
	finalExists, err := entryExistsAt(runtimeFD, runtimeErr, hydrationMarkerName)
	if err != nil {
		return err
	}
	partialExists, err := entryExistsAt(runtimeFD, runtimeErr, hydrationPartialName)
	if err != nil {
		return err
	}
	if finalExists && partialExists {
		return fmt.Errorf("hydration marker and partial both exist: %w", ErrUnsafeMaterialization)
	}
	body := hydrationMarkerBytes(rootStat, request)
	switch {
	case finalExists:
		_, err = validateExactMarkerAt(runtimeFD, hydrationMarkerName, body)
		if err == nil {
			err = unix.Fsync(runtimeFD)
		}
		if err == nil {
			err = verifyDirectoryAt(rootFD, runtimeStat)
		}
	case partialExists:
		err = c.installHydrationMarkerAt(rootFD, runtimeFD, runtimeStat, body)
	default:
		if runtimeFD >= 0 {
			unix.Close(runtimeFD)
			runtimeFD = -1
		}
		if _, provenanceErr := validateExtractionProvenanceAt(rootFD, rootStat, request); provenanceErr != nil {
			err = fmt.Errorf("validate extraction provenance: %w: %w", provenanceErr, ErrUnsafeMaterialization)
		} else {
			err = c.writeHydrationMarkerAt(rootFD, rootStat, request)
		}
	}
	if err != nil {
		return err
	}
	if provenanceStat, provenanceErr := validateExtractionProvenanceAt(rootFD, rootStat, request); provenanceErr == nil {
		if err := removeExactEntryAt(rootFD, extractionOwnerName, provenanceStat); err != nil {
			return err
		}
	} else if !errors.Is(provenanceErr, syscall.ENOENT) {
		return provenanceErr
	}
	return verifyPinnedDirectory(root, rootStat)
}

func (c *Controller) writeHydrationMarkerAt(rootFD int, rootStat unix.Stat_t, request Request) error {
	runtimeFD, runtimeStat, err := openMarkerRuntime(rootFD, true)
	if err != nil {
		return err
	}
	defer unix.Close(runtimeFD)
	if exists, err := entryExistsAt(runtimeFD, nil, hydrationMarkerName); err != nil || exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("hydration marker unexpectedly exists: %w", ErrUnsafeMaterialization)
	}
	if exists, err := entryExistsAt(runtimeFD, nil, hydrationPartialName); err != nil || exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("hydration marker partial unexpectedly exists: %w", ErrUnsafeMaterialization)
	}
	fd, err := unix.Openat(runtimeFD, hydrationPartialName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), hydrationPartialName)
	var created unix.Stat_t
	statErr := unix.Fstat(fd, &created)
	body := hydrationMarkerBytes(rootStat, request)
	writeErr := statErr
	if writeErr == nil {
		writeErr = writeAll(file, body)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		var cleanupErr error
		if statErr == nil {
			cleanupErr = removeExactEntryAt(runtimeFD, hydrationPartialName, created)
		}
		return errors.Join(writeErr, closeErr, cleanupErr)
	}
	if err := unix.Fsync(runtimeFD); err != nil {
		return err
	}
	if c.hooks.Cut != nil {
		if err := c.hooks.Cut(PhaseHydrationMarkerPrepared); err != nil {
			return err
		}
	}
	if err := verifyDirectoryAt(rootFD, runtimeStat); err != nil {
		return err
	}
	return c.installHydrationMarkerAt(rootFD, runtimeFD, runtimeStat, body)
}

func (c *Controller) installHydrationMarkerAt(rootFD, runtimeFD int, runtimeStat unix.Stat_t, body []byte) error {
	partialStat, err := validateExactMarkerAt(runtimeFD, hydrationPartialName, body)
	if err != nil {
		return err
	}
	if exists, err := entryExistsAt(runtimeFD, nil, hydrationMarkerName); err != nil || exists {
		if err != nil {
			return err
		}
		return fmt.Errorf("hydration marker destination exists: %w", ErrUnsafeMaterialization)
	}
	if err := verifyDirectoryAt(rootFD, runtimeStat); err != nil {
		return err
	}
	if err := unix.Renameat2(runtimeFD, hydrationPartialName, runtimeFD, hydrationMarkerName, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("install hydration marker: %w", err)
	}
	if err := unix.Fsync(runtimeFD); err != nil {
		return err
	}
	finalStat, err := validateExactMarkerAt(runtimeFD, hydrationMarkerName, body)
	if err != nil {
		return err
	}
	if !sameStatIdentity(partialStat, finalStat) {
		return fmt.Errorf("hydration marker identity changed during install: %w", ErrUnsafeMaterialization)
	}
	return verifyDirectoryAt(rootFD, runtimeStat)
}

func validateExtractionProvenanceAt(rootFD int, rootStat unix.Stat_t, request Request) (unix.Stat_t, error) {
	return validateExactMarkerAt(rootFD, extractionOwnerName, extractionProvenanceBytes(rootStat, request))
}

func validateExactMarkerAt(parentFD int, name string, expected []byte) (unix.Stat_t, error) {
	return validateExactMarkerAtWithHook(parentFD, name, expected, nil)
}

func validateExactMarkerAtWithHook(parentFD int, name string, expected []byte, afterRead func() error) (unix.Stat_t, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return unix.Stat_t{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Nlink != 1 || stat.Size < 0 || stat.Size > maxHydrationMarkerBytes {
		return unix.Stat_t{}, fmt.Errorf("marker %q has unsafe metadata: %w", name, ErrUnsafeMaterialization)
	}
	body, err := io.ReadAll(io.LimitReader(file, maxHydrationMarkerBytes+1))
	if err != nil {
		return unix.Stat_t{}, err
	}
	if len(body) > maxHydrationMarkerBytes || string(body) != string(expected) {
		return unix.Stat_t{}, fmt.Errorf("marker %q does not match request: %w", name, ErrUnsafeMaterialization)
	}
	if err := unix.Fsync(fd); err != nil {
		return unix.Stat_t{}, err
	}
	if afterRead != nil {
		if err := afterRead(); err != nil {
			return unix.Stat_t{}, err
		}
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return unix.Stat_t{}, err
	}
	var current unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, err
	}
	if current.Mode&unix.S_IFMT != unix.S_IFREG || current.Mode&0o7777 != 0o600 || current.Nlink != 1 || !sameMarkerStat(stat, after) || !sameMarkerStat(after, current) {
		return unix.Stat_t{}, fmt.Errorf("marker %q name identity changed after read: %w", name, ErrUnsafeMaterialization)
	}
	return stat, nil
}

func openPinnedDirectory(path string) (int, unix.Stat_t, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, unix.Stat_t{}, err
	}
	if err := verifyPinnedDirectory(path, stat); err != nil {
		unix.Close(fd)
		return -1, unix.Stat_t{}, err
	}
	return fd, stat, nil
}

func verifyPinnedDirectory(path string, expected unix.Stat_t) error {
	var current unix.Stat_t
	if err := unix.Lstat(path, &current); err != nil {
		return err
	}
	if current.Mode&unix.S_IFMT != unix.S_IFDIR || !sameStatIdentity(expected, current) {
		return fmt.Errorf("directory path identity changed: %w", ErrUnsafeMaterialization)
	}
	return nil
}

func openMarkerRuntime(rootFD int, create bool) (int, unix.Stat_t, error) {
	campFD, _, err := openDirectoryAtOwned(rootFD, ".camp", create)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	defer unix.Close(campFD)
	return openDirectoryAtOwned(campFD, "runtime", create)
}

func openDirectoryAtOwned(parentFD int, name string, create bool) (int, unix.Stat_t, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) && create {
		if err := unix.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
			return -1, unix.Stat_t{}, err
		}
		if err := unix.Fsync(parentFD); err != nil {
			return -1, unix.Stat_t{}, err
		}
		fd, err = unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, unix.Stat_t{}, err
	}
	return fd, stat, nil
}

func verifyDirectoryAt(rootFD int, expected unix.Stat_t) error {
	fd, current, err := openMarkerRuntime(rootFD, false)
	if err != nil {
		return err
	}
	unix.Close(fd)
	if !sameStatIdentity(expected, current) {
		return fmt.Errorf("hydration marker parent identity changed: %w", ErrUnsafeMaterialization)
	}
	return nil
}

func entryExistsAt(parentFD int, parentErr error, name string) (bool, error) {
	if parentErr != nil {
		if errors.Is(parentErr, syscall.ENOENT) {
			return false, nil
		}
		return false, parentErr
	}
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	return err == nil, err
}

func removeExactEntryAt(parentFD int, name string, expected unix.Stat_t) error {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	quarantine := ".camp-remove-" + hex.EncodeToString(random)
	if err := unix.Renameat2(parentFD, name, parentFD, quarantine, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	current, err := validateEntryIdentityAt(parentFD, quarantine)
	if err != nil || !sameStatIdentity(expected, current) {
		restoreErr := unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE)
		syncErr := unix.Fsync(parentFD)
		return errors.Join(fmt.Errorf("entry %q changed before cleanup: %w", name, ErrUnsafeMaterialization), err, restoreErr, syncErr)
	}
	if err := unix.Unlinkat(parentFD, quarantine, 0); err != nil {
		restoreErr := unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE)
		syncErr := unix.Fsync(parentFD)
		return errors.Join(err, restoreErr, syncErr)
	}
	return unix.Fsync(parentFD)
}

func validateEntryIdentityAt(parentFD int, name string) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return unix.Stat_t{}, err
	}
	return stat, nil
}

func sameStatIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

func sameMarkerStat(left, right unix.Stat_t) bool {
	return sameStatIdentity(left, right) && left.Mode == right.Mode && left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func writeAll(file *os.File, body []byte) error {
	for len(body) > 0 {
		written, err := file.Write(body)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		body = body[written:]
	}
	return nil
}

func (c *Controller) writeState(stage string, state stageState) error {
	if _, err := os.Lstat(stage); err != nil {
		return err
	}
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return writeReplaceFile(filepath.Join(stage, ".camp-stage.json"), body, 0o600)
}

func readState(stage string) (stageState, error) {
	path := filepath.Join(stage, ".camp-stage.json")
	info, err := os.Lstat(path)
	if err != nil {
		return stageState{}, fmt.Errorf("inspect materialization stage marker: %w: %w", err, ErrUnsafeMaterialization)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return stageState{}, fmt.Errorf("materialization stage marker is not a regular file: %w", ErrUnsafeMaterialization)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return stageState{}, fmt.Errorf("read materialization stage marker: %w: %w", err, ErrUnsafeMaterialization)
	}
	var state stageState
	if err := json.Unmarshal(body, &state); err != nil {
		return stageState{}, fmt.Errorf("decode materialization stage marker: %w: %w", err, ErrUnsafeMaterialization)
	}
	return state, nil
}

func removeOwnedStage(stage string, request Request) error {
	state, err := readState(stage)
	if err != nil {
		return err
	}
	if state.Token != request.Token || state.StageRoot != request.StageRoot || state.FinalRoot != request.FinalRoot {
		return fmt.Errorf("materialization stage ownership changed: %w", ErrUnsafeMaterialization)
	}
	if state.StageDevice == 0 || state.StageInode == 0 {
		return fmt.Errorf("materialization stage identity is incomplete: %w", ErrUnsafeMaterialization)
	}
	device, inode, err := directoryIdentity(stage)
	if err != nil {
		return err
	}
	if state.StageDevice != device || state.StageInode != inode {
		return fmt.Errorf("materialization stage identity changed: %w", ErrUnsafeMaterialization)
	}
	return os.RemoveAll(stage)
}

func removeOwnedArchivePartial(path string, request Request) error {
	rootFD, rootStat, err := openPinnedDirectory(path)
	if errors.Is(err, syscall.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := validateExtractionProvenanceAt(rootFD, rootStat, request); err != nil {
		unix.Close(rootFD)
		return fmt.Errorf("validate interrupted extraction provenance: %w: %w", err, ErrUnsafeMaterialization)
	}
	if err := verifyPinnedDirectory(path, rootStat); err != nil {
		unix.Close(rootFD)
		return err
	}
	if err := unix.Close(rootFD); err != nil {
		return err
	}
	return removeExactDirectoryPath(path, rootStat)
}

func removeExactDirectoryPath(path string, expected unix.Stat_t) error {
	parentFD, _, err := openPinnedDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer unix.Close(parentFD)
	name := filepath.Base(path)
	current, err := validateEntryIdentityAt(parentFD, name)
	if err != nil {
		return err
	}
	if current.Mode&unix.S_IFMT != unix.S_IFDIR || !sameStatIdentity(expected, current) {
		return fmt.Errorf("interrupted extraction changed before cleanup: %w", ErrUnsafeMaterialization)
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	quarantine := ".camp-extract-remove-" + hex.EncodeToString(random)
	if err := unix.Renameat2(parentFD, name, parentFD, quarantine, unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	if err := unix.Fsync(parentFD); err != nil {
		restoreErr := unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE)
		return errors.Join(err, restoreErr, unix.Fsync(parentFD))
	}
	quarantineFD, err := openHydrationCleanupChildAt(parentFD, quarantine, expected)
	if err != nil {
		return errors.Join(err, unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE), unix.Fsync(parentFD))
	}
	var quarantined unix.Stat_t
	statErr := unix.Fstat(quarantineFD, &quarantined)
	if statErr != nil || !sameStatIdentity(expected, quarantined) {
		unix.Close(quarantineFD)
		restoreErr := unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE)
		return errors.Join(fmt.Errorf("interrupted extraction cleanup captured replacement: %w", ErrUnsafeMaterialization), statErr, restoreErr, unix.Fsync(parentFD))
	}
	removeErr := removeDirectoryContentsAt(quarantineFD)
	closeErr := unix.Close(quarantineFD)
	if removeErr != nil || closeErr != nil {
		restoreErr := unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE)
		return errors.Join(removeErr, closeErr, restoreErr, unix.Fsync(parentFD))
	}
	if err := unix.Unlinkat(parentFD, quarantine, unix.AT_REMOVEDIR); err != nil {
		return errors.Join(err, unix.Renameat2(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE), unix.Fsync(parentFD))
	}
	return unix.Fsync(parentFD)
}

func removeDirectoryContentsAt(directoryFD int) error {
	if err := unix.Fchmod(directoryFD, 0o700); err != nil {
		return err
	}
	dup, err := unix.Dup(directoryFD)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(dup), "hydration-extraction-cleanup")
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(directoryFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			childFD, err := openHydrationCleanupChildAt(directoryFD, entry.Name(), stat)
			if err != nil {
				return err
			}
			removeErr := removeDirectoryContentsAt(childFD)
			closeErr := unix.Close(childFD)
			if removeErr != nil || closeErr != nil {
				return errors.Join(removeErr, closeErr)
			}
			var after unix.Stat_t
			if err := unix.Fstatat(directoryFD, entry.Name(), &after, unix.AT_SYMLINK_NOFOLLOW); err != nil || after.Mode&unix.S_IFMT != unix.S_IFDIR || !sameStatIdentity(stat, after) {
				return fmt.Errorf("hydration cleanup child changed after recursion: %w", ErrUnsafeMaterialization)
			}
			if err := unix.Unlinkat(directoryFD, entry.Name(), unix.AT_REMOVEDIR); err != nil {
				return err
			}
		} else if err := unix.Unlinkat(directoryFD, entry.Name(), 0); err != nil {
			return err
		}
	}
	return unix.Fsync(directoryFD)
}

func openHydrationCleanupChildAt(parentFD int, name string, named unix.Stat_t) (int, error) {
	return openHydrationCleanupChildAtWithHook(parentFD, name, named, nil)
}

func openHydrationCleanupChildAtWithHook(parentFD int, name string, named unix.Stat_t, beforeOpen func() error) (int, error) {
	if named.Mode&unix.S_IFMT != unix.S_IFDIR {
		return -1, fmt.Errorf("hydration cleanup child %q is not a directory: %w", name, ErrUnsafeMaterialization)
	}
	if beforeOpen != nil {
		if err := beforeOpen(); err != nil {
			return -1, err
		}
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(parentFD, name, how)
	if err != nil {
		return -1, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		unix.Close(fd)
		return -1, err
	}
	var parent unix.Stat_t
	if err := unix.Fstat(parentFD, &parent); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if opened.Mode&unix.S_IFMT != unix.S_IFDIR || !sameStatIdentity(named, opened) || opened.Dev != parent.Dev {
		unix.Close(fd)
		return -1, fmt.Errorf("hydration cleanup child %q identity or mount changed: %w", name, ErrUnsafeMaterialization)
	}
	return fd, nil
}

func secureExistingDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path %q is not a real directory: %w", path, ErrUnsafeMaterialization)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != absolute {
		return fmt.Errorf("path %q has unexplained symlinks: %w", path, ErrUnsafeMaterialization)
	}
	return nil
}

func secureParent(path string) error {
	parent := filepath.Dir(path)
	if err := secureExistingDirectory(parent); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return secureMkdirAll(parent)
	}
	return nil
}

func secureMkdirAll(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parts := strings.Split(filepath.Clean(abs), string(filepath.Separator))
	current := string(filepath.Separator)
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			continue
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("directory path %q is unsafe: %w", current, ErrUnsafeMaterialization)
		}
	}
	return nil
}

func mkdirOwned(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("owned directory %q is unsafe: %w", path, ErrUnsafeMaterialization)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return secureMkdirAll(path)
}

func writeStableFile(path string, body []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("stable file %q is unsafe: %w", path, ErrUnsafeMaterialization)
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if string(existing) == string(body) {
			return nil
		}
		return fmt.Errorf("stable file %q differs: %w", path, ErrUnsafeMaterialization)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	partial := path + ".partial"
	if _, err := os.Lstat(partial); err == nil {
		return fmt.Errorf("unexplained stable file partial exists: %w", ErrUnsafeMaterialization)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(partial)
		return errors.Join(err, closeErr)
	}
	if err := renameNoReplace(partial, path); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func writeReplaceFile(path string, body []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("replace target %q is unsafe: %w", path, ErrUnsafeMaterialization)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	partial := path + ".partial"
	if _, err := os.Lstat(partial); err == nil {
		return fmt.Errorf("unexplained replace partial exists: %w", ErrUnsafeMaterialization)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(partial)
		return errors.Join(err, closeErr)
	}
	if err := os.Rename(partial, path); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func verifyFile(path string, expectedSize int64, expectedSHA string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return errors.Join(copyErr, closeErr)
	}
	if size != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedSHA {
		return fmt.Errorf("existing generation haul failed verification: %w", ErrHydrationIntegrity)
	}
	return nil
}

func directoryIdentity(path string) (uint64, uint64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, 0, fmt.Errorf("directory identity is unsafe: %w", ErrUnsafeMaterialization)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, errors.New("directory identity unavailable")
	}
	return uint64(stat.Dev), stat.Ino, nil
}

func within(root, candidate string) bool {
	root, rootErr := filepath.Abs(root)
	candidate, candidateErr := filepath.Abs(candidate)
	if rootErr != nil || candidateErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && relative != "." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

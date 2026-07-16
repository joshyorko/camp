package hydration

import (
	"context"
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

	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrUnsafeMaterialization = errors.New("unsafe materialization state")
	ErrHydrationIntegrity    = errors.New("hydration integrity check failed")
)

type Phase string

const (
	PhaseStageCreated      Phase = "MaterializationStageCreated"
	PhaseGenerationFetched Phase = "GenerationFetched"
	PhaseGenerationLoaded  Phase = "GenerationLoaded"
	PhaseExtractComplete   Phase = "MaterializationExtractComplete"
	PhaseRenameComplete    Phase = "MaterializationRenameComplete"
	PhaseOwnershipFact     Phase = "MaterializationOwnershipFact"
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
				if err := c.validateHydrationMarker(rootStage, request); err != nil {
					return err
				}
			} else if errors.Is(statErr, os.ErrNotExist) {
				partial := rootStage + ".partial"
				if err := removeOwnedArchivePartial(partial); err != nil {
					return err
				}
				if err := c.archive.Extract(ctx, innerArchive, rootStage); err != nil {
					return fmt.Errorf("securely extract generation: %w", err)
				}
				if err := writeHydrationMarker(rootStage, request); err != nil {
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

func (c *Controller) validateHydrationMarker(root string, request Request) error {
	markerPath := filepath.Join(root, ".camp", "runtime", "hydration.json")
	info, err := os.Lstat(markerPath)
	if err != nil {
		return fmt.Errorf("inspect hydration marker: %w: %w", err, ErrUnsafeMaterialization)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("hydration marker is not a regular file: %w", ErrUnsafeMaterialization)
	}
	body, err := os.ReadFile(markerPath)
	if err != nil {
		return fmt.Errorf("read hydration marker: %w: %w", err, ErrUnsafeMaterialization)
	}
	var marker hydrationMarker
	if json.Unmarshal(body, &marker) != nil || marker.Version != 1 || marker.SessionID != request.SessionID || marker.Token != request.Token || marker.CanonicalPath != request.FinalRoot {
		return fmt.Errorf("hydration marker does not match request: %w", ErrUnsafeMaterialization)
	}
	device, inode, err := directoryIdentity(root)
	if err != nil {
		return err
	}
	if marker.Device != device || marker.Inode != inode {
		return fmt.Errorf("hydration marker identity does not match root: %w", ErrUnsafeMaterialization)
	}
	return nil
}

func writeHydrationMarker(root string, request Request) error {
	device, inode, err := directoryIdentity(root)
	if err != nil {
		return err
	}
	directory := filepath.Join(root, ".camp", "runtime")
	if err := secureMkdirAll(directory); err != nil {
		return err
	}
	body, err := json.Marshal(hydrationMarker{Version: 1, SessionID: request.SessionID, Token: request.Token, CanonicalPath: request.FinalRoot, Device: device, Inode: inode})
	if err != nil {
		return err
	}
	return writeStableFile(filepath.Join(directory, "hydration.json"), body, 0o600)
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

func removeOwnedArchivePartial(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("unexplained archive partial is unsafe: %w", ErrUnsafeMaterialization)
	}
	marker, err := os.ReadFile(filepath.Join(path, ".camp-extract-owner"))
	if err != nil || string(marker) != "camp-owned-partial\n" {
		return fmt.Errorf("unexplained archive partial exists: %w", ErrUnsafeMaterialization)
	}
	return os.RemoveAll(path)
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

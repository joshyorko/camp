package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"syscall"
	"time"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	supervisoradapter "github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/journal"
	"github.com/joshyorko/camp/internal/registry"
	"golang.org/x/sys/unix"
)

type productionCheckpointRuntime struct {
	manifest        haulkit.Manifest
	services        *productionServicesRuntime
	controller      *supervisoradapter.ServiceSupervisor
	serviceStore    *journal.Store
	serviceSnapshot domain.JournalSnapshot
	readonly        *supervisoradapter.ServiceSupervisor
	readonlyRecord  domain.ServiceUnitRecord
	hauler          *hauleradapter.Client
	attemptRoot     string
	outputRoot      string
	storeRoot       string
	rootReference   string
	barrierLock     *os.File
}

func newProductionCheckpointRuntime() *productionCheckpointRuntime {
	return &productionCheckpointRuntime{}
}

func (runtimeState *productionCheckpointRuntime) Verify(ctx context.Context, request Request) error {
	if request.Checkpoint == nil {
		return errors.New("remote checkpoint envelope is missing")
	}
	runtimeState.services = &productionServicesRuntime{}
	if err := runtimeState.services.Verify(ctx, request); err != nil {
		return err
	}
	body, err := readStableIdentityFile(request.ManifestPath, request.Expected.Manifest)
	if err != nil {
		return err
	}
	runtimeState.manifest, err = haulkit.DecodeCanonical(body)
	if err != nil {
		return err
	}
	if runtimeState.manifest.SessionID != request.SessionID ||
		runtimeState.manifest.Capsule != request.Checkpoint.Capsule ||
		runtimeState.manifest.Lineage != request.Checkpoint.Lineage ||
		request.Checkpoint.Generation == 0 {
		return ErrIdentityMismatch
	}
	runtimeState.attemptRoot = filepath.Join(request.RuntimeRoot, "checkpoints", request.Checkpoint.AttemptID)
	runtimeState.outputRoot = filepath.Join(runtimeState.attemptRoot, "output")
	runtimeState.storeRoot = filepath.Join(runtimeState.attemptRoot, "store")
	runtimeState.rootReference = "hauler/" + request.Checkpoint.Capsule + ".tar.zst:latest"
	if err := secureMkdirAllOperation(filepath.Dir(runtimeState.attemptRoot)); err != nil {
		return err
	}
	if info, err := os.Lstat(runtimeState.attemptRoot); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrIdentityMismatch
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	version := runtimeState.manifest.Tools.Hauler.Version
	runtimeState.hauler = hauleradapter.NewClientWithVersion(
		filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hauler"), version, subprocess.NewRunner(),
	)
	return nil
}

func (runtimeState *productionCheckpointRuntime) Observe(ctx context.Context, request Request) (CheckpointReceipt, bool, error) {
	path := filepath.Join(runtimeState.attemptRoot, "receipt.json")
	body, err := readCheckpointRegular(path, DiagnosticLimit)
	if errors.Is(err, os.ErrNotExist) {
		preparedPath := filepath.Join(runtimeState.attemptRoot, "prepared.json")
		preparedBody, preparedErr := readCheckpointRegular(preparedPath, DiagnosticLimit)
		if errors.Is(preparedErr, os.ErrNotExist) {
			return CheckpointReceipt{}, false, nil
		}
		if preparedErr != nil {
			return CheckpointReceipt{}, false, preparedErr
		}
		var prepared CheckpointReceipt
		if err := json.Unmarshal(preparedBody, &prepared); err != nil {
			return CheckpointReceipt{}, false, err
		}
		if err := validateCheckpointReceipt(request, prepared); err != nil {
			return CheckpointReceipt{}, false, err
		}
		if err := publishCheckpointExport(request.WorkspaceRoot, prepared); err != nil {
			return CheckpointReceipt{}, false, err
		}
		if err := publishCheckpointRegular(path, preparedBody, 0o400); err != nil {
			return CheckpointReceipt{}, false, err
		}
		if err := runtimeState.stopReadonlyFromJournal(ctx, request); err != nil {
			return CheckpointReceipt{}, false, err
		}
		return prepared, true, nil
	}
	if err != nil {
		return CheckpointReceipt{}, false, err
	}
	var receipt CheckpointReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		return CheckpointReceipt{}, false, err
	}
	if err := validateCheckpointReceipt(request, receipt); err != nil {
		return CheckpointReceipt{}, false, err
	}
	if err := validateCheckpointExport(request.WorkspaceRoot, receipt); err != nil {
		return CheckpointReceipt{}, false, err
	}
	return receipt, true, nil
}

func (runtimeState *productionCheckpointRuntime) stopReadonlyFromJournal(ctx context.Context, request Request) error {
	root := filepath.Join(runtimeState.attemptRoot, "readonly-registry")
	store, err := journal.NewStore(filepath.Join(root, "journal"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	snapshot, _, err := store.Load(ctx, request.SessionID)
	if errors.Is(err, os.ErrNotExist) || len(snapshot.Services) == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	processes, err := supervisoradapter.NewProcessManager()
	if err != nil {
		return err
	}
	controller := supervisoradapter.NewServiceSupervisor(
		store, processes, supervisoradapter.NewUnitInspector(subprocess.NewRunner(), http.DefaultClient),
	)
	for _, record := range snapshot.Services {
		observation, err := controller.Observe(ctx, record)
		if err != nil {
			return err
		}
		if observation.State == supervisoradapter.UnitLive {
			if err := controller.Stop(ctx, record); err != nil {
				return err
			}
		}
	}
	return nil
}

func (runtimeState *productionCheckpointRuntime) Quiesce(ctx context.Context, request Request) (ServiceCheckpointEvidence, error) {
	serviceRoot := filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "services")
	lock, err := lockRemoteServices(serviceRoot)
	if err != nil {
		return ServiceCheckpointEvidence{}, err
	}
	keepLock := false
	defer func() {
		if !keepLock {
			unlockRemoteServices(lock)
		}
	}()
	runtimeState.serviceStore, err = journal.NewStore(filepath.Join(serviceRoot, "journal"))
	if err != nil {
		return ServiceCheckpointEvidence{}, err
	}
	runtimeState.serviceSnapshot, _, err = runtimeState.serviceStore.Load(ctx, request.SessionID)
	if err != nil {
		return ServiceCheckpointEvidence{}, err
	}
	if err := validateServiceEvidence(runtimeState.serviceSnapshot.Services); err != nil {
		return ServiceCheckpointEvidence{}, err
	}
	processes, err := supervisoradapter.NewProcessManager()
	if err != nil {
		return ServiceCheckpointEvidence{}, err
	}
	runtimeState.controller = supervisoradapter.NewServiceSupervisor(
		runtimeState.serviceStore, processes,
		supervisoradapter.NewUnitInspector(subprocess.NewRunner(), http.DefaultClient),
	)
	for _, record := range runtimeState.serviceSnapshot.Services {
		observation, err := runtimeState.controller.Observe(ctx, record)
		if err != nil || observation.State != supervisoradapter.UnitLive ||
			!reflect.DeepEqual(observation.Record, record) {
			return ServiceCheckpointEvidence{}, errors.Join(err, ErrServiceEvidence)
		}
	}
	for index := len(runtimeState.serviceSnapshot.Services) - 1; index >= 0; index-- {
		if err := runtimeState.controller.Stop(ctx, runtimeState.serviceSnapshot.Services[index]); err != nil {
			return ServiceCheckpointEvidence{}, err
		}
	}
	canonical, err := json.Marshal(runtimeState.serviceSnapshot.Services)
	if err != nil {
		return ServiceCheckpointEvidence{}, err
	}
	digest := sha256.Sum256(canonical)
	runtimeState.barrierLock = lock
	keepLock = true
	return ServiceCheckpointEvidence{
		Token: hex.EncodeToString(digest[:]), Services: append([]domain.ServiceUnitRecord(nil), runtimeState.serviceSnapshot.Services...),
	}, nil
}

func (runtimeState *productionCheckpointRuntime) ReleaseBarrier(_ context.Context, _ Request, evidence ServiceCheckpointEvidence) error {
	if evidence.Token == "" || runtimeState.barrierLock == nil {
		return errors.New("remote checkpoint registry write barrier is not held")
	}
	lock := runtimeState.barrierLock
	runtimeState.barrierLock = nil
	unlockRemoteServices(lock)
	return nil
}

type checkpointCutBarrier struct{}

func (checkpointCutBarrier) WithCut(_ context.Context, _ registry.SnapshotRequest, cut func() error) error {
	return cut()
}

func (runtimeState *productionCheckpointRuntime) CutRegistry(ctx context.Context, request Request, evidence ServiceCheckpointEvidence) (registry.Snapshot, error) {
	if len(evidence.Services) != 2 || evidence.Token == "" {
		return registry.Snapshot{}, ErrServiceEvidence
	}
	if err := os.Mkdir(runtimeState.attemptRoot, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return registry.Snapshot{}, err
	}
	snapshotter := registry.NewSnapshotter(checkpointCutBarrier{})
	cut, err := snapshotter.Seal(ctx, registry.SnapshotRequest{
		OverlayRoot:     filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "registry"),
		SnapshotRoot:    filepath.Join(runtimeState.attemptRoot, "registry"),
		CatalogEndpoint: "http://127.0.0.1:5000", SessionID: request.SessionID,
		RegistryLaunchToken: evidence.Token,
	})
	if err != nil {
		return registry.Snapshot{}, err
	}
	if err := runtimeState.startReadonlyRegistry(ctx, request, cut.Root); err != nil {
		return registry.Snapshot{}, err
	}
	return cut, nil
}

func (runtimeState *productionCheckpointRuntime) startReadonlyRegistry(ctx context.Context, request Request, cutRoot string) error {
	root := filepath.Join(runtimeState.attemptRoot, "readonly-registry")
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	store, err := journal.NewStore(filepath.Join(root, "journal"))
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	snapshot := domain.JournalSnapshot{
		SchemaVersion: domain.SchemaVersion, SessionID: request.SessionID,
		State: domain.SessionOpen, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.Create(ctx, snapshot); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	processes, err := supervisoradapter.NewProcessManager()
	if err != nil {
		return err
	}
	runtimeState.readonly = supervisoradapter.NewServiceSupervisor(
		store, processes, supervisoradapter.NewUnitInspector(subprocess.NewRunner(), http.DefaultClient),
	)
	childContextPrefix, err := remoteChildContextPrefix("/sys/fs/selinux/enforce", "/usr/bin/runcon")
	if err != nil {
		return err
	}
	capability := remoteConfinementCapability(request, runtimeState.manifest.Tools.Pasta.Version, childContextPrefix)
	definition, err := hauleradapter.NewRegistryServiceDefinition(hauleradapter.ServiceDefinitionOptions{
		HaulerExecutable: filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hauler"),
		StoreDirectory:   filepath.Join(request.RuntimeRoot, "kit", "store"),
		OverlayDirectory: cutRoot, GuestPort: remoteRegistryGuestPort,
		LogPath: filepath.Join(root, "registry.log"), PIDPath: filepath.Join(root, "registry.pid"),
		ReadOnly: true,
	})
	if err != nil {
		return err
	}
	spec, err := remoteServiceSpec(request, definition, capability)
	if err != nil {
		return err
	}
	spec.LaunchToken = request.Checkpoint.AttemptID + "-readonly-registry"
	runtimeState.readonlyRecord, _, err = runtimeState.readonly.Ensure(ctx, snapshot, spec)
	return err
}

func (runtimeState *productionCheckpointRuntime) Inventory(_ context.Context, _ Request, cut registry.Snapshot) (domain.ImageInventory, error) {
	return registry.InventoryFromCatalog("127.0.0.1:5000", cut.References, time.Now().UTC())
}

func (runtimeState *productionCheckpointRuntime) ArchiveRoot(ctx context.Context, request Request, _ registry.Snapshot) (archiveadapter.ArchiveInfo, error) {
	path := filepath.Join(runtimeState.attemptRoot, request.Checkpoint.Capsule+".tar.zst")
	return archiveadapter.NewTarZstd().Create(ctx, request.WorkspaceRoot, path)
}

func (runtimeState *productionCheckpointRuntime) BuildStore(ctx context.Context, request Request, root archiveadapter.ArchiveInfo, inventory domain.ImageInventory) (haulkit.StoreIdentity, error) {
	if err := os.Mkdir(runtimeState.storeRoot, 0o700); err != nil {
		return haulkit.StoreIdentity{}, err
	}
	name := request.Checkpoint.Capsule + ".tar.zst"
	if result, err := runtimeState.hauler.AddFile(ctx, runtimeState.storeRoot, root.Path, name); err != nil || result.ExitCode != 0 {
		return haulkit.StoreIdentity{}, fmt.Errorf("add remote root to fresh Hauler store: %w", err)
	}
	for _, image := range inventory.Images {
		platform := image.Platform.OS + "/" + image.Platform.Architecture
		if image.Platform.OS == "" || image.Platform.Architecture == "" {
			platform = "linux/" + runtime.GOARCH
		}
		if result, err := runtimeState.hauler.AddImage(ctx, runtimeState.storeRoot, hauleradapter.AddImageOptions{
			Reference: image.CapturedReference, Platform: platform,
		}); err != nil || result.ExitCode != 0 {
			return haulkit.StoreIdentity{}, fmt.Errorf("add remote tagged image to fresh Hauler store: %w", err)
		}
	}
	identity, err := runtimeState.hauler.ValidateStore(ctx, runtimeState.storeRoot)
	if err != nil {
		return haulkit.StoreIdentity{}, err
	}
	rootIdentity, err := runtimeState.hauler.ObserveRoot(ctx, runtimeState.storeRoot, runtimeState.rootReference)
	if err != nil || rootIdentity.SHA256 != root.SHA256 || rootIdentity.Size != root.Size {
		return haulkit.StoreIdentity{}, errors.Join(err, ErrIdentityMismatch)
	}
	return identity, nil
}

func (runtimeState *productionCheckpointRuntime) BuildKit(ctx context.Context, request Request, root archiveadapter.ArchiveInfo, store haulkit.StoreIdentity) (haulkit.Artifact, error) {
	if err := os.Mkdir(runtimeState.outputRoot, 0o700); err != nil {
		return haulkit.Artifact{}, err
	}
	builder := haulkit.NewBuilder(runtimeState.hauler)
	artifact, err := builder.Build(ctx, haulkit.BuildRequest{
		SessionID: request.SessionID, Capsule: request.Checkpoint.Capsule,
		Lineage:      request.Checkpoint.Lineage,
		Generation:   &domain.GenerationRef{Generation: request.Checkpoint.Generation},
		Architecture: request.Expected.Architecture, StoreDirectory: runtimeState.storeRoot,
		Root:             haulkit.RootIdentity{Reference: runtimeState.rootReference, SHA256: root.SHA256, Size: root.Size},
		HaulerExecutable: filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hauler"),
		HaulerVersion:    runtimeState.manifest.Tools.Hauler.Version,
		PastaExecutable:  filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "pasta"),
		PastaVersion:     runtimeState.manifest.Tools.Pasta.Version,
		OutputDirectory:  runtimeState.outputRoot,
	})
	if err != nil {
		return haulkit.Artifact{}, err
	}
	observed, err := runtimeState.hauler.ValidateStore(ctx, runtimeState.storeRoot)
	if err != nil {
		return haulkit.Artifact{}, err
	}
	if !reflect.DeepEqual(store, observed) {
		return haulkit.Artifact{}, ErrIdentityMismatch
	}
	return artifact, nil
}

func (runtimeState *productionCheckpointRuntime) Publish(ctx context.Context, request Request, receipt CheckpointReceipt) (CheckpointReceipt, error) {
	body, err := json.Marshal(receipt)
	if err != nil {
		return CheckpointReceipt{}, err
	}
	preparedPath := filepath.Join(runtimeState.attemptRoot, "prepared.json")
	if err := publishCheckpointRegular(preparedPath, body, 0o400); err != nil {
		return CheckpointReceipt{}, err
	}
	if runtimeState.readonly != nil {
		if err := runtimeState.readonly.Stop(ctx, runtimeState.readonlyRecord); err != nil {
			return CheckpointReceipt{}, err
		}
	}
	if err := publishCheckpointExport(request.WorkspaceRoot, receipt); err != nil {
		return CheckpointReceipt{}, err
	}
	path := filepath.Join(runtimeState.attemptRoot, "receipt.json")
	if err := publishCheckpointRegular(path, body, 0o400); err != nil {
		return CheckpointReceipt{}, err
	}
	observed, err := readCheckpointRegular(path, DiagnosticLimit)
	if err != nil || !reflect.DeepEqual(observed, body) {
		return CheckpointReceipt{}, errors.Join(err, ErrIdentityMismatch)
	}
	return receipt, nil
}

func (runtimeState *productionCheckpointRuntime) Resume(ctx context.Context, request Request, evidence ServiceCheckpointEvidence) error {
	if len(runtimeState.serviceSnapshot.Services) == 0 {
		store, err := journal.NewStore(filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "services", "journal"))
		if err != nil {
			return err
		}
		runtimeState.serviceSnapshot, _, err = store.Load(ctx, request.SessionID)
		if err != nil {
			return err
		}
	}
	if evidence.Token == "" || !reflect.DeepEqual(evidence.Services, runtimeState.serviceSnapshot.Services) {
		return ErrServiceEvidence
	}
	receipt, err := launchServiceSupervisor(ctx, request)
	if err != nil {
		return err
	}
	return validateServiceEvidence(receipt.Services)
}

func publishCheckpointExport(workspaceRoot string, receipt CheckpointReceipt) error {
	transferRoot := filepath.Join(workspaceRoot, ".camp", "transfer")
	exportRoot := filepath.Join(transferRoot, "export")
	attemptRoot := filepath.Join(exportRoot, receipt.AttemptID)
	if err := secureMkdirAllOperation(transferRoot); err != nil {
		return err
	}
	if _, err := os.Lstat(exportRoot); errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(exportRoot, 0o700); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, err := os.Lstat(attemptRoot); err == nil {
		return validateCheckpointExport(workspaceRoot, receipt)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage, err := os.MkdirTemp(exportRoot, "."+receipt.AttemptID+".partial-")
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := copyCheckpointIdentity(receipt.Kit.ManifestPath, filepath.Join(stage, "camp-hauler-kit.json"), receipt.Kit.ManifestSHA256, -1); err != nil {
		return err
	}
	for _, chunk := range receipt.Kit.Chunks {
		if err := copyCheckpointIdentity(filepath.Join(filepath.Dir(receipt.Kit.ArchivePath), "chunks", chunk.Name), filepath.Join(stage, chunk.Name), chunk.SHA256, chunk.Size); err != nil {
			return err
		}
	}
	if err := syncCheckpointDirectory(stage); err != nil {
		return err
	}
	if err := os.Rename(stage, attemptRoot); err != nil {
		return err
	}
	committed = true
	if err := syncCheckpointDirectory(exportRoot); err != nil {
		return err
	}
	return validateCheckpointExport(workspaceRoot, receipt)
}

func validateCheckpointExport(workspaceRoot string, receipt CheckpointReceipt) error {
	root := filepath.Join(workspaceRoot, ".camp", "transfer", "export")
	if err := verifyCheckpointDirectory(root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	if len(entries) != 1 || entries[0].Name() != receipt.AttemptID || !entries[0].IsDir() ||
		entries[0].Type()&os.ModeSymlink != 0 {
		return errors.New("remote checkpoint export root contains an unallowlisted entry")
	}
	attempt := filepath.Join(root, receipt.AttemptID)
	if err := verifyCheckpointDirectory(attempt); err != nil {
		return err
	}
	expected := map[string]struct {
		digest string
		size   int64
	}{"camp-hauler-kit.json": {receipt.Kit.ManifestSHA256, -1}}
	for _, chunk := range receipt.Kit.Chunks {
		expected[chunk.Name] = struct {
			digest string
			size   int64
		}{chunk.SHA256, chunk.Size}
	}
	files, err := os.ReadDir(attempt)
	if err != nil || len(files) != len(expected) {
		return errors.Join(err, errors.New("remote checkpoint export differs from its allow-list"))
	}
	for _, file := range files {
		identity, ok := expected[file.Name()]
		if !ok || file.IsDir() || file.Type()&os.ModeSymlink != 0 {
			return errors.New("remote checkpoint export contains an unallowlisted entry")
		}
		if err := verifyCheckpointIdentity(filepath.Join(attempt, file.Name()), identity.digest, identity.size); err != nil {
			return err
		}
	}
	return nil
}

func copyCheckpointIdentity(source, destination, digest string, size int64) error {
	sourceFD, err := unix.Open(source, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	sourceFile := os.NewFile(uintptr(sourceFD), source)
	defer sourceFile.Close()
	before, err := sourceFile.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return ErrIdentityMismatch
	}
	destinationFile, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o400)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(destinationFile, hash), sourceFile)
	syncErr := destinationFile.Sync()
	closeErr := destinationFile.Close()
	after, statErr := sourceFile.Stat()
	named, namedErr := os.Lstat(source)
	if copyErr != nil || syncErr != nil || closeErr != nil || statErr != nil ||
		!os.SameFile(before, after) || written != before.Size() ||
		namedErr != nil || !os.SameFile(before, named) ||
		(size >= 0 && written != size) || hex.EncodeToString(hash.Sum(nil)) != digest {
		return errors.Join(copyErr, syncErr, closeErr, statErr, namedErr, ErrIdentityMismatch)
	}
	return nil
}

func verifyCheckpointIdentity(path, digest string, size int64) error {
	_, err := observeCheckpointFile(path, digest, size)
	return err
}

func readCheckpointRegular(path string, limit int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !checkpointPrivateRegular(before, limit) {
		return nil, ErrIdentityMismatch
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.Join(err, ErrIdentityMismatch)
	}
	after, err := file.Stat()
	named, namedErr := os.Lstat(path)
	if err != nil || namedErr != nil || !os.SameFile(before, after) || !os.SameFile(before, named) || before.Size() != after.Size() {
		return nil, errors.Join(err, ErrIdentityMismatch)
	}
	return body, nil
}

func observeCheckpointFile(path, digest string, size int64) (int64, error) {
	return observeCheckpointFileWithHook(path, digest, size, nil)
}

func observeCheckpointFileWithHook(path, digest string, size int64, afterRead func()) (int64, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return 0, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !checkpointPrivateRegular(before, haulkit.DefaultChunkSize) ||
		(size >= 0 && before.Size() != size) {
		return 0, ErrIdentityMismatch
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, haulkit.DefaultChunkSize+1))
	if err != nil || written > haulkit.DefaultChunkSize {
		return 0, errors.Join(err, ErrIdentityMismatch)
	}
	if afterRead != nil {
		afterRead()
	}
	after, statErr := file.Stat()
	named, namedErr := os.Lstat(path)
	if statErr != nil || namedErr != nil || !os.SameFile(before, after) ||
		!os.SameFile(before, named) || before.Size() != after.Size() ||
		hex.EncodeToString(hash.Sum(nil)) != digest {
		return 0, errors.Join(statErr, namedErr, ErrIdentityMismatch)
	}
	return written, nil
}

func checkpointPrivateRegular(info os.FileInfo, limit int64) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && info.Mode().IsRegular() && info.Size() <= limit &&
		info.Mode().Perm() == 0o400 && stat.Nlink == 1
}

func verifyCheckpointDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o700 {
		return errors.Join(err, ErrIdentityMismatch)
	}
	return nil
}

func publishCheckpointRegular(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := readCheckpointRegular(path, int64(len(body)))
		if readErr == nil && reflect.DeepEqual(existing, body) {
			return nil
		}
		return errors.Join(readErr, ErrIdentityMismatch)
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return errors.Join(writeErr, syncErr, closeErr)
	}
	return syncCheckpointDirectory(filepath.Dir(path))
}

func syncCheckpointDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func checkpointExportNames(receipt CheckpointReceipt) []string {
	result := []string{"camp-hauler-kit.json"}
	for _, chunk := range receipt.Kit.Chunks {
		result = append(result, chunk.Name)
	}
	sort.Strings(result)
	return result
}

var _ checkpointRuntime = (*productionCheckpointRuntime)(nil)

package app

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
	"runtime"
	"strings"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/capsule"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/remoteworker"
	"golang.org/x/sys/unix"
)

const (
	remoteAttemptOwnerMarker = ".camp-attempt-owner"
	remoteAttemptComplete    = ".camp-attempt-complete"
)

type RemoteRootArchiver interface {
	Create(context.Context, string, string) (archiveadapter.ArchiveInfo, error)
}

type RemoteHaulerStore interface {
	haulkit.StoreValidator
	AddFile(context.Context, string, string, string) (ports.Result, error)
	AddImage(context.Context, string, hauleradapter.AddImageOptions) (ports.Result, error)
}

type RemoteImageResolver interface {
	Resolve(context.Context, string) (string, error)
}

type RemoteConfinementResolver interface {
	Resolve(context.Context) (ports.ConfinementCapability, error)
}

type RemoteDataPlaneDependencies struct {
	Root             string
	Archiver         RemoteRootArchiver
	Hauler           RemoteHaulerStore
	Builder          haulkit.Builder
	Verifier         haulkit.Verifier
	Images           RemoteImageResolver
	Confinement      RemoteConfinementResolver
	HaulerExecutable string
	HaulerVersion    string
}

type RemoteDataPlanePreparer struct {
	deps   RemoteDataPlaneDependencies
	render func(capsule.BootstrapRequest) (capsule.Bootstrap, error)
}

func NewRemoteDataPlanePreparer(deps RemoteDataPlaneDependencies) *RemoteDataPlanePreparer {
	return &RemoteDataPlanePreparer{deps: deps, render: capsule.RenderBootstrap}
}

func (p *RemoteDataPlanePreparer) Prepare(ctx context.Context, request RemoteDataPlaneRequest) (RemoteDataPlaneResult, error) {
	if p == nil || p.deps.Archiver == nil || p.deps.Hauler == nil || p.deps.Builder == nil || p.deps.Verifier == nil ||
		p.deps.Images == nil || p.deps.Confinement == nil || p.render == nil || !validRoot(p.deps.Root) ||
		!validRoot(request.Materialization) || !validRoot(request.DevcontainerPath) ||
		request.SessionID == "" || request.AttemptID != request.SessionID+"-hauler-kit-v1" ||
		request.Capsule == "" || request.Lineage.Branch == "" || !filepath.IsAbs(p.deps.HaulerExecutable) || p.deps.HaulerVersion == "" {
		return RemoteDataPlaneResult{}, errors.New("remote Hauler data plane dependencies or request are incomplete")
	}
	attemptRoot := filepath.Join(p.deps.Root, request.AttemptID)
	if err := os.MkdirAll(p.deps.Root, 0o700); err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("create remote data-plane root: %w", err)
	}
	if err := createOwnedAttempt(p.deps.Root, request.AttemptID); err != nil {
		if errors.Is(err, os.ErrExist) {
			if _, markerErr := os.Lstat(filepath.Join(attemptRoot, remoteAttemptComplete)); markerErr == nil {
				return p.reusePrepared(ctx, request, attemptRoot)
			} else if !errors.Is(markerErr, os.ErrNotExist) {
				return RemoteDataPlaneResult{}, markerErr
			}
			if err := removeOwnedPartialAttempt(p.deps.Root, request.AttemptID); err != nil {
				return RemoteDataPlaneResult{}, err
			}
			if err := createOwnedAttempt(p.deps.Root, request.AttemptID); err != nil {
				return RemoteDataPlaneResult{}, fmt.Errorf("recreate remote data-plane attempt: %w", err)
			}
		} else {
			return RemoteDataPlaneResult{}, fmt.Errorf("create remote data-plane attempt: %w", err)
		}
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(attemptRoot)
		}
	}()
	storeRoot := filepath.Join(attemptRoot, "store")
	outputRoot := filepath.Join(attemptRoot, "output")
	if err := os.Mkdir(storeRoot, 0o700); err != nil {
		return RemoteDataPlaneResult{}, err
	}
	if err := os.Mkdir(outputRoot, 0o700); err != nil {
		return RemoteDataPlaneResult{}, err
	}
	rootName := request.Capsule + ".tar.zst"
	rootArchive := filepath.Join(attemptRoot, rootName)
	archiveInfo, err := p.deps.Archiver.Create(ctx, request.Materialization, rootArchive)
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("snapshot remote root: %w", err)
	}
	if archiveInfo.Path != rootArchive || archiveInfo.Size <= 0 || len(archiveInfo.SHA256) != 64 {
		return RemoteDataPlaneResult{}, errors.New("root archive identity is incomplete")
	}
	if result, err := p.deps.Hauler.AddFile(ctx, storeRoot, rootArchive, rootName); err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("add root archive to Hauler store: %w", err)
	} else if result.ExitCode != 0 {
		return RemoteDataPlaneResult{}, fmt.Errorf("add root archive to Hauler store exited %d", result.ExitCode)
	}
	outerImage, err := p.resolveOuterImage(ctx, request.DevcontainerPath)
	if err != nil {
		return RemoteDataPlaneResult{}, err
	}
	if result, err := p.deps.Hauler.AddImage(ctx, storeRoot, hauleradapter.AddImageOptions{
		Reference: outerImage, Platform: "linux/" + runtime.GOARCH,
	}); err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("add immutable devcontainer image to Hauler store: %w", err)
	} else if result.ExitCode != 0 {
		return RemoteDataPlaneResult{}, fmt.Errorf("add immutable devcontainer image to Hauler store exited %d", result.ExitCode)
	}
	confinement, err := p.deps.Confinement.Resolve(ctx)
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("resolve pasta confinement: %w", err)
	}
	artifact, err := p.deps.Builder.Build(ctx, haulkit.BuildRequest{
		SessionID: request.SessionID, Capsule: request.Capsule, Lineage: request.Lineage, Generation: request.Generation,
		Architecture: "linux/" + runtime.GOARCH, StoreDirectory: storeRoot,
		Root:             haulkit.RootIdentity{Reference: "hauler/" + rootName + ":latest", SHA256: archiveInfo.SHA256, Size: archiveInfo.Size},
		CampVersion:      "",
		HaulerExecutable: p.deps.HaulerExecutable, HaulerVersion: p.deps.HaulerVersion,
		PastaExecutable: confinement.Executable, PastaVersion: confinement.Version,
		OutputDirectory: outputRoot,
	})
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("build Camp Hauler Kit v1: %w", err)
	}
	manifestBody, err := os.ReadFile(artifact.ManifestPath)
	if err != nil {
		return RemoteDataPlaneResult{}, err
	}
	manifest, err := haulkit.DecodeCanonical(manifestBody)
	if err != nil {
		return RemoteDataPlaneResult{}, err
	}
	verified, err := p.deps.Verifier.Verify(ctx, haulkit.VerifyRequest{
		ManifestPath: artifact.ManifestPath, ArchivePath: artifact.ArchivePath,
		Architecture: manifest.Architecture, Tools: manifest.Tools, StoreDirectory: storeRoot,
	})
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("verify Camp Hauler Kit v1: %w", err)
	}
	if verified.Manifest.Archive.SHA256 != artifact.SHA256 || verified.Manifest.Archive.Size != artifact.Size ||
		verified.Manifest.Root.SHA256 != archiveInfo.SHA256 || verified.Manifest.Root.Size != archiveInfo.Size {
		return RemoteDataPlaneResult{}, errors.New("verified Camp Hauler Kit v1 identity differs from prepared inputs")
	}
	manifestIdentity := identityForBytes("camp-hauler-kit.json", manifestBody)
	expected := remoteworker.ExpectedIdentity{
		Architecture: manifest.Architecture,
		Helper: remoteworker.FileIdentity{
			Name: "camp", SHA256: manifest.Tools.Camp.SHA256, Size: manifest.Tools.Camp.Size,
		},
		Kit:      remoteworker.FileIdentity{Name: "camp-hauler-kit.tar.zst", SHA256: artifact.SHA256, Size: artifact.Size},
		Manifest: manifestIdentity,
		Image:    outerImage,
	}
	workspaceRoot := filepath.Join("/workspaces", request.Capsule)
	runtimeRoot := filepath.Join("/var/lib/camp", request.SessionID)
	remoteManifest := filepath.Join(runtimeRoot, "camp-hauler-kit.json")
	workerRequest := func(operation remoteworker.Operation) remoteworker.Request {
		return remoteworker.Request{
			SchemaVersion: remoteworker.ProtocolSchemaVersion, Operation: operation, SessionID: request.SessionID,
			WorkspaceRoot: workspaceRoot, RuntimeRoot: runtimeRoot, ManifestPath: remoteManifest, Expected: expected,
		}
	}
	bootstrapRoot := filepath.Join(attemptRoot, "bootstrap")
	bootstrap, err := p.render(capsule.BootstrapRequest{
		Root: bootstrapRoot, DevcontainerPath: request.DevcontainerPath,
		KitArchivePath: artifact.ArchivePath, ManifestPath: artifact.ManifestPath, OuterImage: outerImage,
		InitializeRequest: workerRequest(remoteworker.OperationActivateImage),
		HydrateRequest:    workerRequest(remoteworker.OperationHydrate),
		ServicesRequest:   workerRequest(remoteworker.OperationStartServices),
	})
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("render remote bootstrap: %w", err)
	}
	if bootstrap.Root != bootstrapRoot {
		return RemoteDataPlaneResult{}, errors.New("bootstrap renderer returned an unexpected root")
	}
	if _, err := capsule.VerifyBootstrap(capsule.BootstrapVerificationRequest{Root: bootstrap.Root, Expected: expected}); err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("verify rendered remote bootstrap: %w", err)
	}
	record := domain.RemoteDataPlaneRecord{
		Mode: domain.DataPlaneHaulerKitV1, AttemptID: request.AttemptID, BootstrapRoot: bootstrap.Root,
		KitSHA256: artifact.SHA256, KitSize: artifact.Size,
		ManifestSHA256: manifestIdentity.SHA256, ManifestSize: manifestIdentity.Size, OuterImage: outerImage,
	}
	if err := publishAttemptCompletion(attemptRoot, record); err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("publish remote data-plane completion: %w", err)
	}
	cleanup = false
	return RemoteDataPlaneResult{BootstrapRoot: bootstrap.Root, Record: record}, nil
}

func (p *RemoteDataPlanePreparer) reusePrepared(ctx context.Context, request RemoteDataPlaneRequest, attemptRoot string) (RemoteDataPlaneResult, error) {
	storeRoot := filepath.Join(attemptRoot, "store")
	manifestPath := filepath.Join(attemptRoot, "output", "camp-hauler-kit.json")
	archivePath := filepath.Join(attemptRoot, "output", "camp-hauler-kit.tar.zst")
	bootstrapRoot := filepath.Join(attemptRoot, "bootstrap")
	manifestBody, err := os.ReadFile(manifestPath)
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("observe prior remote data-plane attempt: %w", err)
	}
	manifest, err := haulkit.DecodeCanonical(manifestBody)
	if err != nil {
		return RemoteDataPlaneResult{}, err
	}
	if manifest.SessionID != request.SessionID || manifest.Capsule != request.Capsule || manifest.Lineage != request.Lineage ||
		!sameGenerationRef(manifest.Generation, request.Generation) {
		return RemoteDataPlaneResult{}, errors.New("prior remote data-plane attempt identity differs")
	}
	verified, err := p.deps.Verifier.Verify(ctx, haulkit.VerifyRequest{
		ManifestPath: manifestPath, ArchivePath: archivePath, Architecture: manifest.Architecture,
		Tools: manifest.Tools, StoreDirectory: storeRoot,
	})
	if err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("verify prior Camp Hauler Kit v1: %w", err)
	}
	if verified.Manifest.Archive != manifest.Archive || verified.Manifest.Root != manifest.Root {
		return RemoteDataPlaneResult{}, errors.New("prior verified kit identity differs")
	}
	requestFile, err := os.Open(filepath.Join(bootstrapRoot, ".camp-bootstrap", "initialize-request.json"))
	if err != nil {
		return RemoteDataPlaneResult{}, err
	}
	initialize, decodeErr := remoteworker.DecodeRequest(requestFile)
	closeErr := requestFile.Close()
	if decodeErr != nil || closeErr != nil {
		return RemoteDataPlaneResult{}, errors.Join(decodeErr, closeErr)
	}
	manifestIdentity := identityForBytes("camp-hauler-kit.json", manifestBody)
	if initialize.Operation != remoteworker.OperationActivateImage ||
		initialize.Expected.Kit.SHA256 != manifest.Archive.SHA256 || initialize.Expected.Kit.Size != manifest.Archive.Size ||
		initialize.Expected.Manifest != manifestIdentity || initialize.Expected.Helper.SHA256 != manifest.Tools.Camp.SHA256 ||
		initialize.Expected.Helper.Size != manifest.Tools.Camp.Size || !immutableImage(initialize.Expected.Image) {
		return RemoteDataPlaneResult{}, errors.New("prior bootstrap identity differs from verified kit")
	}
	expected := remoteworker.ExpectedIdentity{
		Architecture: manifest.Architecture,
		Helper:       remoteworker.FileIdentity{Name: "camp", SHA256: manifest.Tools.Camp.SHA256, Size: manifest.Tools.Camp.Size},
		Kit:          initialize.Expected.Kit, Manifest: manifestIdentity, Image: initialize.Expected.Image,
	}
	if _, err := capsule.VerifyBootstrap(capsule.BootstrapVerificationRequest{Root: bootstrapRoot, Expected: expected}); err != nil {
		return RemoteDataPlaneResult{}, fmt.Errorf("verify prior remote bootstrap: %w", err)
	}
	record := domain.RemoteDataPlaneRecord{
		Mode: domain.DataPlaneHaulerKitV1, AttemptID: request.AttemptID, BootstrapRoot: bootstrapRoot,
		KitSHA256: manifest.Archive.SHA256, KitSize: manifest.Archive.Size,
		ManifestSHA256: manifestIdentity.SHA256, ManifestSize: manifestIdentity.Size, OuterImage: initialize.Expected.Image,
	}
	completion, err := readAttemptCompletion(attemptRoot)
	if err != nil || completion != record {
		return RemoteDataPlaneResult{}, errors.New("prior remote data-plane completion marker differs")
	}
	return RemoteDataPlaneResult{BootstrapRoot: bootstrapRoot, Record: record}, nil
}

func sameGenerationRef(left, right *domain.GenerationRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (p *RemoteDataPlanePreparer) resolveOuterImage(ctx context.Context, path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(body, &document); err != nil {
		return "", fmt.Errorf("decode devcontainer image: %w", err)
	}
	var image string
	if raw := document["image"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &image); err != nil {
			return "", errors.New("devcontainer image is not a string")
		}
	}
	if image == "" {
		return "", errors.New("remote data plane requires an image-based devcontainer")
	}
	if immutableImage(image) {
		return image, nil
	}
	digest, err := p.deps.Images.Resolve(ctx, image)
	if err != nil {
		return "", fmt.Errorf("resolve immutable devcontainer image: %w", err)
	}
	resolved := image + "@" + digest
	if !immutableImage(resolved) {
		return "", errors.New("resolved devcontainer image is not immutable")
	}
	return resolved, nil
}

func immutableImage(image string) bool {
	const marker = "@sha256:"
	index := strings.LastIndex(image, marker)
	if index <= 0 || len(image[index+len(marker):]) != 64 {
		return false
	}
	_, err := hex.DecodeString(image[index+len(marker):])
	return err == nil
}

func identityForBytes(name string, body []byte) remoteworker.FileIdentity {
	digest := sha256.Sum256(body)
	return remoteworker.FileIdentity{Name: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
}

func createOwnedAttempt(parent, attemptID string) error {
	stage, err := os.MkdirTemp(parent, ".remote-attempt-stage-")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	marker := filepath.Join(stage, remoteAttemptOwnerMarker)
	file, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.WriteString(attemptID + "\n"); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	directory, err := os.Open(stage)
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, filepath.Join(parent, attemptID), unix.RENAME_NOREPLACE); err != nil {
		return err
	}
	published = true
	return syncRemoteDirectory(parent)
}

func removeOwnedPartialAttempt(parent, attemptID string) error {
	parentDirectory, err := os.Open(parent)
	if err != nil {
		return err
	}
	defer parentDirectory.Close()
	fd, err := unix.Openat(int(parentDirectory.Fd()), attemptID, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	attempt := os.NewFile(uintptr(fd), attemptID)
	defer attempt.Close()
	pinned, err := attempt.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(filepath.Join(parent, attemptID))
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(pinned, current) {
		return errors.New("remote data-plane partial attempt identity changed")
	}
	owner, err := readRelativeAttemptFile(attempt, remoteAttemptOwnerMarker, 4096)
	if err != nil || string(owner) != attemptID+"\n" {
		return errors.New("remote data-plane partial attempt is not Camp-owned")
	}
	if _, err := os.Lstat(filepath.Join(parent, attemptID, remoteAttemptComplete)); err == nil {
		return errors.New("completed remote data-plane attempt cannot be removed as partial")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	quarantine := ""
	for range 10 {
		placeholder, err := os.MkdirTemp(parent, "."+attemptID+".partial-")
		if err != nil {
			return err
		}
		candidate := filepath.Base(placeholder)
		if err := os.Remove(placeholder); err != nil {
			return err
		}
		err = unix.Renameat2(int(parentDirectory.Fd()), attemptID, int(parentDirectory.Fd()), candidate, unix.RENAME_NOREPLACE)
		if err == nil {
			quarantine = candidate
			break
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	if quarantine == "" {
		return errors.New("could not allocate remote data-plane partial quarantine")
	}
	movedFD, err := unix.Openat(int(parentDirectory.Fd()), quarantine, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	moved := os.NewFile(uintptr(movedFD), quarantine)
	defer moved.Close()
	movedInfo, movedErr := moved.Stat()
	if movedErr != nil || !os.SameFile(pinned, movedInfo) {
		return errors.New("quarantined remote data-plane attempt identity changed")
	}
	if err := syncRemoteDirectory(parent); err != nil {
		return err
	}
	if err := removeDirectoryContents(moved); err != nil {
		return err
	}
	currentFD, err := unix.Openat(int(parentDirectory.Fd()), quarantine, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	currentFile := os.NewFile(uintptr(currentFD), quarantine)
	currentInfo, currentErr := currentFile.Stat()
	_ = currentFile.Close()
	if currentErr != nil || !os.SameFile(movedInfo, currentInfo) {
		return errors.New("quarantined remote data-plane attempt changed before removal")
	}
	if err := unix.Unlinkat(int(parentDirectory.Fd()), quarantine, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return syncRemoteDirectory(parent)
}

func removeDirectoryContents(directory *os.File) error {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			fd, err := unix.Openat(int(directory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			child := os.NewFile(uintptr(fd), entry.Name())
			err = removeDirectoryContents(child)
			_ = child.Close()
			if err != nil {
				return err
			}
			if err := unix.Unlinkat(int(directory.Fd()), entry.Name(), unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.Type().IsRegular() {
			if err := unix.Unlinkat(int(directory.Fd()), entry.Name(), 0); err != nil {
				return err
			}
			continue
		}
		return errors.New("remote data-plane partial attempt contains unsupported file type")
	}
	return directory.Sync()
}

func publishAttemptCompletion(attemptRoot string, record domain.RemoteDataPlaneRecord) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	temporary := filepath.Join(attemptRoot, "."+remoteAttemptComplete+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	if err := unix.Renameat2(unix.AT_FDCWD, temporary, unix.AT_FDCWD, filepath.Join(attemptRoot, remoteAttemptComplete), unix.RENAME_NOREPLACE); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return syncRemoteDirectory(attemptRoot)
}

func readAttemptCompletion(attemptRoot string) (domain.RemoteDataPlaneRecord, error) {
	directory, err := os.Open(attemptRoot)
	if err != nil {
		return domain.RemoteDataPlaneRecord{}, err
	}
	defer directory.Close()
	body, err := readRelativeAttemptFile(directory, remoteAttemptComplete, 1<<20)
	if err != nil {
		return domain.RemoteDataPlaneRecord{}, err
	}
	var record domain.RemoteDataPlaneRecord
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return domain.RemoteDataPlaneRecord{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domain.RemoteDataPlaneRecord{}, errors.New("remote data-plane completion marker has trailing JSON")
	}
	return record, nil
}

func readRelativeAttemptFile(parent *os.File, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("remote data-plane marker is invalid")
	}
	return io.ReadAll(io.LimitReader(file, limit+1))
}

func syncRemoteDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

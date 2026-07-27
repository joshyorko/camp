package haulkit

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

type Builder interface {
	Build(context.Context, BuildRequest) (Artifact, error)
}

type StoreValidator interface {
	ValidateStore(context.Context, string) (StoreIdentity, error)
}

type StorePreparer interface {
	PrepareStore(context.Context, string, string) (StoreIdentity, error)
}

type RootObserver interface {
	ObserveRoot(context.Context, string, string) (RootIdentity, error)
}

type RuntimeObserver interface {
	OpenRunningExecutable() (*os.File, error)
	Probe(context.Context, string, string) (string, error)
}

type BuildRequest struct {
	SessionID        string
	Capsule          string
	Lineage          domain.Lineage
	Generation       *domain.GenerationRef
	Architecture     string
	StoreDirectory   string
	Root             RootIdentity
	CampExecutable   string
	CampVersion      string
	HaulerExecutable string
	HaulerVersion    string
	PastaExecutable  string
	PastaVersion     string
	OutputDirectory  string
}

type Artifact struct {
	ManifestPath string
	ArchivePath  string
	SHA256       string
	Size         int64
	Chunks       []ChunkIdentity
}

type KitBuilder struct {
	validator            StoreValidator
	chunkSize            int64
	afterSplit           func(string, []ChunkIdentity) error
	runtimeObserver      RuntimeObserver
	afterStoreValidation func() error
}

func NewBuilder(validator StoreValidator) *KitBuilder {
	return &KitBuilder{validator: validator, chunkSize: DefaultChunkSize}
}

func NewBuilderWithRuntimeObserver(validator StoreValidator, observer RuntimeObserver) *KitBuilder {
	return &KitBuilder{validator: validator, runtimeObserver: observer, chunkSize: DefaultChunkSize}
}

func (builder *KitBuilder) Build(ctx context.Context, request BuildRequest) (Artifact, error) {
	resetAtomicBoundaryOccurrences()
	if builder == nil || builder.validator == nil || !filepath.IsAbs(request.StoreDirectory) ||
		!filepath.IsAbs(request.OutputDirectory) || !filepath.IsAbs(request.HaulerExecutable) ||
		!filepath.IsAbs(request.PastaExecutable) {
		return Artifact{}, errors.New("Camp Hauler kit builder requires absolute paths and a store validator")
	}
	store, err := builder.validator.ValidateStore(ctx, request.StoreDirectory)
	if err != nil {
		return Artifact{}, fmt.Errorf("validate fresh Hauler store: %w", err)
	}
	if builder.afterStoreValidation != nil {
		if err := builder.afterStoreValidation(); err != nil {
			return Artifact{}, err
		}
	}
	preparer, ok := builder.validator.(StorePreparer)
	if !ok {
		return Artifact{}, errors.New("Camp Hauler kit builder requires an official store preparer")
	}
	snapshotRoot, err := os.MkdirTemp(request.OutputDirectory, ".haulkit-snapshot-")
	if err != nil {
		return Artifact{}, err
	}
	snapshotCleanup := true
	defer func() {
		if snapshotCleanup {
			_ = removeDurably(snapshotRoot, request.OutputDirectory)
		}
	}()
	snapshotStore := filepath.Join(snapshotRoot, "store")
	preparedStore, err := preparer.PrepareStore(ctx, request.StoreDirectory, snapshotStore)
	if err != nil {
		return Artifact{}, fmt.Errorf("prepare private Hauler store: %w", err)
	}
	if !storeIdentitiesEqual(store, preparedStore) {
		return Artifact{}, fmt.Errorf("%w: prepared Hauler store differs from validated source", ErrIdentityMismatch)
	}
	rootObserver, ok := builder.validator.(RootObserver)
	if !ok {
		return Artifact{}, errors.New("Camp Hauler kit builder requires an observed root")
	}
	observedRoot, err := rootObserver.ObserveRoot(ctx, snapshotStore, request.Root.Reference)
	if err != nil {
		return Artifact{}, fmt.Errorf("observe prepared Hauler root: %w", err)
	}
	if request.Root.SHA256 != "" && request.Root.SHA256 != observedRoot.SHA256 ||
		request.Root.Size > 0 && request.Root.Size != observedRoot.Size {
		return Artifact{}, fmt.Errorf("%w: caller root expectation differs from observed root", ErrIdentityMismatch)
	}
	preparedRequest := request
	preparedRequest.StoreDirectory = snapshotStore
	preparedRequest.CampExecutable = filepath.Join(snapshotRoot, "camp")
	preparedRequest.HaulerExecutable = filepath.Join(snapshotRoot, "hauler")
	preparedRequest.PastaExecutable = filepath.Join(snapshotRoot, "pasta")
	observer := builder.runtimeObserver
	if observer == nil {
		observer = productionRuntimeObserver{}
	}
	runningCamp, err := observer.OpenRunningExecutable()
	if err != nil {
		return Artifact{}, fmt.Errorf("observe running Camp executable: %w", err)
	}
	defer runningCamp.Close()
	if err := snapshotOpenedRegular(runningCamp, preparedRequest.CampExecutable); err != nil {
		return Artifact{}, err
	}
	for source, destination := range map[string]string{
		request.HaulerExecutable: preparedRequest.HaulerExecutable,
		request.PastaExecutable:  preparedRequest.PastaExecutable,
	} {
		if err := snapshotRegular(source, destination); err != nil {
			return Artifact{}, err
		}
	}
	architecture := "linux/" + runtime.GOARCH
	if request.Architecture != architecture {
		return Artifact{}, fmt.Errorf("%w: caller architecture %q != observed %q", ErrIdentityMismatch, request.Architecture, architecture)
	}
	preparedRequest.Architecture = architecture
	observedVersions := make(map[string]string, 3)
	expectedVersions := map[string]string{"camp": request.CampVersion, "hauler": request.HaulerVersion, "pasta": request.PastaVersion}
	for kind, path := range map[string]string{"camp": preparedRequest.CampExecutable, "hauler": preparedRequest.HaulerExecutable, "pasta": preparedRequest.PastaExecutable} {
		output, err := observer.Probe(ctx, path, kind)
		observed := strings.TrimSpace(output)
		if err != nil {
			return Artifact{}, fmt.Errorf("probe %s runtime identity: %w", kind, err)
		}
		if observed == "" {
			return Artifact{}, fmt.Errorf("%w: observed %s runtime identity is empty", ErrIdentityMismatch, kind)
		}
		if expectedVersions[kind] != "" && !containsExactIdentity(output, expectedVersions[kind]) {
			return Artifact{}, fmt.Errorf("%w: observed %s runtime identity does not match the locked version", ErrIdentityMismatch, kind)
		}
		if expectedVersions[kind] == "" {
			expectedVersions[kind] = observed
		}
		observedVersions[kind] = expectedVersions[kind]
	}
	preparedRequest.CampVersion = expectedVersions["camp"]
	if observedVersions["camp"] != preparedRequest.CampVersion || observedVersions["hauler"] != request.HaulerVersion ||
		observedVersions["pasta"] != request.PastaVersion || preparedStore.HaulerVersion != observedVersions["hauler"] {
		return Artifact{}, fmt.Errorf("%w: caller and observed runtime versions differ", ErrIdentityMismatch)
	}
	tools, err := identifyTools(preparedRequest)
	if err != nil {
		return Artifact{}, err
	}
	archivePath := filepath.Join(request.OutputDirectory, "camp-hauler-kit.tar.zst")
	manifestPath := filepath.Join(request.OutputDirectory, "camp-hauler-kit.json")
	chunkDirectory := filepath.Join(request.OutputDirectory, "chunks")
	for _, path := range []string{archivePath, manifestPath, chunkDirectory} {
		if _, err := os.Lstat(path); err == nil {
			return Artifact{}, fmt.Errorf("output already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Artifact{}, err
		}
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(archivePath)
			_ = os.Remove(manifestPath)
			_ = os.RemoveAll(chunkDirectory)
			_ = syncDirectory(request.OutputDirectory)
		}
	}()
	if err := writeReadyArchive(ctx, archivePath, preparedRequest); err != nil {
		return Artifact{}, err
	}
	archiveDigest, archiveSize, err := hashPath(archivePath)
	if err != nil {
		return Artifact{}, err
	}
	chunks, err := Split(ctx, archivePath, chunkDirectory, builder.chunkSize)
	if err != nil {
		return Artifact{}, err
	}
	if builder.afterSplit != nil {
		if err := builder.afterSplit(chunkDirectory, chunks); err != nil {
			return Artifact{}, err
		}
	}
	reassembledFile, err := os.CreateTemp(request.OutputDirectory, ".haulkit-acceptance-*")
	if err != nil {
		return Artifact{}, err
	}
	reassembledPath := reassembledFile.Name()
	if err := reassembledFile.Close(); err != nil {
		_ = os.Remove(reassembledPath)
		return Artifact{}, err
	}
	if err := os.Remove(reassembledPath); err != nil {
		return Artifact{}, err
	}
	if err := Reassemble(ctx, chunkDirectory, chunks, reassembledPath, builder.chunkSize); err != nil {
		return Artifact{}, fmt.Errorf("reassemble completed Camp Hauler kit: %w", err)
	}
	reassembledDigest, reassembledSize, err := hashPath(reassembledPath)
	_ = os.Remove(reassembledPath)
	if err != nil {
		return Artifact{}, err
	}
	if reassembledDigest != archiveDigest || reassembledSize != archiveSize {
		return Artifact{}, errors.New("reassembled Camp Hauler kit does not match archive")
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Kind:          manifestKind,
		SessionID:     request.SessionID,
		Capsule:       request.Capsule,
		Lineage:       request.Lineage,
		Generation:    request.Generation,
		Architecture:  architecture,
		Store:         preparedStore,
		Root:          observedRoot,
		Tools:         tools,
		Archive:       ArchiveIdentity{SHA256: archiveDigest, Size: archiveSize},
		Chunks:        chunks,
	}
	body, err := MarshalCanonical(manifest)
	if err != nil {
		return Artifact{}, err
	}
	if err := writePrivateAtomic(manifestPath, body); err != nil {
		return Artifact{}, err
	}
	artifact := Artifact{ManifestPath: manifestPath, ArchivePath: archivePath, SHA256: archiveDigest, Size: archiveSize, Chunks: chunks}
	verifiedReady := filepath.Join(request.OutputDirectory, ".haulkit-verified-ready")
	verifiedCleanup := false
	defer func() {
		if verifiedCleanup {
			_ = removeDurably(verifiedReady, request.OutputDirectory)
		}
	}()
	if _, err := NewVerifier(builder.validator).Verify(ctx, VerifyRequest{
		ManifestPath: manifestPath,
		ArchivePath:  archivePath,
		Architecture: architecture,
		Tools:        tools,
		Destination:  verifiedReady,
	}); err != nil {
		return Artifact{}, fmt.Errorf("verify completed Camp Hauler kit: %w", err)
	}
	verifiedCleanup = true
	if err := removeDurably(verifiedReady, request.OutputDirectory); err != nil {
		return Artifact{}, err
	}
	verifiedCleanup = false
	if err := removeDurably(snapshotRoot, request.OutputDirectory); err != nil {
		return Artifact{}, err
	}
	snapshotCleanup = false
	cleanup = false
	return artifact, nil
}

func containsExactIdentity(output, expected string) bool {
	if expected == "" {
		return false
	}
	if strings.TrimSpace(output) == expected {
		return true
	}
	for _, field := range strings.Fields(output) {
		if field == expected {
			return true
		}
	}
	return false
}

type productionRuntimeObserver struct{}

func (productionRuntimeObserver) OpenRunningExecutable() (*os.File, error) {
	if runtime.GOOS == "linux" {
		fd, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		file := os.NewFile(uintptr(fd), "/proc/self/exe")
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return nil, errors.New("running Camp executable is not a regular file")
		}
		return file, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	before, err := os.Stat(executable)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	after, err := os.Stat(executable)
	if err != nil || !os.SameFile(before, opened) || !os.SameFile(opened, after) {
		_ = file.Close()
		return nil, errors.New("running Camp executable identity changed while opening")
	}
	return file, nil
}

func (productionRuntimeObserver) Probe(ctx context.Context, path, kind string) (string, error) {
	argument := "version"
	if kind == "camp" || kind == "pasta" {
		argument = "--version"
	}
	command := exec.CommandContext(ctx, path, argument)
	body, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

func snapshotRegular(source, destination string) error {
	input, err := openRegular(source)
	if err != nil {
		return err
	}
	defer input.Close()
	return snapshotOpenedRegular(input, destination)
}

func snapshotOpenedRegular(input *os.File, destination string) error {
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("runtime source is not a regular file")
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o500)
	if err != nil {
		return err
	}
	if err := runAtomicBoundaryHook("tool-snapshot-write"); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := runAtomicBoundaryHook("tool-snapshot-fsync"); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return err
	}
	return output.Close()
}

func identifyTools(request BuildRequest) (ToolIdentities, error) {
	identities := make([]FileIdentity, 3)
	for index, tool := range []struct {
		name, version, path string
	}{
		{"camp", request.CampVersion, request.CampExecutable},
		{"hauler", request.HaulerVersion, request.HaulerExecutable},
		{"pasta", request.PastaVersion, request.PastaExecutable},
	} {
		digest, size, err := hashPath(tool.path)
		if err != nil {
			return ToolIdentities{}, fmt.Errorf("identify %s executable: %w", tool.name, err)
		}
		if tool.version == "" {
			return ToolIdentities{}, fmt.Errorf("%s runtime identity is empty", tool.name)
		}
		identities[index] = FileIdentity{Name: tool.name, Version: tool.version, SHA256: digest, Size: size}
	}
	return ToolIdentities{Camp: identities[0], Hauler: identities[1], Pasta: identities[2]}, nil
}

type archiveSource struct {
	name       string
	sourcePath string
	directory  bool
	executable bool
	size       int64
}

func writeReadyArchive(ctx context.Context, output string, request BuildRequest) error {
	sources, err := readyArchiveSources(request)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".haulkit-archive-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = temporary.Close()
			_ = os.Remove(name)
		}
	}()
	encoder, err := zstd.NewWriter(temporary, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderCRC(true))
	if err != nil {
		return err
	}
	writer := tar.NewWriter(encoder)
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		mode := int64(0o444)
		typeflag := byte(tar.TypeReg)
		if source.directory {
			mode = 0o555
			typeflag = tar.TypeDir
		} else if source.executable {
			mode = 0o555
		}
		header := &tar.Header{
			Name: source.name, Mode: mode, Size: source.size, Typeflag: typeflag,
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatUSTAR,
		}
		if err := runAtomicBoundaryHook("archive-header-write"); err != nil {
			return err
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if source.directory {
			continue
		}
		file, err := openRegular(source.sourcePath)
		if err != nil {
			return err
		}
		if err := runAtomicBoundaryHook("archive-content-write"); err != nil {
			_ = file.Close()
			return err
		}
		written, copyErr := io.Copy(writer, &contextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != source.size {
			return fmt.Errorf("source %q changed while archiving", source.sourcePath)
		}
	}
	if err := runAtomicBoundaryHook("archive-finalize"); err != nil {
		_ = writer.Close()
		_ = encoder.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := runAtomicBoundaryHook("zstd-finalize"); err != nil {
		_ = encoder.Close()
		return err
	}
	if err := encoder.Close(); err != nil {
		return err
	}
	if err := syncPublish(temporary, output); err != nil {
		return err
	}
	cleanup = false
	return syncDirectory(filepath.Dir(output))
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}

func readyArchiveSources(request BuildRequest) ([]archiveSource, error) {
	sources := []archiveSource{
		{name: "bin", directory: true},
		{name: "bin/camp", sourcePath: request.CampExecutable, executable: true},
		{name: "bin/hauler", sourcePath: request.HaulerExecutable, executable: true},
		{name: "bin/pasta", sourcePath: request.PastaExecutable, executable: true},
		{name: "store", directory: true},
	}
	err := filepath.Walk(request.StoreDirectory, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == request.StoreDirectory {
			return nil
		}
		relative, err := filepath.Rel(request.StoreDirectory, path)
		if err != nil || unsafePath(filepath.ToSlash(relative)) {
			return fmt.Errorf("%w: unsafe store path", ErrUnsafeKit)
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("%w: outer kit source %q is not regular", ErrUnsafeKit, path)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && !info.IsDir() && stat.Nlink != 1 {
			return fmt.Errorf("%w: outer kit source %q is hardlinked", ErrUnsafeKit, path)
		}
		sources = append(sources, archiveSource{
			name: filepath.ToSlash(filepath.Join("store", relative)), sourcePath: path,
			directory: info.IsDir(), size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for index := range sources {
		if sources[index].directory {
			continue
		}
		info, err := os.Lstat(sources[index].sourcePath)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: source is not a regular file", ErrUnsafeKit)
		}
		sources[index].size = info.Size()
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].name < sources[j].name })
	return sources, nil
}

func writePrivateAtomic(output string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(output), ".haulkit-manifest-*")
	if err != nil {
		return err
	}
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	if err := runAtomicBoundaryHook("manifest-write"); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	if err := syncPublish(temporary, output); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(output))
}

func removeDurably(path, parent string) error {
	if err := runAtomicBoundaryHook("cleanup-remove"); err != nil {
		return err
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func hashPath(path string) (string, int64, error) {
	file, err := openRegular(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

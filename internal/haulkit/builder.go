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
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/klauspost/compress/zstd"
)

type Builder interface {
	Build(context.Context, BuildRequest) (Artifact, error)
}

type StoreValidator interface {
	ValidateStore(context.Context, string) (StoreIdentity, error)
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
	validator  StoreValidator
	chunkSize  int64
	afterSplit func(string, []ChunkIdentity) error
}

func NewBuilder(validator StoreValidator) *KitBuilder {
	return &KitBuilder{validator: validator, chunkSize: DefaultChunkSize}
}

func (builder *KitBuilder) Build(ctx context.Context, request BuildRequest) (Artifact, error) {
	if builder == nil || builder.validator == nil || !filepath.IsAbs(request.StoreDirectory) ||
		!filepath.IsAbs(request.OutputDirectory) || !filepath.IsAbs(request.CampExecutable) ||
		!filepath.IsAbs(request.HaulerExecutable) || !filepath.IsAbs(request.PastaExecutable) {
		return Artifact{}, errors.New("Camp Hauler kit builder requires absolute paths and a store validator")
	}
	store, err := builder.validator.ValidateStore(ctx, request.StoreDirectory)
	if err != nil {
		return Artifact{}, fmt.Errorf("validate fresh Hauler store: %w", err)
	}
	if store.HaulerVersion != request.HaulerVersion {
		return Artifact{}, fmt.Errorf("validated Hauler version %q != locked %q", store.HaulerVersion, request.HaulerVersion)
	}
	tools, err := identifyTools(request)
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
		}
	}()
	if err := writeReadyArchive(ctx, archivePath, request); err != nil {
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
		Architecture:  request.Architecture,
		Store:         store,
		Root:          request.Root,
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
	if _, err := NewVerifier(builder.validator).Verify(ctx, VerifyRequest{
		ManifestPath: manifestPath,
		ArchivePath:  archivePath,
		Architecture: request.Architecture,
		Tools:        tools,
	}); err != nil {
		return Artifact{}, fmt.Errorf("verify completed Camp Hauler kit: %w", err)
	}
	cleanup = false
	return artifact, nil
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
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if source.directory {
			continue
		}
		file, err := os.Open(source.sourcePath)
		if err != nil {
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
	if err := writer.Close(); err != nil {
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
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	if err := runAtomicBoundaryHook("write"); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
		return err
	}
	if err := syncPublish(temporary, output); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(output))
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

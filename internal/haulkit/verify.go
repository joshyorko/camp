package haulkit

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"golang.org/x/sys/unix"
)

var (
	ErrUnsafeKit        = errors.New("unsafe Camp Hauler kit")
	ErrIdentityMismatch = errors.New("Camp Hauler kit identity mismatch")
	ErrArchiveLimit     = errors.New("Camp Hauler kit archive limit exceeded")
)

type readyArchiveLimits struct {
	MaxEntries       int
	MaxFileBytes     int64
	MaxExpandedBytes int64
	MaxDecoderMemory uint64
}

type Verifier interface {
	Verify(context.Context, VerifyRequest) (VerifiedKit, error)
}

type VerifyRequest struct {
	ManifestPath   string
	ArchivePath    string
	Architecture   string
	Tools          ToolIdentities
	StoreDirectory string
	Destination    string
}

type VerifiedKit struct {
	Manifest       Manifest
	ReadyDirectory string
}

type KitVerifier struct {
	validator StoreValidator
	limits    readyArchiveLimits
}

func NewVerifier(validator StoreValidator) *KitVerifier {
	return &KitVerifier{validator: validator}
}

func (verifier *KitVerifier) Verify(ctx context.Context, request VerifyRequest) (VerifiedKit, error) {
	if verifier == nil || verifier.validator == nil || !filepath.IsAbs(request.ManifestPath) || !filepath.IsAbs(request.ArchivePath) {
		return VerifiedKit{}, errors.New("Camp Hauler kit verifier requires absolute paths and a store validator")
	}
	body, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return VerifiedKit{}, err
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		return VerifiedKit{}, err
	}
	if request.Architecture != manifest.Architecture || !reflect.DeepEqual(request.Tools, manifest.Tools) {
		return VerifiedKit{}, ErrIdentityMismatch
	}
	digest, size, err := hashPath(request.ArchivePath)
	if err != nil {
		return VerifiedKit{}, err
	}
	if digest != manifest.Archive.SHA256 || size != manifest.Archive.Size {
		return VerifiedKit{}, ErrIdentityMismatch
	}
	limits := verifier.limits
	if limits == (readyArchiveLimits{}) {
		limits = defaultReadyArchiveLimits(manifest.Archive.Size)
	}
	if limits.MaxEntries <= 0 || limits.MaxFileBytes <= 0 || limits.MaxExpandedBytes <= 0 || limits.MaxDecoderMemory < 1<<20 {
		return VerifiedKit{}, ErrArchiveLimit
	}
	stageParent := ""
	if request.Destination == "" {
		stageParent = os.TempDir()
	} else {
		if _, err := os.Lstat(request.Destination); err == nil {
			return VerifiedKit{}, os.ErrExist
		} else if !errors.Is(err, os.ErrNotExist) {
			return VerifiedKit{}, err
		}
		stageParent = filepath.Dir(request.Destination)
	}
	stage, err := os.MkdirTemp(stageParent, ".haulkit-stage-*")
	if err != nil {
		return VerifiedKit{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
			_ = syncDirectory(stageParent)
		}
	}()
	if err := extractReadyArchive(ctx, request.ArchivePath, stage, limits); err != nil {
		return VerifiedKit{}, err
	}
	if err := syncExtractedDirectories(stage); err != nil {
		return VerifiedKit{}, err
	}
	for name, identity := range map[string]FileIdentity{
		"camp": manifest.Tools.Camp, "hauler": manifest.Tools.Hauler, "pasta": manifest.Tools.Pasta,
	} {
		gotDigest, gotSize, err := hashPath(filepath.Join(stage, "bin", name))
		if err != nil {
			return VerifiedKit{}, err
		}
		if gotDigest != identity.SHA256 || gotSize != identity.Size {
			return VerifiedKit{}, fmt.Errorf("%w: %s tool", ErrIdentityMismatch, name)
		}
	}
	storePath := filepath.Join(stage, "store")
	currentStore, err := verifier.validator.ValidateStore(ctx, storePath)
	if err != nil {
		return VerifiedKit{}, err
	}
	if !storeIdentitiesEqual(currentStore, manifest.Store) {
		return VerifiedKit{}, fmt.Errorf("%w: Hauler store drift", ErrIdentityMismatch)
	}
	if !rootMatchesStore(manifest.Root, currentStore) {
		return VerifiedKit{}, fmt.Errorf("%w: root identity is not present in Hauler store", ErrIdentityMismatch)
	}
	if request.StoreDirectory != "" {
		currentSource, err := verifier.validator.ValidateStore(ctx, request.StoreDirectory)
		if err != nil {
			return VerifiedKit{}, err
		}
		if !storeIdentitiesEqual(currentSource, manifest.Store) {
			return VerifiedKit{}, fmt.Errorf("%w: source Hauler store drift", ErrIdentityMismatch)
		}
	}
	result := VerifiedKit{Manifest: manifest}
	if request.Destination != "" {
		if err := runAtomicBoundaryHook("extraction-publish"); err != nil {
			return VerifiedKit{}, err
		}
		if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, request.Destination, unix.RENAME_NOREPLACE); err != nil {
			return VerifiedKit{}, err
		}
		if err := syncDirectory(stageParent); err != nil {
			_ = os.RemoveAll(request.Destination)
			_ = syncDirectory(stageParent)
			return VerifiedKit{}, err
		}
		published = true
		result.ReadyDirectory = request.Destination
	}
	return result, nil
}

func rootMatchesStore(root RootIdentity, store StoreIdentity) bool {
	for _, entry := range store.Entries {
		if entry.Type == "file" && entry.Reference == root.Reference &&
			entry.Digest == root.SHA256 && entry.Size == root.Size {
			return true
		}
	}
	return false
}

func storeIdentitiesEqual(left, right StoreIdentity) bool {
	return reflect.DeepEqual(canonicalStore(left), canonicalStore(right))
}

func canonicalStore(identity StoreIdentity) StoreIdentity {
	identity.Entries = append([]StoreEntry(nil), identity.Entries...)
	sort.Slice(identity.Entries, func(i, j int) bool {
		if identity.Entries[i].Reference != identity.Entries[j].Reference {
			return identity.Entries[i].Reference < identity.Entries[j].Reference
		}
		if identity.Entries[i].Type != identity.Entries[j].Type {
			return identity.Entries[i].Type < identity.Entries[j].Type
		}
		return identity.Entries[i].Platform < identity.Entries[j].Platform
	})
	return identity
}

func extractReadyArchive(ctx context.Context, archive, destination string, limits readyArchiveLimits) error {
	file, err := openRegular(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder, err := zstd.NewReader(
		file,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(limits.MaxDecoderMemory),
		zstd.WithDecoderMaxWindow(max(uint64(limits.MaxFileBytes), limits.MaxDecoderMemory)),
	)
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	seen := make(map[string]struct{})
	var expanded int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if err := validateReadyHeader(header); err != nil {
			return err
		}
		if len(seen) >= limits.MaxEntries {
			return ErrArchiveLimit
		}
		if header.Size < 0 || header.Size > limits.MaxFileBytes || expanded > limits.MaxExpandedBytes-header.Size {
			return ErrArchiveLimit
		}
		expanded += header.Size
		if _, exists := seen[header.Name]; exists {
			return fmt.Errorf("%w: duplicate entry", ErrUnsafeKit)
		}
		seen[header.Name] = struct{}{}
		target := filepath.Join(destination, filepath.FromSlash(header.Name))
		if !strings.HasPrefix(target, destination+string(filepath.Separator)) {
			return fmt.Errorf("%w: path escapes destination", ErrUnsafeKit)
		}
		if header.Typeflag == tar.TypeDir {
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if err := runAtomicBoundaryHook("extraction-write"); err != nil {
			_ = output.Close()
			_ = os.Remove(target)
			return err
		}
		written, copyErr := io.CopyN(output, reader, header.Size)
		if err := runAtomicBoundaryHook("extraction-file-fsync"); err != nil {
			_ = output.Close()
			_ = os.Remove(target)
			return err
		}
		syncErr := output.Sync()
		closeErr := output.Close()
		if copyErr != nil || written != header.Size {
			return fmt.Errorf("extract %q: %w", header.Name, copyErr)
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	for _, required := range []string{"bin", "bin/camp", "bin/hauler", "bin/pasta", "store"} {
		if _, exists := seen[required]; !exists {
			return fmt.Errorf("%w: missing %s", ErrUnsafeKit, required)
		}
	}
	return nil
}

func syncExtractedDirectories(root string) error {
	var directories []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		if err := runAtomicBoundaryHook("extraction-directory-fsync"); err != nil {
			return err
		}
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func defaultReadyArchiveLimits(compressedSize int64) readyArchiveLimits {
	const (
		maxExpanded = int64(256 << 30)
		slack       = int64(64 << 20)
		ratio       = int64(200)
	)
	expanded := maxExpanded
	if compressedSize >= 0 && compressedSize <= (maxExpanded-slack)/ratio {
		expanded = compressedSize*ratio + slack
	}
	file := min(expanded, int64(128<<30))
	memory := uint64(min(file, int64(1<<30)))
	if memory < 1<<20 {
		memory = 1 << 20
	}
	return readyArchiveLimits{
		MaxEntries:       1_000_000,
		MaxFileBytes:     file,
		MaxExpandedBytes: expanded,
		MaxDecoderMemory: memory,
	}
}

func validateReadyHeader(header *tar.Header) error {
	if unsafePath(header.Name) || (header.Name != "bin" && header.Name != "store" &&
		!strings.HasPrefix(header.Name, "bin/") && !strings.HasPrefix(header.Name, "store/")) {
		return fmt.Errorf("%w: unsafe path %q", ErrUnsafeKit, header.Name)
	}
	if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
		return fmt.Errorf("%w: links and special files are prohibited", ErrUnsafeKit)
	}
	mode := int64(0o444)
	if header.Typeflag == tar.TypeDir || strings.HasPrefix(header.Name, "bin/") {
		mode = 0o555
	}
	if header.Format != tar.FormatUSTAR || header.Mode != mode || header.Uid != 0 || header.Gid != 0 ||
		header.Uname != "" || header.Gname != "" || header.Linkname != "" ||
		!header.ModTime.Equal(time.Unix(0, 0).UTC()) || !header.AccessTime.IsZero() ||
		!header.ChangeTime.IsZero() || len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
		return fmt.Errorf("%w: non-deterministic header", ErrUnsafeKit)
	}
	return nil
}

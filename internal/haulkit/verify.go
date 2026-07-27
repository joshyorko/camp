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
	MaxArchiveBytes  int64
	MaxEntries       int
	MaxInodes        int
	MaxFileBytes     int64
	MaxExpandedBytes int64
	MaxDecoderMemory uint64
}

type Verifier interface {
	Verify(context.Context, VerifyRequest) (VerifiedKit, error)
}

type VerifyRequest struct {
	ManifestPath           string
	ExpectedManifestSHA256 string
	ArchivePath            string
	Architecture           string
	Tools                  ToolIdentities
	StoreDirectory         string
	Destination            string
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
	if !validSHA256(request.ExpectedManifestSHA256) {
		return VerifiedKit{}, errors.New("Camp Hauler kit verifier requires a trusted manifest SHA-256")
	}
	body, err := readBoundedRegular(request.ManifestPath, maxManifestBytes)
	if err != nil {
		return VerifiedKit{}, err
	}
	if sha256Bytes(body) != request.ExpectedManifestSHA256 {
		return VerifiedKit{}, fmt.Errorf("%w: manifest authority", ErrIdentityMismatch)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		return VerifiedKit{}, err
	}
	if request.Architecture != manifest.Architecture || !reflect.DeepEqual(request.Tools, manifest.Tools) {
		return VerifiedKit{}, ErrIdentityMismatch
	}
	limits := verifier.limits
	if limits == (readyArchiveLimits{}) {
		limits = defaultReadyArchiveLimits(manifest.Archive.Size)
	}
	if limits.MaxArchiveBytes <= 0 || limits.MaxEntries <= 0 || limits.MaxInodes <= 0 ||
		limits.MaxFileBytes <= 0 || limits.MaxExpandedBytes <= 0 || limits.MaxDecoderMemory < 1<<20 {
		return VerifiedKit{}, ErrArchiveLimit
	}
	archive, digest, size, err := openHashedArchive(request.ArchivePath, limits.MaxArchiveBytes)
	if err != nil {
		return VerifiedKit{}, err
	}
	defer archive.Close()
	if digest != manifest.Archive.SHA256 || size != manifest.Archive.Size {
		return VerifiedKit{}, ErrIdentityMismatch
	}
	if err := runAtomicBoundaryHook("archive-hash-complete"); err != nil {
		return VerifiedKit{}, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return VerifiedKit{}, err
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
	stageOwner, err := openOwnedDirectory(stage)
	if err != nil {
		_ = os.Remove(stage)
		return VerifiedKit{}, err
	}
	defer stageOwner.close()
	published := false
	defer func() {
		if !published {
			_ = stageOwner.remove()
			_ = syncDirectory(stageParent)
		}
	}()
	if err := extractReadyArchive(ctx, archive, stage, limits); err != nil {
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
	rootObserver, ok := verifier.validator.(RootObserver)
	if !ok {
		return VerifiedKit{}, errors.New("Camp Hauler kit verifier requires an observed root")
	}
	observedRoot, err := rootObserver.ObserveRoot(ctx, storePath, manifest.Root.Reference)
	if err != nil || observedRoot != manifest.Root {
		return VerifiedKit{}, fmt.Errorf("%w: root identity differs from extracted Hauler bytes", ErrIdentityMismatch)
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
		stageOwner.name = filepath.Base(request.Destination)
		if err := syncDirectory(stageParent); err != nil {
			_ = stageOwner.remove()
			_ = syncDirectory(stageParent)
			return VerifiedKit{}, err
		}
		published = true
		result.ReadyDirectory = request.Destination
	}
	return result, nil
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

func extractReadyArchive(ctx context.Context, archive *os.File, destination string, limits readyArchiveLimits) error {
	decoder, err := zstd.NewReader(
		archive,
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
	inodes := 0
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
			if inodes >= limits.MaxInodes {
				return ErrArchiveLimit
			}
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			inodes++
			continue
		}
		createdParents, err := missingParentDirectories(destination, filepath.Dir(target))
		if err != nil {
			return err
		}
		if inodes > limits.MaxInodes-createdParents-1 {
			return ErrArchiveLimit
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		inodes += createdParents + 1
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

func missingParentDirectories(root, parent string) (int, error) {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("%w: parent escapes extraction root", ErrUnsafeKit)
	}
	if relative == "." {
		return 0, nil
	}
	current := root
	missing := 0
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing++
			continue
		}
		if err != nil {
			return 0, err
		}
		if !info.IsDir() {
			return 0, fmt.Errorf("%w: extraction parent is not a directory", ErrUnsafeKit)
		}
	}
	return missing, nil
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
		MaxArchiveBytes:  maxArchiveBytes,
		MaxEntries:       1_000_000,
		MaxInodes:        1_000_000,
		MaxFileBytes:     file,
		MaxExpandedBytes: expanded,
		MaxDecoderMemory: memory,
	}
}

func readBoundedRegular(path string, maxBytes int) ([]byte, error) {
	file, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > int64(maxBytes) {
		return nil, ErrArchiveLimit
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, ErrArchiveLimit
	}
	return body, nil
}

func openHashedArchive(path string, maxBytes int64) (*os.File, string, int64, error) {
	file, err := openRegular(path)
	if err != nil {
		return nil, "", 0, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", 0, err
	}
	if info.Size() > maxBytes {
		_ = file.Close()
		return nil, "", 0, ErrArchiveLimit
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil {
		_ = file.Close()
		return nil, "", 0, err
	}
	if size > maxBytes {
		_ = file.Close()
		return nil, "", 0, ErrArchiveLimit
	}
	return file, hex.EncodeToString(hash.Sum(nil)), size, nil
}

func sha256Bytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type ownedDirectory struct {
	parentFD int
	rootFD   int
	name     string
	device   uint64
	inode    uint64
}

func openOwnedDirectory(path string) (*ownedDirectory, error) {
	parentFD, err := unix.Open(filepath.Dir(path), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	rootFD, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = unix.Close(parentFD)
		return nil, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(rootFD, &stat); err != nil {
		_ = unix.Close(rootFD)
		_ = unix.Close(parentFD)
		return nil, err
	}
	return &ownedDirectory{
		parentFD: parentFD, rootFD: rootFD, name: name,
		device: uint64(stat.Dev), inode: stat.Ino,
	}, nil
}

func (owned *ownedDirectory) close() {
	if owned == nil {
		return
	}
	if owned.rootFD >= 0 {
		_ = unix.Close(owned.rootFD)
		owned.rootFD = -1
	}
	if owned.parentFD >= 0 {
		_ = unix.Close(owned.parentFD)
		owned.parentFD = -1
	}
}

func (owned *ownedDirectory) remove() error {
	if owned == nil || owned.rootFD < 0 || owned.parentFD < 0 {
		return errors.New("Camp Hauler kit cleanup descriptor is closed")
	}
	if err := runAtomicBoundaryHook("cleanup-descriptor-open"); err != nil {
		return err
	}
	if err := removeDirectoryContentsAt(owned.rootFD); err != nil {
		return err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(owned.parentFD, owned.name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	if uint64(named.Dev) != owned.device || named.Ino != owned.inode {
		return fmt.Errorf("%w: cleanup target identity changed", ErrUnsafeKit)
	}
	return unix.Unlinkat(owned.parentFD, owned.name, unix.AT_REMOVEDIR)
}

func removeDirectoryContentsAt(directoryFD int) error {
	if _, err := unix.Seek(directoryFD, 0, io.SeekStart); err != nil {
		return err
	}
	readFD, err := unix.Dup(directoryFD)
	if err != nil {
		return err
	}
	reader := os.NewFile(uintptr(readFD), "owned-cleanup-directory")
	entries, readErr := reader.ReadDir(-1)
	closeErr := reader.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, entry := range entries {
		name := entry.Name()
		childFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err == nil {
			var child unix.Stat_t
			if err := unix.Fstat(childFD, &child); err != nil {
				_ = unix.Close(childFD)
				return err
			}
			if err := removeDirectoryContentsAt(childFD); err != nil {
				_ = unix.Close(childFD)
				return err
			}
			if err := unix.Close(childFD); err != nil {
				return err
			}
			var named unix.Stat_t
			if err := unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return err
			}
			if named.Dev != child.Dev || named.Ino != child.Ino {
				return fmt.Errorf("%w: cleanup child identity changed", ErrUnsafeKit)
			}
			if err := unix.Unlinkat(directoryFD, name, unix.AT_REMOVEDIR); err != nil {
				return err
			}
			continue
		}
		if !errors.Is(err, unix.ENOTDIR) && !errors.Is(err, unix.ELOOP) {
			return err
		}
		if err := unix.Unlinkat(directoryFD, name, 0); err != nil {
			return err
		}
	}
	return nil
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

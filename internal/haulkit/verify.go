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
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

var (
	ErrUnsafeKit        = errors.New("unsafe Camp Hauler kit")
	ErrIdentityMismatch = errors.New("Camp Hauler kit identity mismatch")
)

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
	limits    verifyLimits
}

func NewVerifier(validator StoreValidator) *KitVerifier {
	return &KitVerifier{validator: validator, limits: defaultVerifyLimits()}
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
	if digestBody(body) != request.ExpectedManifestSHA256 {
		return VerifiedKit{}, fmt.Errorf("%w: manifest authority", ErrIdentityMismatch)
	}
	manifest, err := DecodeCanonical(body)
	if err != nil {
		return VerifiedKit{}, err
	}
	if request.Architecture != manifest.Architecture || !reflect.DeepEqual(request.Tools, manifest.Tools) {
		return VerifiedKit{}, ErrIdentityMismatch
	}
	digest, size, err := hashPathBounded(request.ArchivePath, maxArchiveBytes)
	if err != nil {
		return VerifiedKit{}, err
	}
	if digest != manifest.Archive.SHA256 || size != manifest.Archive.Size {
		return VerifiedKit{}, ErrIdentityMismatch
	}
	destination := request.Destination
	temporary := false
	created := false
	var owned destinationIdentity
	if destination == "" {
		destination, err = os.MkdirTemp("", "camp-haulkit-verify-")
		if err != nil {
			return VerifiedKit{}, err
		}
		temporary = true
		created = true
	} else if !filepath.IsAbs(destination) {
		return VerifiedKit{}, errors.New("Camp Hauler kit verifier requires an absolute destination")
	}
	if !temporary {
		if err := os.Mkdir(destination, 0o700); err != nil {
			return VerifiedKit{}, err
		}
		created = true
	}
	owned, err = identifyDestination(destination)
	if err != nil {
		_ = os.Remove(destination)
		return VerifiedKit{}, err
	}
	success := false
	defer func() {
		if created && (temporary || !success) {
			_ = removeOwnedDestination(destination, owned)
		}
	}()
	if err := extractReadyArchive(ctx, request.ArchivePath, destination, verifier.limits); err != nil {
		return VerifiedKit{}, err
	}
	for name, identity := range map[string]FileIdentity{
		"camp": manifest.Tools.Camp, "hauler": manifest.Tools.Hauler, "pasta": manifest.Tools.Pasta,
	} {
		gotDigest, gotSize, err := hashPath(filepath.Join(destination, "bin", name))
		if err != nil {
			return VerifiedKit{}, err
		}
		if gotDigest != identity.SHA256 || gotSize != identity.Size {
			return VerifiedKit{}, fmt.Errorf("%w: %s tool", ErrIdentityMismatch, name)
		}
	}
	storePath := filepath.Join(destination, "store")
	currentStore, err := verifier.validator.ValidateStore(ctx, storePath)
	if err != nil {
		return VerifiedKit{}, err
	}
	if !storeIdentitiesEqual(currentStore, manifest.Store) {
		return VerifiedKit{}, fmt.Errorf("%w: Hauler store drift", ErrIdentityMismatch)
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
	rootDigest, rootSize, err := hashPathBounded(filepath.Join(storePath, manifest.Root.Reference), verifier.limits.maxExtractedBytes)
	if err != nil {
		return VerifiedKit{}, err
	}
	if rootDigest != manifest.Root.SHA256 || rootSize != manifest.Root.Size {
		return VerifiedKit{}, fmt.Errorf("%w: root", ErrIdentityMismatch)
	}
	result := VerifiedKit{Manifest: manifest}
	if !temporary {
		result.ReadyDirectory = destination
	}
	success = true
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

type verifyLimits struct {
	maxExtractedBytes int64
	maxEntries        int
	maxInodes         int
}

func defaultVerifyLimits() verifyLimits {
	return verifyLimits{
		maxExtractedBytes: 64 << 30,
		maxEntries:        100_000,
		maxInodes:         100_000,
	}
}

func extractReadyArchive(ctx context.Context, archive, destination string, limits verifyLimits) error {
	if limits.maxExtractedBytes <= 0 || limits.maxEntries <= 0 || limits.maxInodes <= 0 {
		return resourceLimit("invalid verifier limits")
	}
	file, err := openRegular(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder, err := zstd.NewReader(
		file,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(128<<20),
		zstd.WithDecoderMaxWindow(64<<20),
	)
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	seen := make(map[string]struct{})
	var extractedBytes int64
	entries := 0
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
		entries++
		if entries > limits.maxEntries {
			return resourceLimit("archive entries")
		}
		if _, exists := seen[header.Name]; exists {
			return fmt.Errorf("%w: duplicate entry", ErrUnsafeKit)
		}
		parent := path.Dir(header.Name)
		if parent != "." {
			if _, exists := seen[parent]; !exists {
				return fmt.Errorf("%w: parent directory is not an earlier archive entry", ErrUnsafeKit)
			}
		}
		seen[header.Name] = struct{}{}
		inodes++
		if inodes > limits.maxInodes {
			return resourceLimit("extracted inodes")
		}
		if header.Size < 0 || extractedBytes > limits.maxExtractedBytes-header.Size {
			return resourceLimit("extracted bytes")
		}
		extractedBytes += header.Size
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
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		written, copyErr := io.CopyN(output, reader, header.Size)
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
		return nil, resourceLimit("manifest bytes")
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBytes {
		return nil, resourceLimit("manifest bytes")
	}
	return body, nil
}

func hashPathBounded(path string, maxBytes int64) (string, int64, error) {
	file, err := openRegular(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() > maxBytes {
		return "", 0, resourceLimit("file bytes")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", 0, err
	}
	if size > maxBytes {
		return "", 0, resourceLimit("file bytes")
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func digestBody(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

type destinationIdentity struct {
	device uint64
	inode  uint64
}

func identifyDestination(path string) (destinationIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return destinationIdentity{}, err
	}
	if !info.IsDir() {
		return destinationIdentity{}, fmt.Errorf("%w: destination is not a directory", ErrUnsafeKit)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return destinationIdentity{}, errors.New("Camp Hauler kit destination identity unavailable")
	}
	return destinationIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func removeOwnedDestination(path string, expected destinationIdentity) error {
	current, err := identifyDestination(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || current != expected {
		return err
	}
	return os.RemoveAll(path)
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

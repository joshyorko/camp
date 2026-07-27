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
)

var (
	ErrUnsafeKit        = errors.New("unsafe Camp Hauler kit")
	ErrIdentityMismatch = errors.New("Camp Hauler kit identity mismatch")
)

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
	destination := request.Destination
	temporary := false
	if destination == "" {
		destination, err = os.MkdirTemp("", "camp-haulkit-verify-")
		if err != nil {
			return VerifiedKit{}, err
		}
		temporary = true
	}
	if temporary {
		defer os.RemoveAll(destination)
	} else if err := os.Mkdir(destination, 0o700); err != nil {
		return VerifiedKit{}, err
	}
	if err := extractReadyArchive(ctx, request.ArchivePath, destination); err != nil {
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
	result := VerifiedKit{Manifest: manifest}
	if !temporary {
		result.ReadyDirectory = destination
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

func extractReadyArchive(ctx context.Context, archive, destination string) error {
	file, err := openRegular(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	seen := make(map[string]struct{})
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

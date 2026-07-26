package coordination

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var ErrGenerationVerification = errors.New("generation verification failed")

type GenerationRecord struct {
	Metadata domain.GenerationMetadata
	Archive  ports.ObjectMeta
	Sidecar  ports.ObjectMeta
}

type ExactGenerationRecord struct {
	Metadata           domain.GenerationMetadata
	MetadataBytes      []byte
	Archive            ports.ObjectMeta
	Sidecar            ports.ObjectMeta
	ArchiveSource      ports.RestartableSource
	SidecarSource      ports.RestartableSource
	ArchiveFingerprint ports.ObjectSourceFingerprint
	SidecarFingerprint ports.ObjectSourceFingerprint
	store              ports.ObjectStore
}

// RevalidateSources confirms that sources reopened after exact resolution
// still identify the same immutable archive and metadata sidecar.
func (r ExactGenerationRecord) RevalidateSources(ctx context.Context) error {
	if r.store == nil {
		return fmt.Errorf("exact generation has no source store: %w", ErrGenerationVerification)
	}
	if err := revalidateSource(ctx, r.store, r.Metadata.ObjectKey, r.ArchiveFingerprint); err != nil {
		return fmt.Errorf("revalidate generation archive: %w", err)
	}
	if err := revalidateSource(ctx, r.store, r.Metadata.MetadataKey, r.SidecarFingerprint); err != nil {
		return fmt.Errorf("revalidate generation metadata: %w", err)
	}
	return nil
}

func revalidateSource(ctx context.Context, store ports.ObjectStore, key string, expected ports.ObjectSourceFingerprint) error {
	meta, err := store.Head(ctx, key)
	if err != nil {
		return err
	}
	if meta.Size != expected.Size || meta.SHA256 != expected.SHA256 || (expected.Revision != "" && meta.Revision != expected.Revision) {
		return fmt.Errorf("source %q fingerprint changed: got size=%d sha256=%s revision=%s", key, meta.Size, meta.SHA256, meta.Revision)
	}
	if identityStore, ok := store.(ports.ObjectStoreIdentity); ok {
		identity, err := identityStore.SourceIdentity(key)
		if err != nil {
			return err
		}
		if expected.CanonicalPath != "" && identity.CanonicalPath != expected.CanonicalPath ||
			expected.Device != 0 && identity.Device != expected.Device ||
			expected.Inode != 0 && identity.Inode != expected.Inode {
			return fmt.Errorf("source %q identity changed", key)
		}
	}
	return nil
}

type GenerationRepository struct {
	store ports.ObjectStore
}

func NewGenerationRepository(store ports.ObjectStore) *GenerationRepository {
	return &GenerationRepository{store: store}
}

func GenerationObjectKey(capsule string, lineage domain.Lineage, generation domain.GenerationRef) (string, error) {
	if _, err := lineage.PointerKey(capsule); err != nil {
		return "", err
	}
	if err := validateGenerationRef(generation); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/generations/%d-%s.tar.zst", capsule, generation.Generation, generation.ArchiveSHA256), nil
}

func GenerationMetadataKey(capsule string, lineage domain.Lineage, generation domain.GenerationRef) (string, error) {
	prefix, err := generationMetadataPrefix(capsule, lineage)
	if err != nil {
		return "", err
	}
	if err := validateGenerationRef(generation); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d-%s.json", prefix, generation.Generation, generation.ArchiveSHA256), nil
}

func (r *GenerationRepository) PutAndVerify(ctx context.Context, metadata domain.GenerationMetadata, source ports.RestartableSource) (GenerationRecord, error) {
	if source == nil {
		return GenerationRecord{}, fmt.Errorf("generation source is nil: %w", ErrGenerationVerification)
	}
	if err := validateGenerationMetadata(metadata, metadata.Capsule, metadata.Lineage, metadata.Generation); err != nil {
		return GenerationRecord{}, err
	}
	if !metadata.Verified.LocalHaulLoadable {
		return GenerationRecord{}, fmt.Errorf("generation was not proven locally loadable: %w", ErrGenerationVerification)
	}
	archive, err := r.store.PutImmutable(ctx, metadata.ObjectKey, source, metadata.Generation.ArchiveSHA256, metadata.Size)
	if err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			if reconciled, headErr := r.store.Head(ctx, metadata.ObjectKey); headErr == nil && reconciled.Size == metadata.Size && reconciled.SHA256 == metadata.Generation.ArchiveSHA256 {
				archive = reconciled
				err = nil
			}
		}
		if err != nil {
			return GenerationRecord{}, err
		}
	}
	if err := r.verifyArchive(ctx, metadata, archive); err != nil {
		return GenerationRecord{}, err
	}
	metadata.Verified.RemoteBytesVerified = true
	body, err := json.Marshal(metadata)
	if err != nil {
		return GenerationRecord{}, fmt.Errorf("marshal generation sidecar: %w", err)
	}
	sidecar, err := r.store.PutImmutable(ctx, metadata.MetadataKey, restartableBytes(body), sha256Hex(body), int64(len(body)))
	if err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			if reconciled, headErr := r.store.Head(ctx, metadata.MetadataKey); headErr == nil && reconciled.Size == int64(len(body)) && reconciled.SHA256 == sha256Hex(body) {
				sidecar = reconciled
				err = nil
			}
		}
		if err != nil {
			return GenerationRecord{}, err
		}
	}
	return GenerationRecord{Metadata: metadata, Archive: archive, Sidecar: sidecar}, nil
}

func (r *GenerationRepository) ReadMetadata(ctx context.Context, capsule string, lineage domain.Lineage, generation domain.GenerationRef) (domain.GenerationMetadata, ports.ObjectMeta, error) {
	key, err := GenerationMetadataKey(capsule, lineage, generation)
	if err != nil {
		return domain.GenerationMetadata{}, ports.ObjectMeta{}, err
	}
	metadata, meta, err := readJSON[domain.GenerationMetadata](ctx, r.store, key)
	if err != nil {
		return domain.GenerationMetadata{}, ports.ObjectMeta{}, err
	}
	if err := validateStoredGenerationMetadata(metadata, capsule, lineage, generation); err != nil {
		return domain.GenerationMetadata{}, ports.ObjectMeta{}, err
	}
	return metadata, meta, nil
}

func (r *GenerationRepository) ResolveExactGeneration(ctx context.Context, capsule string, lineage domain.Lineage, generation domain.GenerationRef) (ExactGenerationRecord, error) {
	objectKey, err := GenerationObjectKey(capsule, lineage, generation)
	if err != nil {
		return ExactGenerationRecord{}, err
	}
	metadataKey, err := GenerationMetadataKey(capsule, lineage, generation)
	if err != nil {
		return ExactGenerationRecord{}, err
	}

	rawMetadata, metadata, sidecarMeta, err := readJSONWithBytes[domain.GenerationMetadata](ctx, r.store, metadataKey)
	if err != nil {
		return ExactGenerationRecord{}, err
	}
	if err := validateStoredGenerationMetadata(metadata, capsule, lineage, generation); err != nil {
		return ExactGenerationRecord{}, err
	}

	archiveMeta, err := r.store.Head(ctx, objectKey)
	if err != nil {
		return ExactGenerationRecord{}, err
	}
	if err := r.verifyArchive(ctx, metadata, archiveMeta); err != nil {
		return ExactGenerationRecord{}, err
	}
	if sidecarMeta.Size != int64(len(rawMetadata)) {
		return ExactGenerationRecord{}, fmt.Errorf("metadata sidecar size %d does not match read bytes %d: %w", sidecarMeta.Size, len(rawMetadata), ErrGenerationVerification)
	}
	if got := sha256Hex(rawMetadata); got != sidecarMeta.SHA256 {
		return ExactGenerationRecord{}, fmt.Errorf("metadata sidecar sha256 %s does not match %s: %w", got, sidecarMeta.SHA256, ErrGenerationVerification)
	}

	archiveFingerprint := ports.ObjectSourceFingerprint{
		Key:      metadata.ObjectKey,
		Revision: archiveMeta.Revision,
		Size:     archiveMeta.Size,
		SHA256:   archiveMeta.SHA256,
	}
	sidecarFingerprint := ports.ObjectSourceFingerprint{
		Key:      metadata.MetadataKey,
		Revision: sidecarMeta.Revision,
		Size:     sidecarMeta.Size,
		SHA256:   sidecarMeta.SHA256,
	}

	if identityStore, ok := r.store.(ports.ObjectStoreIdentity); ok {
		archiveIdentity, err := identityStore.SourceIdentity(metadata.ObjectKey)
		if err != nil {
			return ExactGenerationRecord{}, fmt.Errorf("read archive source identity %q: %w", metadata.ObjectKey, err)
		}
		if archiveIdentity.Key != metadata.ObjectKey {
			return ExactGenerationRecord{}, fmt.Errorf("archive source identity key %q does not match metadata object key %q: %w", archiveIdentity.Key, metadata.ObjectKey, ErrGenerationVerification)
		}
		if archiveIdentity.Revision != "" && archiveIdentity.Revision != archiveMeta.Revision {
			return ExactGenerationRecord{}, fmt.Errorf("archive source identity revision %q does not match %q: %w", archiveIdentity.Revision, archiveMeta.Revision, ErrGenerationVerification)
		}
		if archiveIdentity.Size > 0 && archiveIdentity.Size != archiveMeta.Size {
			return ExactGenerationRecord{}, fmt.Errorf("archive source identity size %d does not match %d: %w", archiveIdentity.Size, archiveMeta.Size, ErrGenerationVerification)
		}
		if archiveIdentity.SHA256 != "" && archiveIdentity.SHA256 != archiveMeta.SHA256 {
			return ExactGenerationRecord{}, fmt.Errorf("archive source identity sha256 %s does not match %s: %w", archiveIdentity.SHA256, archiveMeta.SHA256, ErrGenerationVerification)
		}
		archiveFingerprint = archiveIdentity

		sidecarIdentity, err := identityStore.SourceIdentity(metadata.MetadataKey)
		if err != nil {
			return ExactGenerationRecord{}, fmt.Errorf("read metadata source identity %q: %w", metadata.MetadataKey, err)
		}
		if sidecarIdentity.Key != metadata.MetadataKey {
			return ExactGenerationRecord{}, fmt.Errorf("metadata source identity key %q does not match sidecar key %q: %w", sidecarIdentity.Key, metadata.MetadataKey, ErrGenerationVerification)
		}
		if sidecarIdentity.Revision != "" && sidecarIdentity.Revision != sidecarMeta.Revision {
			return ExactGenerationRecord{}, fmt.Errorf("metadata source identity revision %q does not match %q: %w", sidecarIdentity.Revision, sidecarMeta.Revision, ErrGenerationVerification)
		}
		if sidecarIdentity.Size > 0 && sidecarIdentity.Size != sidecarMeta.Size {
			return ExactGenerationRecord{}, fmt.Errorf("metadata source identity size %d does not match %d: %w", sidecarIdentity.Size, sidecarMeta.Size, ErrGenerationVerification)
		}
		if sidecarIdentity.SHA256 != "" && sidecarIdentity.SHA256 != sidecarMeta.SHA256 {
			return ExactGenerationRecord{}, fmt.Errorf("metadata source identity sha256 %s does not match %s: %w", sidecarIdentity.SHA256, sidecarMeta.SHA256, ErrGenerationVerification)
		}
		sidecarFingerprint = sidecarIdentity
	}

	return ExactGenerationRecord{
		Metadata:           metadata,
		MetadataBytes:      rawMetadata,
		Archive:            archiveMeta,
		Sidecar:            sidecarMeta,
		ArchiveSource:      restartableObjectSource{store: r.store, key: metadata.ObjectKey},
		SidecarSource:      restartableObjectSource{store: r.store, key: metadata.MetadataKey},
		ArchiveFingerprint: archiveFingerprint,
		SidecarFingerprint: sidecarFingerprint,
		store:              r.store,
	}, nil
}

func validateStoredGenerationMetadata(metadata domain.GenerationMetadata, capsule string, lineage domain.Lineage, generation domain.GenerationRef) error {
	if err := validateGenerationMetadata(metadata, capsule, lineage, generation); err != nil {
		return err
	}
	if !metadata.Verified.LocalHaulLoadable || !metadata.Verified.RemoteBytesVerified {
		return fmt.Errorf("stored generation sidecar is not fully verified: %w", ErrGenerationVerification)
	}
	return nil
}

func (r *GenerationRepository) verifyArchive(ctx context.Context, metadata domain.GenerationMetadata, archive ports.ObjectMeta) error {
	if archive.Size != metadata.Size {
		return fmt.Errorf("remote generation size %d does not match %d: %w", archive.Size, metadata.Size, ErrGenerationVerification)
	}
	if archive.SHA256 != "" {
		if archive.SHA256 != metadata.Generation.ArchiveSHA256 {
			return fmt.Errorf("remote generation sha256 %s does not match %s: %w", archive.SHA256, metadata.Generation.ArchiveSHA256, ErrGenerationVerification)
		}
		return nil
	}
	reader, _, err := r.store.Get(ctx, metadata.ObjectKey)
	if err != nil {
		return err
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, reader)
	closeErr := reader.Close()
	if readErr != nil {
		return fmt.Errorf("read back remote generation: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close remote generation readback: %w", closeErr)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	if size != metadata.Size || got != metadata.Generation.ArchiveSHA256 {
		return fmt.Errorf("remote readback has size %d and sha256 %s: %w", size, got, ErrGenerationVerification)
	}
	return nil
}

func validateGenerationMetadata(metadata domain.GenerationMetadata, capsule string, lineage domain.Lineage, generation domain.GenerationRef) error {
	if metadata.SchemaVersion != domain.SchemaVersion || metadata.Capsule != capsule || metadata.Lineage != lineage || metadata.Generation != generation {
		return fmt.Errorf("generation metadata identity or schema mismatch: %w", ErrInvalidDocument)
	}
	if err := validateGenerationRef(metadata.Generation); err != nil {
		return err
	}
	if err := validateGenerationParent(metadata.Generation, metadata.Parent); err != nil {
		return err
	}
	wantObject, err := GenerationObjectKey(capsule, lineage, generation)
	if err != nil {
		return err
	}
	wantMetadata, err := GenerationMetadataKey(capsule, lineage, generation)
	if err != nil {
		return err
	}
	if metadata.ObjectKey != wantObject || metadata.MetadataKey != wantMetadata || metadata.Size < 0 || metadata.CreatedAt.IsZero() || metadata.SessionID == "" {
		return fmt.Errorf("generation metadata has invalid keys or publication fields: %w", ErrInvalidDocument)
	}
	return nil
}

func validateGenerationRef(generation domain.GenerationRef) error {
	if generation.Generation == 0 || len(generation.ArchiveSHA256) != sha256.Size*2 || generation.ArchiveSHA256 != strings.ToLower(generation.ArchiveSHA256) {
		return fmt.Errorf("invalid generation reference: %w", ErrInvalidDocument)
	}
	decoded, err := hex.DecodeString(generation.ArchiveSHA256)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid generation sha256: %w", ErrInvalidDocument)
	}
	return nil
}

func validateGenerationParent(child domain.GenerationRef, parent *domain.GenerationRef) error {
	if parent == nil {
		return nil
	}
	if err := validateGenerationRef(*parent); err != nil {
		return err
	}
	if parent.Generation >= child.Generation {
		return fmt.Errorf("parent generation %d does not precede child generation %d: %w", parent.Generation, child.Generation, ErrInvalidDocument)
	}
	return nil
}

func generationMetadataPrefix(capsule string, lineage domain.Lineage) (string, error) {
	pointerKey, err := lineage.PointerKey(capsule)
	if err != nil {
		return "", err
	}
	return path.Join(path.Dir(pointerKey), "generations") + "/", nil
}

type restartableBytes []byte

func (b restartableBytes) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b)), nil
}

type restartableObjectSource struct {
	store ports.ObjectStore
	key   string
}

func (o restartableObjectSource) Open() (io.ReadCloser, error) {
	reader, _, err := o.store.Get(context.Background(), o.key)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

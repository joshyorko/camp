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

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

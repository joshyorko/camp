package coordination

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

const maxCoordinationDocumentSize = 1 << 20

var (
	ErrInvalidDocument = errors.New("invalid coordination document")
	ErrPointerChanged  = errors.New("pointer changed")
)

type PointerRecord struct {
	Pointer  domain.LatestPointer
	Revision ports.Revision
}

type PointerRepository struct {
	store ports.ObjectStore
}

func NewPointerRepository(store ports.ObjectStore) *PointerRepository {
	return &PointerRepository{store: store}
}

func (r *PointerRepository) Read(ctx context.Context, capsule string, lineage domain.Lineage) (PointerRecord, error) {
	key, err := lineage.PointerKey(capsule)
	if err != nil {
		return PointerRecord{}, err
	}
	pointer, meta, err := readJSON[domain.LatestPointer](ctx, r.store, key)
	if err != nil {
		return PointerRecord{}, err
	}
	if err := validatePointer(pointer, capsule, lineage); err != nil {
		return PointerRecord{}, err
	}
	return PointerRecord{Pointer: pointer, Revision: meta.Revision}, nil
}

func (r *PointerRepository) List(ctx context.Context) ([]PointerRecord, error) {
	var records []PointerRecord
	token := ""
	for {
		items, next, err := r.store.List(ctx, "", token)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			capsule, lineage, ok := pointerIdentityFromKey(item.Key)
			if !ok {
				continue
			}
			record, err := r.Read(ctx, capsule, lineage)
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		if next == "" {
			break
		}
		token = next
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Pointer.Capsule != records[j].Pointer.Capsule {
			return records[i].Pointer.Capsule < records[j].Pointer.Capsule
		}
		return records[i].Pointer.Lineage.Branch < records[j].Pointer.Lineage.Branch
	})
	return records, nil
}

func pointerIdentityFromKey(key string) (string, domain.Lineage, bool) {
	parts := strings.Split(key, "/")
	if len(parts) == 2 && parts[1] == "latest.json" {
		return parts[0], domain.Lineage{Branch: "main"}, parts[0] != ""
	}
	if len(parts) == 4 && parts[1] == "branches" && parts[3] == "latest.json" {
		return parts[0], domain.Lineage{Branch: parts[2]}, parts[0] != "" && parts[2] != ""
	}
	return "", domain.Lineage{}, false
}

func (r *PointerRepository) Create(ctx context.Context, next domain.LatestPointer) (PointerRecord, error) {
	if err := validatePointer(next, next.Capsule, next.Lineage); err != nil {
		return PointerRecord{}, err
	}
	if !next.Lineage.IsMain() && next.Parent == nil {
		return PointerRecord{}, fmt.Errorf("branch pointer has no source parent: %w", ErrInvalidDocument)
	}
	key, err := next.Lineage.PointerKey(next.Capsule)
	if err != nil {
		return PointerRecord{}, err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return PointerRecord{}, fmt.Errorf("marshal pointer %q: %w", key, err)
	}
	meta, err := r.store.PutConditional(ctx, key, body, ports.WriteCondition{MustBeAbsent: true})
	if err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			if reconciled, readErr := r.Read(ctx, next.Capsule, next.Lineage); readErr == nil && documentsEqual(reconciled.Pointer, next) {
				return reconciled, nil
			}
		}
		return PointerRecord{}, err
	}
	return PointerRecord{Pointer: next, Revision: meta.Revision}, nil
}

func (r *PointerRepository) CompareAndSwap(ctx context.Context, expected PointerRecord, next domain.LatestPointer) (PointerRecord, error) {
	if err := validatePointer(expected.Pointer, expected.Pointer.Capsule, expected.Pointer.Lineage); err != nil {
		return PointerRecord{}, err
	}
	if expected.Revision == "" {
		return PointerRecord{}, fmt.Errorf("pointer has empty expected revision: %w", ErrInvalidDocument)
	}
	if err := validatePointer(next, expected.Pointer.Capsule, expected.Pointer.Lineage); err != nil {
		return PointerRecord{}, err
	}
	if next.Parent == nil || *next.Parent != expected.Pointer.Generation {
		return PointerRecord{}, fmt.Errorf("next pointer parent does not match current generation: %w", ErrInvalidDocument)
	}
	if next.Generation.Generation <= expected.Pointer.Generation.Generation {
		return PointerRecord{}, fmt.Errorf("next pointer generation %d does not advance %d: %w", next.Generation.Generation, expected.Pointer.Generation.Generation, ErrInvalidDocument)
	}
	if err := r.Revalidate(ctx, expected); err != nil {
		return PointerRecord{}, err
	}
	body, err := json.Marshal(next)
	if err != nil {
		return PointerRecord{}, fmt.Errorf("marshal next pointer: %w", err)
	}
	key, _ := next.Lineage.PointerKey(next.Capsule)
	meta, err := r.store.PutConditional(ctx, key, body, ports.WriteCondition{MatchRevision: expected.Revision})
	if err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			if reconciled, readErr := r.Read(ctx, next.Capsule, next.Lineage); readErr == nil && documentsEqual(reconciled.Pointer, next) {
				return reconciled, nil
			}
		}
		if errors.Is(err, ports.ErrConflict) || errors.Is(err, ports.ErrNotFound) {
			return PointerRecord{}, fmt.Errorf("compare and swap pointer %q: %w", key, ErrPointerChanged)
		}
		return PointerRecord{}, err
	}
	return PointerRecord{Pointer: next, Revision: meta.Revision}, nil
}

func (r *PointerRepository) SelectHistorical(ctx context.Context, expected PointerRecord, historical domain.LatestPointer) (PointerRecord, error) {
	if err := validatePointer(expected.Pointer, expected.Pointer.Capsule, expected.Pointer.Lineage); err != nil {
		return PointerRecord{}, err
	}
	if expected.Revision == "" {
		return PointerRecord{}, fmt.Errorf("pointer has empty expected revision: %w", ErrInvalidDocument)
	}
	if err := validatePointer(historical, expected.Pointer.Capsule, expected.Pointer.Lineage); err != nil {
		return PointerRecord{}, err
	}
	if historical.Generation.Generation >= expected.Pointer.Generation.Generation {
		return PointerRecord{}, fmt.Errorf("historical generation %d does not precede current generation %d: %w", historical.Generation.Generation, expected.Pointer.Generation.Generation, ErrInvalidDocument)
	}
	if err := r.Revalidate(ctx, expected); err != nil {
		return PointerRecord{}, err
	}
	body, err := json.Marshal(historical)
	if err != nil {
		return PointerRecord{}, fmt.Errorf("marshal historical pointer: %w", err)
	}
	key, _ := historical.Lineage.PointerKey(historical.Capsule)
	meta, err := r.store.PutConditional(ctx, key, body, ports.WriteCondition{MatchRevision: expected.Revision})
	if err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			if reconciled, readErr := r.Read(ctx, historical.Capsule, historical.Lineage); readErr == nil && documentsEqual(reconciled.Pointer, historical) {
				return reconciled, nil
			}
		}
		if errors.Is(err, ports.ErrConflict) || errors.Is(err, ports.ErrNotFound) {
			return PointerRecord{}, fmt.Errorf("select historical pointer %q: %w", key, ErrPointerChanged)
		}
		return PointerRecord{}, err
	}
	return PointerRecord{Pointer: historical, Revision: meta.Revision}, nil
}

func (r *PointerRepository) Revalidate(ctx context.Context, observed PointerRecord) error {
	current, err := r.Read(ctx, observed.Pointer.Capsule, observed.Pointer.Lineage)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return fmt.Errorf("revalidate pointer: %w", ErrPointerChanged)
		}
		return err
	}
	if current.Revision != observed.Revision || !documentsEqual(current.Pointer, observed.Pointer) {
		return fmt.Errorf("revalidate pointer at revision %q: %w", observed.Revision, ErrPointerChanged)
	}
	return nil
}

type PointerBaseline struct {
	OpenedGeneration  *domain.GenerationRef
	CurrentGeneration *domain.GenerationRef
	ExpectedRevision  ports.Revision
}

func NewPointerBaseline(observed *PointerRecord) PointerBaseline {
	if observed == nil {
		return PointerBaseline{}
	}
	return PointerBaseline{
		OpenedGeneration:  cloneGenerationRef(&observed.Pointer.Generation),
		CurrentGeneration: cloneGenerationRef(&observed.Pointer.Generation),
		ExpectedRevision:  observed.Revision,
	}
}

func (b PointerBaseline) Advance(published PointerRecord) PointerBaseline {
	b.CurrentGeneration = cloneGenerationRef(&published.Pointer.Generation)
	b.ExpectedRevision = published.Revision
	return b
}

func validatePointer(pointer domain.LatestPointer, capsule string, lineage domain.Lineage) error {
	if pointer.SchemaVersion != domain.SchemaVersion || pointer.Capsule != capsule || pointer.Lineage != lineage {
		return fmt.Errorf("pointer identity or schema mismatch: %w", ErrInvalidDocument)
	}
	if _, err := lineage.PointerKey(capsule); err != nil {
		return err
	}
	if err := validateGenerationRef(pointer.Generation); err != nil {
		return err
	}
	if err := validateGenerationParent(pointer.Generation, pointer.Parent); err != nil {
		return err
	}
	wantObjectKey, err := GenerationObjectKey(capsule, lineage, pointer.Generation)
	if err != nil {
		return err
	}
	if pointer.ObjectKey != wantObjectKey || pointer.Size < 0 || pointer.CreatedAt.IsZero() || pointer.SessionID == "" {
		return fmt.Errorf("pointer lacks required publication metadata: %w", ErrInvalidDocument)
	}
	return nil
}

func documentsEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func cloneGenerationRef(ref *domain.GenerationRef) *domain.GenerationRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func readJSON[T any](ctx context.Context, store ports.ObjectStore, key string) (T, ports.ObjectMeta, error) {
	var zero T
	reader, meta, err := store.Get(ctx, key)
	if err != nil {
		return zero, ports.ObjectMeta{}, err
	}
	body, readErr := io.ReadAll(io.LimitReader(reader, maxCoordinationDocumentSize+1))
	closeErr := reader.Close()
	if readErr != nil {
		return zero, ports.ObjectMeta{}, fmt.Errorf("read coordination object %q: %w", key, readErr)
	}
	if closeErr != nil {
		return zero, ports.ObjectMeta{}, fmt.Errorf("close coordination object %q: %w", key, closeErr)
	}
	if len(body) > maxCoordinationDocumentSize {
		return zero, ports.ObjectMeta{}, fmt.Errorf("coordination object %q exceeds size limit: %w", key, ErrInvalidDocument)
	}
	var document T
	if err := json.Unmarshal(body, &document); err != nil {
		return zero, ports.ObjectMeta{}, fmt.Errorf("decode coordination object %q: %v: %w", key, err, ErrInvalidDocument)
	}
	return document, meta, nil
}

package coordination

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func (r *GenerationRepository) List(ctx context.Context, capsule string, lineage domain.Lineage) ([]domain.GenerationMetadata, error) {
	prefix, err := generationMetadataPrefix(capsule, lineage)
	if err != nil {
		return nil, err
	}
	var history []domain.GenerationMetadata
	token := ""
	seenTokens := make(map[string]struct{})
	for {
		items, next, err := r.store.List(ctx, prefix, token)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if !strings.HasSuffix(item.Key, ".json") {
				continue
			}
			metadata, _, err := readJSON[domain.GenerationMetadata](ctx, r.store, item.Key)
			if err != nil {
				return nil, err
			}
			if err := validateStoredGenerationMetadata(metadata, capsule, lineage, metadata.Generation); err != nil {
				return nil, err
			}
			if metadata.MetadataKey != item.Key {
				return nil, fmt.Errorf("generation sidecar key does not match its document: %w", ErrInvalidDocument)
			}
			history = append(history, metadata)
		}
		if next == "" {
			break
		}
		if next == token {
			return nil, fmt.Errorf("object store returned a repeated page token: %w", ports.ErrInvalidPageToken)
		}
		if _, duplicate := seenTokens[next]; duplicate {
			return nil, fmt.Errorf("object store returned a pagination cycle: %w", ports.ErrInvalidPageToken)
		}
		seenTokens[next] = struct{}{}
		token = next
	}
	sort.Slice(history, func(i, j int) bool {
		if history[i].Generation.Generation != history[j].Generation.Generation {
			return history[i].Generation.Generation > history[j].Generation.Generation
		}
		if !history[i].CreatedAt.Equal(history[j].CreatedAt) {
			return history[i].CreatedAt.After(history[j].CreatedAt)
		}
		return history[i].Generation.ArchiveSHA256 < history[j].Generation.ArchiveSHA256
	})
	return history, nil
}

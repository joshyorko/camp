package coordination_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type tokenSequenceStore struct {
	next map[string]string
}

func (s tokenSequenceStore) Get(context.Context, string) (io.ReadCloser, ports.ObjectMeta, error) {
	return nil, ports.ObjectMeta{}, errors.New("unexpected Get")
}

func (s tokenSequenceStore) Head(context.Context, string) (ports.ObjectMeta, error) {
	return ports.ObjectMeta{}, errors.New("unexpected Head")
}

func (s tokenSequenceStore) PutImmutable(context.Context, string, ports.RestartableSource, string, int64) (ports.ObjectMeta, error) {
	return ports.ObjectMeta{}, errors.New("unexpected PutImmutable")
}

func (s tokenSequenceStore) PutConditional(context.Context, string, []byte, ports.WriteCondition) (ports.ObjectMeta, error) {
	return ports.ObjectMeta{}, errors.New("unexpected PutConditional")
}

func (s tokenSequenceStore) DeleteConditional(context.Context, string, ports.Revision) error {
	return errors.New("unexpected DeleteConditional")
}

func (s tokenSequenceStore) List(_ context.Context, _ string, token string) ([]ports.ObjectMeta, string, error) {
	return nil, s.next[token], nil
}

func sha256String(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestGenerationRepositoryListsLineageHistoryNewestFirst(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewGenerationRepository(store)
	ctx := context.Background()
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	put := func(lineage domain.Lineage, generation uint64, body string) {
		t.Helper()
		payload := []byte(body)
		ref := domain.GenerationRef{Generation: generation, ArchiveSHA256: sha256String(payload)}
		metadata := generationMetadataFixture(t, "second-brain", lineage, ref, nil, payload, now.Add(time.Duration(generation)*time.Minute))
		if _, err := repository.PutAndVerify(ctx, metadata, byteSource(payload)); err != nil {
			t.Fatal(err)
		}
	}
	put(domain.Lineage{Branch: "main"}, 41, "main-41")
	put(domain.Lineage{Branch: "feature-safe"}, 42, "branch-42")
	put(domain.Lineage{Branch: "main"}, 43, "main-43")

	main, err := repository.List(ctx, "second-brain", domain.Lineage{Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(main) != 2 || main[0].Generation.Generation != 43 || main[1].Generation.Generation != 41 {
		t.Fatalf("main history = %#v", main)
	}
	branch, err := repository.List(ctx, "second-brain", domain.Lineage{Branch: "feature-safe"})
	if err != nil {
		t.Fatal(err)
	}
	if len(branch) != 1 || branch[0].Generation.Generation != 42 {
		t.Fatalf("branch history = %#v", branch)
	}
}

func TestGenerationRepositoryListsEveryBackendPage(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewGenerationRepository(store)
	ctx := context.Background()
	lineage := domain.Lineage{Branch: "main"}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	const generations = 105

	for generation := uint64(1); generation <= generations; generation++ {
		body := []byte{byte(generation)}
		ref := domain.GenerationRef{Generation: generation, ArchiveSHA256: sha256String(body)}
		metadata := generationMetadataFixture(t, "second-brain", lineage, ref, nil, body, now.Add(time.Duration(generation)*time.Minute))
		metadata.Verified.RemoteBytesVerified = true
		encoded, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutConditional(ctx, metadata.MetadataKey, encoded, ports.WriteCondition{MustBeAbsent: true}); err != nil {
			t.Fatal(err)
		}
	}

	history, err := repository.List(ctx, "second-brain", lineage)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != generations {
		t.Fatalf("history length = %d, want %d", len(history), generations)
	}
	if history[0].Generation.Generation != generations || history[len(history)-1].Generation.Generation != 1 {
		t.Fatalf("history bounds = %d..%d, want %d..1", history[0].Generation.Generation, history[len(history)-1].Generation.Generation, generations)
	}
}

func TestGenerationRepositoryRejectsMalformedParentInHistory(t *testing.T) {
	for _, test := range []struct {
		name             string
		parentGeneration uint64
	}{
		{name: "self parent", parentGeneration: 42},
		{name: "forward parent", parentGeneration: 43},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newObjectStore(t)
			repository := coordination.NewGenerationRepository(store)
			body := []byte("generation")
			child := domain.GenerationRef{Generation: 42, ArchiveSHA256: sha256String(body)}
			parent := generationRef(test.parentGeneration, "a")
			metadata := generationMetadataFixture(t, "second-brain", domain.Lineage{Branch: "feature-safe"}, child, &parent, body, time.Now())
			metadata.Verified.RemoteBytesVerified = true
			encoded, err := json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.PutConditional(context.Background(), metadata.MetadataKey, encoded, ports.WriteCondition{MustBeAbsent: true}); err != nil {
				t.Fatal(err)
			}

			if _, err := repository.List(context.Background(), metadata.Capsule, metadata.Lineage); !errors.Is(err, coordination.ErrInvalidDocument) {
				t.Fatalf("List error = %v, want invalid document", err)
			}
		})
	}
}

func TestGenerationRepositoryRejectsRepeatedAndCyclicPageTokens(t *testing.T) {
	for _, test := range []struct {
		name string
		next map[string]string
	}{
		{name: "repeated", next: map[string]string{"": "repeat", "repeat": "repeat"}},
		{name: "cyclic", next: map[string]string{"": "a", "a": "b", "b": "a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := coordination.NewGenerationRepository(tokenSequenceStore{next: test.next})
			_, err := repository.List(context.Background(), "second-brain", domain.Lineage{Branch: "main"})
			if !errors.Is(err, ports.ErrInvalidPageToken) {
				t.Fatalf("List error = %v, want invalid page token", err)
			}
		})
	}
}

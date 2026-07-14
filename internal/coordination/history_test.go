package coordination_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

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

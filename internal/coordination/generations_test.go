package coordination_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func generationMetadataFixture(t *testing.T, capsule string, lineage domain.Lineage, ref domain.GenerationRef, parent *domain.GenerationRef, body []byte, created time.Time) domain.GenerationMetadata {
	t.Helper()
	objectKey, err := coordination.GenerationObjectKey(capsule, lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	metadataKey, err := coordination.GenerationMetadataKey(capsule, lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	return domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion,
		Capsule:       capsule,
		Lineage:       lineage,
		Generation:    ref,
		Parent:        parent,
		ObjectKey:     objectKey,
		MetadataKey:   metadataKey,
		Size:          int64(len(body)),
		CreatedAt:     created,
		Tools:         domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"},
		SessionID:     "session-" + fillForGeneration(ref),
		Verified:      domain.Verification{LocalHaulLoadable: true},
	}
}

func TestGenerationRepositoryRejectsPersistedUnverifiedSidecars(t *testing.T) {
	for _, mutate := range []func(*domain.GenerationMetadata){
		func(metadata *domain.GenerationMetadata) { metadata.Verified.LocalHaulLoadable = false },
		func(metadata *domain.GenerationMetadata) { metadata.Verified.RemoteBytesVerified = false },
	} {
		store := newObjectStore(t)
		repository := coordination.NewGenerationRepository(store)
		ctx := context.Background()
		body := []byte("verified archive")
		ref := domain.GenerationRef{Generation: 42, ArchiveSHA256: sha256String(body)}
		metadata := generationMetadataFixture(t, "second-brain", domain.Lineage{Branch: "main"}, ref, nil, body, time.Now())
		record, err := repository.PutAndVerify(ctx, metadata, byteSource(body))
		if err != nil {
			t.Fatal(err)
		}
		mutated := record.Metadata
		mutate(&mutated)
		encoded, err := json.Marshal(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.PutConditional(ctx, mutated.MetadataKey, encoded, ports.WriteCondition{MatchRevision: record.Sidecar.Revision}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := repository.ReadMetadata(ctx, mutated.Capsule, mutated.Lineage, mutated.Generation); !errors.Is(err, coordination.ErrGenerationVerification) {
			t.Fatalf("ReadMetadata error = %v, want generation verification failure", err)
		}
		if _, err := repository.List(ctx, mutated.Capsule, mutated.Lineage); !errors.Is(err, coordination.ErrGenerationVerification) {
			t.Fatalf("List error = %v, want generation verification failure", err)
		}
	}
}

func TestGenerationRepositoryPutsVerifiesAndPreservesImmutableSidecar(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewGenerationRepository(store)
	ctx := context.Background()
	body := []byte("verified outer Hauler archive")
	ref := domain.GenerationRef{Generation: 42, ArchiveSHA256: sha256String(body)}
	metadata := generationMetadataFixture(t, "second-brain", domain.Lineage{Branch: "main"}, ref, nil, body, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))

	record, err := repository.PutAndVerify(ctx, metadata, byteSource(body))
	if err != nil {
		t.Fatal(err)
	}
	if !record.Metadata.Verified.LocalHaulLoadable || !record.Metadata.Verified.RemoteBytesVerified {
		t.Fatalf("verification = %#v", record.Metadata.Verified)
	}
	if record.Archive.Key != metadata.ObjectKey || record.Sidecar.Key != metadata.MetadataKey {
		t.Fatalf("record keys = %#v", record)
	}
	read, sidecar, err := repository.ReadMetadata(ctx, metadata.Capsule, metadata.Lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(read, record.Metadata) || sidecar.Revision != record.Sidecar.Revision {
		t.Fatalf("sidecar round trip mismatch\n got: %#v\nwant: %#v", read, record.Metadata)
	}

	mutated := metadata
	mutated.SessionID = "different-session"
	if _, err := repository.PutAndVerify(ctx, mutated, byteSource(body)); !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("mutated sidecar error = %v, want immutable conflict", err)
	}
}

func TestGenerationKeysDeriveBranchScopeFromValidatedLineage(t *testing.T) {
	ref := generationRef(7, "a")
	mainObject, err := coordination.GenerationObjectKey("second-brain", domain.Lineage{Branch: "main"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	branchObject, err := coordination.GenerationObjectKey("second-brain", domain.Lineage{Branch: "feature-safe"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	branchMetadata, err := coordination.GenerationMetadataKey("second-brain", domain.Lineage{Branch: "feature-safe"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	if mainObject != "second-brain/generations/7-"+ref.ArchiveSHA256+".tar.zst" {
		t.Fatalf("main object key = %q", mainObject)
	}
	if branchObject != mainObject {
		t.Fatalf("branch archive key = %q, want shared immutable object %q", branchObject, mainObject)
	}
	if branchMetadata != "second-brain/branches/feature-safe/generations/7-"+ref.ArchiveSHA256+".json" {
		t.Fatalf("branch metadata key = %q", branchMetadata)
	}
	if _, err := coordination.GenerationObjectKey("second-brain", domain.Lineage{Branch: ".."}, ref); err == nil {
		t.Fatal("unsafe lineage produced a generation key")
	}
}

func TestGenerationRepositoryRequiresParentsOlderThanTheirChildren(t *testing.T) {
	for _, test := range []struct {
		name       string
		generation uint64
		wantErr    bool
	}{
		{name: "older cross-lineage branch root", generation: 41},
		{name: "self parent", generation: 42, wantErr: true},
		{name: "forward parent", generation: 43, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newObjectStore(t)
			repository := coordination.NewGenerationRepository(store)
			body := []byte("branch generation")
			child := domain.GenerationRef{Generation: 42, ArchiveSHA256: sha256String(body)}
			parent := generationRef(test.generation, "a")
			metadata := generationMetadataFixture(t, "second-brain", domain.Lineage{Branch: "feature-safe"}, child, &parent, body, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))

			_, err := repository.PutAndVerify(context.Background(), metadata, byteSource(body))
			if test.wantErr {
				if !errors.Is(err, coordination.ErrInvalidDocument) {
					t.Fatalf("PutAndVerify error = %v, want invalid document", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("PutAndVerify with older external-lineage parent: %v", err)
			}
		})
	}
}

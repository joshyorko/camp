package coordination_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

func TestGenerationRepositoryResolvesExactGenerationByKey(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewGenerationRepository(store)
	ctx := context.Background()
	body := []byte("exact generation payload")
	ref := domain.GenerationRef{Generation: 7, ArchiveSHA256: sha256String(body)}
	metadata := generationMetadataFixture(t, "second-brain", domain.Lineage{Branch: "main"}, ref, nil, body, time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC))

	record, err := repository.PutAndVerify(ctx, metadata, byteSource(body))
	if err != nil {
		t.Fatal(err)
	}

	exact, err := repository.ResolveExactGeneration(ctx, metadata.Capsule, metadata.Lineage, metadata.Generation)
	if err != nil {
		t.Fatal(err)
	}
	expectedMetadataBytes := mustMarshalJSON(t, record.Metadata)
	if !bytes.Equal(exact.MetadataBytes, expectedMetadataBytes) {
		t.Fatalf("metadata bytes mismatch")
	}
	if !reflect.DeepEqual(exact.Metadata, record.Metadata) {
		t.Fatalf("exact metadata mismatch\n got: %#v\nwant: %#v", exact.Metadata, record.Metadata)
	}
	if exact.Archive.Key != record.Metadata.ObjectKey || exact.Sidecar.Key != record.Metadata.MetadataKey {
		t.Fatalf("exact metadata keys mismatch\n got archive=%q/%q sidecar=%q/%q", exact.Archive.Key, record.Archive.Key, exact.Sidecar.Key, record.Sidecar.Key)
	}
	if exact.Archive.SHA256 != metadata.Generation.ArchiveSHA256 {
		t.Fatalf("archive sha256 = %q, want %q", exact.Archive.SHA256, metadata.Generation.ArchiveSHA256)
	}
	if exact.ArchiveFingerprint.Key != metadata.ObjectKey || exact.ArchiveFingerprint.Size != metadata.Size {
		t.Fatalf("archive fingerprint = %#v", exact.ArchiveFingerprint)
	}
	if exact.Sidecar.SHA256 == "" || exact.SidecarFingerprint.SHA256 == "" {
		t.Fatal("missing metadata fingerprint")
	}
	if err := exact.RevalidateSources(ctx); err != nil {
		t.Fatalf("RevalidateSources: %v", err)
	}
	archiveReader, err := exact.ArchiveSource.Open()
	if err != nil {
		t.Fatal(err)
	}
	sidecarReader, err := exact.SidecarSource.Open()
	if err != nil {
		archiveReader.Close()
		t.Fatal(err)
	}
	defer archiveReader.Close()
	defer sidecarReader.Close()
	gotArchive, err := io.ReadAll(archiveReader)
	if err != nil {
		t.Fatal(err)
	}
	gotSidecar, err := io.ReadAll(sidecarReader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotArchive, body) {
		t.Fatalf("archive source mismatch")
	}
	if !bytes.Equal(gotSidecar, exact.MetadataBytes) {
		t.Fatalf("sidecar source mismatch")
	}
	if err := store.DeleteConditional(ctx, exact.Archive.Key, exact.Archive.Revision); err != nil {
		t.Fatal(err)
	}
	if err := exact.RevalidateSources(ctx); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("RevalidateSources after source removal = %v, want not found", err)
	}
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestGenerationRepositoryResolveExactGenerationRejectsUnverifiedMetadata(t *testing.T) {
	store := newObjectStore(t)
	repository := coordination.NewGenerationRepository(store)
	ctx := context.Background()
	body := []byte("verified archive")
	ref := domain.GenerationRef{Generation: 11, ArchiveSHA256: sha256String(body)}
	metadata := generationMetadataFixture(t, "second-brain", domain.Lineage{Branch: "main"}, ref, nil, body, time.Now())

	record, err := repository.PutAndVerify(ctx, metadata, byteSource(body))
	if err != nil {
		t.Fatal(err)
	}
	mutated := record.Metadata
	mutated.Verified.RemoteBytesVerified = false
	encoded, err := json.Marshal(mutated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConditional(ctx, mutated.MetadataKey, encoded, ports.WriteCondition{MatchRevision: record.Sidecar.Revision}); err != nil {
		t.Fatal(err)
	}
	_, err = repository.ResolveExactGeneration(ctx, metadata.Capsule, metadata.Lineage, metadata.Generation)
	if !errors.Is(err, coordination.ErrGenerationVerification) {
		t.Fatalf("ResolveExactGeneration error = %v, want generation verification", err)
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

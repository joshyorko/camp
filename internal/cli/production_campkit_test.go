package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

func TestProductionCampKitResolverResolvesExactGenerationThenRejectsIncompleteClosure(t *testing.T) {
	store, err := filebackend.New(filepath.Join(t.TempDir(), "backend"))
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("immutable-generation")
	digest := sha256.Sum256(body)
	ref := domain.GenerationRef{Generation: 42, ArchiveSHA256: hex.EncodeToString(digest[:])}
	lineage := domain.Lineage{Branch: "main"}
	objectKey, _ := coordination.GenerationObjectKey("brain", lineage, ref)
	metadataKey, _ := coordination.GenerationMetadataKey("brain", lineage, ref)
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: "brain", Lineage: lineage, Generation: ref,
		ObjectKey: objectKey, MetadataKey: metadataKey, Size: int64(len(body)), CreatedAt: time.Unix(1, 0).UTC(),
		Tools: domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.2"}, SessionID: "session-1",
		Verified: domain.Verification{LocalHaulLoadable: true},
	}
	if _, err := coordination.NewGenerationRepository(store).PutAndVerify(context.Background(), metadata, testRestartableBytes(body)); err != nil {
		t.Fatal(err)
	}

	resolver := productionCampKitResolver{generations: coordination.NewGenerationRepository(store), capsule: "brain", lineage: lineage}
	_, _, _, err = resolver.ResolveCampKitExport(context.Background(), "42-"+ref.ArchiveSHA256)
	var incomplete *CampKitIncompleteClosureError
	if !errors.As(err, &incomplete) {
		t.Fatalf("ResolveCampKitExport error = %v, want incomplete closure", err)
	}
	for _, missing := range []string{"camp executable", "runtime", "devpod provider", "devpod tool", "hauler tool", "Room image"} {
		if !strings.Contains(err.Error(), missing) {
			t.Fatalf("error %q does not list %q", err, missing)
		}
	}
}

func TestWriteKitExportReceiptIsStable(t *testing.T) {
	result := KitExportReceipt{Generation: "42-" + strings.Repeat("a", 64), Output: "/tmp/brain.campkit"}
	for _, mode := range []OutputMode{ModeHuman, ModeJSON} {
		var output bytes.Buffer
		if err := writeKitExportReceipt(&output, mode, result); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), result.Generation) || !strings.Contains(output.String(), result.Output) {
			t.Fatalf("receipt %q lacks stable identity", output.String())
		}
	}
}

type testRestartableBytes []byte

func (b testRestartableBytes) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(b)), nil
}

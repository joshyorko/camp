package coordination_test

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

type byteSource []byte

func (s byteSource) Open() (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s)), nil
}

func newObjectStore(t *testing.T) *filebackend.Store {
	t.Helper()
	store, err := filebackend.New(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func generationRef(number uint64, fill string) domain.GenerationRef {
	return domain.GenerationRef{Generation: number, ArchiveSHA256: strings.Repeat(fill, 64)}
}

func pointerFixture(capsule string, lineage domain.Lineage, generation domain.GenerationRef, parent *domain.GenerationRef, created time.Time) domain.LatestPointer {
	objectKey, err := coordination.GenerationObjectKey(capsule, lineage, generation)
	if err != nil {
		panic(err)
	}
	return domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion,
		Capsule:       capsule,
		Lineage:       lineage,
		Generation:    generation,
		Parent:        parent,
		ObjectKey:     objectKey,
		Size:          123,
		CreatedAt:     created,
		Tools:         domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.1"},
		SessionID:     "session-" + fillForGeneration(generation),
	}
}

func fillForGeneration(ref domain.GenerationRef) string {
	if ref.ArchiveSHA256 == "" {
		return "empty"
	}
	return ref.ArchiveSHA256[:1]
}

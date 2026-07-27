package haulkit

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

const testDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestMarshalCanonicalIsStableAndDoesNotMutateManifest(t *testing.T) {
	manifest := validTestManifest()
	manifest.Store.Entries = []StoreEntry{
		{Reference: "z", Type: "file", Digest: testDigest},
		{Reference: "a", Type: "image", Platform: "linux/amd64", Digest: strings.Repeat("b", 64)},
	}
	before := append([]StoreEntry(nil), manifest.Store.Entries...)

	first, err := MarshalCanonical(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalCanonical(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("canonical manifests differ")
	}
	if manifest.Store.Entries[0] != before[0] || manifest.Store.Entries[1] != before[1] {
		t.Fatal("MarshalCanonical mutated caller-owned entries")
	}
	if bytes.Index(first, []byte(`"reference":"a"`)) > bytes.Index(first, []byte(`"reference":"z"`)) {
		t.Fatal("store entries are not canonical")
	}
}

func TestValidateRejectsMalformedIdentityAndPathFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"kind", func(m *Manifest) { m.Kind = "camp-kit" }},
		{"session traversal", func(m *Manifest) { m.SessionID = "../session" }},
		{"capsule separator", func(m *Manifest) { m.Capsule = "a/b" }},
		{"lineage traversal", func(m *Manifest) { m.Lineage.Branch = ".." }},
		{"architecture", func(m *Manifest) { m.Architecture = "darwin/amd64" }},
		{"store digest", func(m *Manifest) { m.Store.IndexSHA256 = "sha256:bad" }},
		{"entry traversal", func(m *Manifest) { m.Store.Entries[0].Reference = "../escape" }},
		{"root traversal", func(m *Manifest) { m.Root.Reference = "../../root.tar.zst" }},
		{"tool path", func(m *Manifest) { m.Tools.Camp.Name = "../camp" }},
		{"tool version", func(m *Manifest) { m.Tools.Hauler.Version = "" }},
		{"tool digest", func(m *Manifest) { m.Tools.Pasta.SHA256 = "bad" }},
		{"archive digest", func(m *Manifest) { m.Archive.SHA256 = "" }},
		{"chunk name", func(m *Manifest) { m.Chunks[0].Name = "/tmp/chunk" }},
		{"chunk order", func(m *Manifest) { m.Chunks[0].Index = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validTestManifest()
			test.mutate(&manifest)
			if err := Validate(manifest); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestValidateRejectsResourceCeilingViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"archive bytes", func(m *Manifest) {
			m.Archive.Size = maxArchiveBytes + 1
			m.Chunks = []ChunkIdentity{{Index: 0, Name: "kit.part", SHA256: testDigest, Size: maxArchiveBytes + 1}}
		}},
		{"chunk bytes", func(m *Manifest) {
			m.Archive.Size = maxChunkBytes + 1
			m.Chunks = []ChunkIdentity{{Index: 0, Name: "kit.part", SHA256: testDigest, Size: maxChunkBytes + 1}}
		}},
		{"chunk count", func(m *Manifest) {
			m.Archive.Size = int64(maxChunkCount + 1)
			m.Chunks = make([]ChunkIdentity, maxChunkCount+1)
			for index := range m.Chunks {
				m.Chunks[index] = ChunkIdentity{
					Index: uint32(index), Name: "part-" + strings.Repeat("x", index%3),
					SHA256: testDigest, Size: 1,
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validTestManifest()
			test.mutate(&manifest)
			if err := Validate(manifest); !errors.Is(err, ErrResourceLimit) {
				t.Fatalf("Validate() error = %v, want ErrResourceLimit", err)
			}
		})
	}
}

func TestDecodeCanonicalRejectsOversizedManifestBeforeDecode(t *testing.T) {
	body := bytes.Repeat([]byte(" "), maxManifestBytes+1)
	if _, err := DecodeCanonical(body); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("DecodeCanonical() error = %v, want ErrResourceLimit", err)
	}
}

func validTestManifest() Manifest {
	return Manifest{
		SchemaVersion: ManifestSchemaVersion,
		Kind:          "camp-hauler-kit",
		SessionID:     "session-1",
		Capsule:       "capsule",
		Lineage:       domain.Lineage{Branch: "main"},
		Generation:    &domain.GenerationRef{Generation: 1, ArchiveSHA256: testDigest},
		Architecture:  "linux/amd64",
		Store: StoreIdentity{
			HaulerVersion: "v2.0.2",
			IndexSHA256:   testDigest,
			Entries:       []StoreEntry{{Reference: "root", Type: "file", Digest: testDigest}},
		},
		Root: RootIdentity{Reference: "root.tar.zst", SHA256: testDigest, Size: 4},
		Tools: ToolIdentities{
			Camp:   FileIdentity{Name: "camp", Version: "dev", SHA256: testDigest, Size: 4},
			Hauler: FileIdentity{Name: "hauler", Version: "v2.0.2", SHA256: testDigest, Size: 4},
			Pasta:  FileIdentity{Name: "pasta", Version: "pasta 1", SHA256: testDigest, Size: 4},
		},
		Archive: ArchiveIdentity{SHA256: testDigest, Size: 8},
		Chunks:  []ChunkIdentity{{Index: 0, Name: "kit.tar.zst.part-000000", SHA256: testDigest, Size: 8}},
	}
}

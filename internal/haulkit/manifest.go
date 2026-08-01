package haulkit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/joshyorko/camp/internal/domain"
)

const (
	ManifestSchemaVersion uint32 = 1
	DefaultChunkSize      int64  = 256 << 20
	manifestKind                 = "camp-hauler-kit"
	maxManifestBytes             = 4 << 20
	maxArchiveBytes       int64  = 64 << 30
	maxChunkBytes         int64  = DefaultChunkSize
	maxChunkCount                = 256
)

var ErrInvalidManifest = errors.New("invalid Camp Hauler kit manifest")

type Manifest struct {
	SchemaVersion uint32                `json:"schemaVersion"`
	Kind          string                `json:"kind"`
	SessionID     string                `json:"sessionId"`
	Capsule       string                `json:"capsule"`
	Lineage       domain.Lineage        `json:"lineage"`
	Generation    *domain.GenerationRef `json:"generation,omitempty"`
	Architecture  string                `json:"architecture"`
	Store         StoreIdentity         `json:"store"`
	Root          RootIdentity          `json:"root"`
	Tools         ToolIdentities        `json:"tools"`
	Archive       ArchiveIdentity       `json:"archive"`
	Chunks        []ChunkIdentity       `json:"chunks"`
}

type StoreIdentity struct {
	HaulerVersion string       `json:"haulerVersion"`
	IndexSHA256   string       `json:"indexSHA256"`
	Entries       []StoreEntry `json:"entries"`
}

type StoreEntry struct {
	Reference string `json:"reference"`
	Type      string `json:"type"`
	Platform  string `json:"platform,omitempty"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size,omitempty"`
}

type RootIdentity struct {
	Reference string `json:"reference"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type ToolIdentities struct {
	Camp   FileIdentity `json:"camp"`
	Hauler FileIdentity `json:"hauler"`
	Pasta  FileIdentity `json:"pasta"`
}

type FileIdentity struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type ArchiveIdentity struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ChunkIdentity struct {
	Index  uint32 `json:"index"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func Validate(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchemaVersion || manifest.Kind != manifestKind {
		return invalidManifest("unsupported schema or kind")
	}
	if !safeSegment(manifest.SessionID) || !safeSegment(manifest.Capsule) || !safeSegment(manifest.Lineage.Branch) {
		return invalidManifest("unsafe session, capsule, or lineage")
	}
	if manifest.Generation != nil && (manifest.Generation.Generation == 0 || !validSHA256(manifest.Generation.ArchiveSHA256)) {
		return invalidManifest("invalid generation")
	}
	if manifest.Architecture != "linux/amd64" && manifest.Architecture != "linux/arm64" {
		return invalidManifest("unsupported architecture")
	}
	if manifest.Store.HaulerVersion == "" || !validSHA256(manifest.Store.IndexSHA256) || len(manifest.Store.Entries) == 0 {
		return invalidManifest("invalid store identity")
	}
	seenEntries := make(map[string]struct{}, len(manifest.Store.Entries))
	for _, entry := range manifest.Store.Entries {
		key := entry.Type + "\x00" + entry.Reference + "\x00" + entry.Platform
		if entry.Reference == "" || unsafePath(entry.Reference) || (entry.Type != "file" && entry.Type != "image") ||
			!validSHA256(entry.Digest) || entry.Size < 0 ||
			(entry.Platform != "" && entry.Platform != "linux/amd64" && entry.Platform != "linux/arm64") {
			return invalidManifest("invalid store entry")
		}
		if _, exists := seenEntries[key]; exists {
			return invalidManifest("duplicate store entry")
		}
		seenEntries[key] = struct{}{}
	}
	normalizedRoot, err := NormalizeRootReference(manifest.Root.Reference)
	if err != nil || normalizedRoot != manifest.Root.Reference || !validSHA256(manifest.Root.SHA256) || manifest.Root.Size <= 0 {
		return invalidManifest("invalid root identity")
	}
	for _, tool := range []FileIdentity{manifest.Tools.Camp, manifest.Tools.Hauler, manifest.Tools.Pasta} {
		if !safeRelativeName(tool.Name) || tool.Version == "" || !validSHA256(tool.SHA256) || tool.Size <= 0 {
			return invalidManifest("invalid tool identity")
		}
	}
	if !validSHA256(manifest.Archive.SHA256) || manifest.Archive.Size <= 0 || len(manifest.Chunks) == 0 {
		return invalidManifest("invalid archive identity")
	}
	if manifest.Archive.Size > maxArchiveBytes || len(manifest.Chunks) > maxChunkCount {
		return fmt.Errorf("%w: archive bytes or chunk count", ErrArchiveLimit)
	}
	var chunkBytes int64
	for index, chunk := range manifest.Chunks {
		if chunk.Index != uint32(index) || !safeRelativeName(chunk.Name) || !validSHA256(chunk.SHA256) ||
			chunk.Size <= 0 {
			return invalidManifest("invalid chunk identity")
		}
		if chunk.Size > maxChunkBytes {
			return fmt.Errorf("%w: chunk bytes", ErrArchiveLimit)
		}
		if chunkBytes > manifest.Archive.Size-chunk.Size {
			return invalidManifest("chunk size overflow")
		}
		chunkBytes += chunk.Size
	}
	if chunkBytes != manifest.Archive.Size {
		return invalidManifest("chunk sizes do not match archive")
	}
	return nil
}

func NormalizeRootReference(reference string) (string, error) {
	const (
		prefix = "hauler/"
		suffix = ":latest"
	)
	name := reference
	if strings.HasPrefix(reference, prefix) {
		if !strings.HasSuffix(reference, suffix) {
			return "", invalidManifest("root reference tag")
		}
		name = strings.TrimSuffix(strings.TrimPrefix(reference, prefix), suffix)
	}
	if !safeRelativeName(name) || !strings.HasSuffix(name, ".tar.zst") {
		return "", invalidManifest("root reference")
	}
	return prefix + name + suffix, nil
}

func MarshalCanonical(manifest Manifest) ([]byte, error) {
	copyManifest := manifest
	copyManifest.Store.Entries = append([]StoreEntry(nil), manifest.Store.Entries...)
	copyManifest.Chunks = append([]ChunkIdentity(nil), manifest.Chunks...)
	sort.Slice(copyManifest.Store.Entries, func(i, j int) bool {
		left, right := copyManifest.Store.Entries[i], copyManifest.Store.Entries[j]
		if left.Reference != right.Reference {
			return left.Reference < right.Reference
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		return left.Platform < right.Platform
	})
	sort.Slice(copyManifest.Chunks, func(i, j int) bool { return copyManifest.Chunks[i].Index < copyManifest.Chunks[j].Index })
	if err := Validate(copyManifest); err != nil {
		return nil, err
	}
	return json.Marshal(copyManifest)
}

func DecodeCanonical(body []byte) (Manifest, error) {
	var manifest Manifest
	if len(body) == 0 || len(body) > maxManifestBytes {
		return manifest, invalidManifest("manifest size")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("%w: decode: %v", ErrInvalidManifest, err)
	}
	if decoder.More() {
		return manifest, invalidManifest("trailing JSON")
	}
	canonical, err := MarshalCanonical(manifest)
	if err != nil {
		return manifest, err
	}
	if !bytes.Equal(body, canonical) {
		return manifest, invalidManifest("non-canonical encoding")
	}
	return manifest, nil
}

func invalidManifest(reason string) error {
	return fmt.Errorf("%w: %s", ErrInvalidManifest, reason)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func safeSegment(value string) bool {
	return value != "" && value != "." && value != ".." && !strings.ContainsAny(value, `/\`+"\x00") && !hasControl(value)
}

func safeRelativeName(value string) bool {
	return value != "" && filepath.Base(value) == value && value != "." && value != ".." && !hasControl(value)
}

func unsafePath(value string) bool {
	if filepath.IsAbs(value) || strings.Contains(value, "\\") || hasControl(value) {
		return true
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return true
		}
	}
	return false
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

package campkit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/klauspost/compress/zstd"
)

func TestExportWritesDeterministicArchiveFromValidatedSources(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	manifest.Lineage.ExportedAt = time.Unix(1700000000, 0).UTC()
	if err := Validate(manifest); err != nil {
		t.Fatal(err)
	}

	first := new(bytes.Buffer)
	second := new(bytes.Buffer)
	if err := Export(context.Background(), first, manifest, byteSources(payloads)); err != nil {
		t.Fatal(err)
	}
	if err := Export(context.Background(), second, manifest, byteSources(payloads)); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Bytes(), second.Bytes()) {
		t.Fatal("repeated exports differ")
	}
	if _, err := Verify(context.Background(), bytes.NewReader(first.Bytes()), DefaultArchiveLimits(), nil); err != nil {
		t.Fatalf("exported archive does not verify: %v", err)
	}
}

func TestExportRejectsSourceDigestMismatchBeforeWriting(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	payloads[manifest.Payloads[0].Path] = []byte("wrong")
	sources := byteSources(payloads)
	output := new(bytes.Buffer)
	if err := Export(context.Background(), output, manifest, sources); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Export error = %v, want digest mismatch", err)
	}
	if output.Len() != 0 {
		t.Fatal("export wrote bytes after source validation failed")
	}
}

func TestExportFileLeavesNoTemporaryArtifactOnFailure(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	payloads[manifest.Payloads[0].Path] = []byte("wrong")
	directory := t.TempDir()
	output := filepath.Join(directory, "kit.campkit")
	if err := ExportFile(context.Background(), output, manifest, byteSources(payloads)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("ExportFile error = %v, want digest mismatch", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output stat error = %v, want missing output", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary artifacts remain: %v", entries)
	}
}

func TestExportStreamsLargePayloadWithoutWholeBodyOrArchiveWrites(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	var target string
	for _, payload := range manifest.Payloads {
		if payload.Role != PayloadGenerationArchive && payload.Role != PayloadGenerationMetadata {
			target = payload.Path
			break
		}
	}
	const size = int64(32 << 20)
	setGeneratedPayload(&manifest, target, size)
	source := &generatedSource{size: size, value: 'x'}
	sources := byteSources(payloads)
	sources[target] = source
	output := &boundedRecordingWriter{}
	if err := Export(context.Background(), output, manifest, sources); err != nil {
		t.Fatal(err)
	}
	if source.maxRead > 1<<20 {
		t.Fatalf("source read request = %d, want bounded", source.maxRead)
	}
	if output.maxWrite > 1<<20 {
		t.Fatalf("archive write = %d, want streaming", output.maxWrite)
	}
}

func TestExportFileCleansOwnedTemporaryFileOnCancellationAndSourceErrors(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	tests := []struct {
		name   string
		ctx    func() context.Context
		source PayloadSource
	}{
		{name: "cancelled", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, source: byteSources(payloads)[manifest.Payloads[0].Path]},
		{name: "open error", ctx: context.Background, source: failingSource{err: errors.New("open failed")}},
		{name: "close error", ctx: context.Background, source: closeFailingSource{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			output := filepath.Join(directory, "kit.campkit")
			sources := byteSources(payloads)
			sources[manifest.Payloads[0].Path] = test.source
			if err := ExportFile(test.ctx(), output, manifest, sources); err == nil {
				t.Fatal("ExportFile unexpectedly succeeded")
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary artifacts remain: %v", entries)
			}
		})
	}
}

func setGeneratedPayload(manifest *Manifest, path string, size int64) {
	hash := sha256.New()
	chunk := bytes.Repeat([]byte{'x'}, 64<<10)
	for remaining := size; remaining > 0; {
		n := int64(len(chunk))
		if n > remaining {
			n = remaining
		}
		_, _ = hash.Write(chunk[:n])
		remaining -= n
	}
	for i := range manifest.Payloads {
		if manifest.Payloads[i].Path == path {
			manifest.Payloads[i].Size = size
			manifest.Payloads[i].SHA256 = hex.EncodeToString(hash.Sum(nil))
			return
		}
	}
	panic("payload not found: " + path)
}

type generatedSource struct {
	size    int64
	value   byte
	maxRead int
}

func (s *generatedSource) Open() (io.ReadCloser, error) {
	return &generatedReader{remaining: s.size, value: s.value, maxRead: &s.maxRead}, nil
}

type generatedReader struct {
	remaining int64
	value     byte
	maxRead   *int
}

func (r *generatedReader) Read(p []byte) (int, error) {
	if len(p) > *r.maxRead {
		*r.maxRead = len(p)
	}
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = r.value
	}
	r.remaining -= int64(n)
	return n, nil
}

func (r *generatedReader) Close() error { return nil }

type boundedRecordingWriter struct{ maxWrite int }

func (w *boundedRecordingWriter) Write(p []byte) (int, error) {
	if len(p) > w.maxWrite {
		w.maxWrite = len(p)
	}
	return len(p), nil
}

type failingSource struct{ err error }

func (s failingSource) Open() (io.ReadCloser, error) { return nil, s.err }

type closeFailingSource struct{}

func (closeFailingSource) Open() (io.ReadCloser, error) {
	return closeFailingReader{Reader: bytes.NewReader([]byte("close-error"))}, nil
}

type closeFailingReader struct{ *bytes.Reader }

func (closeFailingReader) Close() error { return errors.New("close failed") }

func byteSources(values map[string][]byte) map[string]PayloadSource {
	result := make(map[string]PayloadSource, len(values))
	for path, body := range values {
		result[path] = payloadBytes(body)
	}
	return result
}

type payloadBytes []byte

func (b payloadBytes) Open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }

func TestInspectReadsOnlyCanonicalManifestAndMarksIntegrityNotVerified(t *testing.T) {
	manifest := validManifest()
	body := mustCanonical(t, manifest)
	archive := campKitFixture(t, []fixtureEntry{
		{name: "manifest.json", body: body},
		{name: manifest.Payloads[0].Path, body: []byte("intentionally corrupt and unread")},
	})

	inspection, err := Inspect(bytes.NewReader(archive), DefaultArchiveLimits())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Manifest.Lineage.KitID != manifest.Lineage.KitID {
		t.Fatalf("kit ID = %q, want %q", inspection.Manifest.Lineage.KitID, manifest.Lineage.KitID)
	}
	if inspection.Integrity != IntegrityNotVerified {
		t.Fatalf("integrity = %q, want %q", inspection.Integrity, IntegrityNotVerified)
	}
}

func TestInspectRejectsNonDeterministicManifestHeader(t *testing.T) {
	manifest := validManifest()
	archive := campKitFixtureWithHeader(t, []fixtureEntry{{
		name: "manifest.json",
		body: mustCanonical(t, manifest),
	}}, func(header *tar.Header) {
		header.Mode = 0o644
	})

	if _, err := Inspect(bytes.NewReader(archive), DefaultArchiveLimits()); err == nil {
		t.Fatal("inspect accepted a writable manifest header")
	}
}

func TestVerifyStreamsPayloadsAndSeparatesIntegrityFromTrust(t *testing.T) {
	archive, manifest, _ := verifiedKitFixture(t)

	verification, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Integrity != IntegrityValid {
		t.Fatalf("integrity = %q, want %q", verification.Integrity, IntegrityValid)
	}
	if verification.Trust != TrustResultUnverified {
		t.Fatalf("trust = %q, want %q", verification.Trust, TrustResultUnverified)
	}
	if len(verification.Payloads) != len(manifest.Payloads) {
		t.Fatalf("verified payloads = %d, want %d", len(verification.Payloads), len(manifest.Payloads))
	}
	if verification.OCIClosure != OCIClosureNotVerified {
		t.Fatalf("OCI closure = %q, want %q", verification.OCIClosure, OCIClosureNotVerified)
	}
}

func TestVerifyStreamsLargePayloadThroughArchiveTraversal(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	var target string
	for _, payload := range manifest.Payloads {
		if payload.Role != PayloadGenerationArchive && payload.Role != PayloadGenerationMetadata {
			target = payload.Path
			break
		}
	}
	if target == "" {
		t.Fatal("fixture has no ordinary payload")
	}
	payloads[target] = bytes.Repeat([]byte("p"), 8<<20)
	setPayloadBytes(&manifest, target, payloads[target], "")
	archive := campKitFixture(t, fixtureEntries(manifest, payloads, sortedPayloadPaths(manifest)))

	verification, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil)
	if err != nil {
		t.Fatalf("Verify large archive: %v", err)
	}
	if verification.Integrity != IntegrityValid {
		t.Fatalf("integrity = %q, want valid", verification.Integrity)
	}

	runtime.GC()
	allocs := testing.AllocsPerRun(3, func() {
		if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil); err != nil {
			t.Fatalf("Verify repeated large archive: %v", err)
		}
	})
	if allocs > 500 {
		t.Fatalf("large-payload Verify allocations = %.0f, want bounded traversal", allocs)
	}
}

func TestVerifyPassesTrustEvidenceToEvaluator(t *testing.T) {
	archive, manifest, payloads := verifiedKitFixture(t)
	when := manifest.Lineage.ExportedAt.Add(-time.Minute)
	manifest.Trust = verifiedTrust(&when)
	evidence := []byte(`{"evidence":"sig","trust":"present"}`)
	addTrustEvidence(&manifest)
	payloads[manifest.Trust.EvidencePath] = evidence
	setPayloadBytes(&manifest, manifest.Trust.EvidencePath, evidence, "")
	archive = campKitFixture(t, fixtureEntries(manifest, payloads, sortedPayloadPaths(manifest)))

	expected := string(evidence)
	evaluator := &trustEvaluatorMock{
		evidence: expected,
		result:   TrustResult("verified"),
	}
	verification, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), evaluator)
	if err != nil {
		t.Fatal(err)
	}
	if verification.Trust != TrustResult("verified") {
		t.Fatalf("trust = %q, want %q", verification.Trust, TrustResult("verified"))
	}
	if got := string(evaluator.received); got != expected {
		t.Fatalf("evidence = %q, want %q", got, expected)
	}
}

func TestVerifyRejectsTrustRejectedPath(t *testing.T) {
	archive, manifest, payloads := verifiedKitFixture(t)
	when := manifest.Lineage.ExportedAt.Add(-time.Minute)
	manifest.Trust = verifiedTrust(&when)
	manifest.Trust.Status = TrustRejected
	evidence := []byte(`{"evidence":"reject"}`)
	addTrustEvidence(&manifest)
	payloads[manifest.Trust.EvidencePath] = evidence
	setPayloadBytes(&manifest, manifest.Trust.EvidencePath, evidence, "")
	archive = campKitFixture(t, fixtureEntries(manifest, payloads, sortedPayloadPaths(manifest)))

	evaluator := &trustEvaluatorMock{
		evidence: string(evidence),
		err:      ErrTrustRejected,
	}
	if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), evaluator); !errors.Is(err, ErrTrustRejected) {
		t.Fatalf("verify error = %v, want %v", err, ErrTrustRejected)
	}
}

func TestVerifyRejectsVerifiedTrustWithoutEvaluator(t *testing.T) {
	archive, manifest, payloads := verifiedKitFixture(t)
	when := manifest.Lineage.ExportedAt.Add(-time.Minute)
	manifest.Trust = verifiedTrust(&when)
	evidence := []byte(`{"evidence":"sig","trust":"required"}`)
	addTrustEvidence(&manifest)
	payloads[manifest.Trust.EvidencePath] = evidence
	setPayloadBytes(&manifest, manifest.Trust.EvidencePath, evidence, "")
	archive = campKitFixture(t, fixtureEntries(manifest, payloads, sortedPayloadPaths(manifest)))

	if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil); !errors.Is(err, ErrTrustUnsupported) {
		t.Fatalf("verify error = %v, want %v", err, ErrTrustUnsupported)
	}
}

func TestVerifyRejectsOversizedPayloadWithoutBoundedAllocation(t *testing.T) {
	payload := bytes.Repeat([]byte{'a'}, 65536)
	if _, err := sha256HexFromReader(bytes.NewReader(payload), int64(len(payload)), 1024); !errors.Is(err, ErrArchiveLimit) {
		t.Fatalf("sha256HexFromReader error = %v, want %v", err, ErrArchiveLimit)
	}
}

func TestVerifyRejectsCorruptReorderedAndTrailingArchives(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	paths := sortedPayloadPaths(manifest)

	t.Run("corrupt payload", func(t *testing.T) {
		entries := fixtureEntries(manifest, payloads, paths)
		entries[1].body = append([]byte(nil), entries[1].body...)
		entries[1].body[0] ^= 0xff
		archive := campKitFixture(t, entries)
		if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil); !errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("verify error = %v, want digest mismatch", err)
		}
	})

	t.Run("reordered payloads", func(t *testing.T) {
		reversed := append([]string(nil), paths...)
		reverse(reversed)
		archive := campKitFixture(t, fixtureEntries(manifest, payloads, reversed))
		if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil); !errors.Is(err, ErrArchiveFormat) {
			t.Fatalf("verify error = %v, want archive format", err)
		}
	})

	t.Run("second zstd frame", func(t *testing.T) {
		archive, _, _ := verifiedKitFixture(t)
		second := campKitFixture(t, []fixtureEntry{{name: "manifest.json", body: mustCanonical(t, manifest)}})
		archive = append(archive, second...)
		if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil); !errors.Is(err, ErrArchiveFormat) {
			t.Fatalf("verify error = %v, want archive format", err)
		}
	})
}

func TestVerifyBindsRawGenerationMetadataSemantics(t *testing.T) {
	_, manifest, payloads := verifiedKitFixture(t)
	metadataPath := manifest.Generation.MetadataPath
	var metadata domain.GenerationMetadata
	if err := json.Unmarshal(payloads[metadataPath], &metadata); err != nil {
		t.Fatal(err)
	}
	metadata.Verified.RemoteBytesVerified = false
	payloads[metadataPath] = mustJSON(t, metadata)
	setPayloadBytes(&manifest, metadataPath, payloads[metadataPath], "")
	archive := campKitFixture(t, fixtureEntries(manifest, payloads, sortedPayloadPaths(manifest)))

	if _, err := Verify(context.Background(), bytes.NewReader(archive), DefaultArchiveLimits(), nil); !errors.Is(err, ErrMetadataMismatch) {
		t.Fatalf("verify error = %v, want metadata mismatch", err)
	}
}

type trustEvaluatorMock struct {
	evidence string
	received []byte
	result   TrustResult
	err      error
}

func (m *trustEvaluatorMock) Evaluate(_ context.Context, _ Manifest, r io.Reader) (TrustResult, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return TrustResultUnverified, err
	}
	m.received = body
	return m.result, m.err
}

type fixtureEntry struct {
	name string
	body []byte
}

func campKitFixture(t *testing.T, entries []fixtureEntry) []byte {
	t.Helper()
	return campKitFixtureWithHeader(t, entries, nil)
}

func campKitFixtureWithHeader(t *testing.T, entries []fixtureEntry, mutate func(*tar.Header)) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(
		&compressed,
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	for _, entry := range entries {
		header := &tar.Header{
			Typeflag: tar.TypeReg,
			Name:     entry.name,
			Size:     int64(len(entry.body)),
			Mode:     0o444,
			Uid:      0,
			Gid:      0,
			ModTime:  time.Unix(0, 0).UTC(),
			Format:   tar.FormatUSTAR,
		}
		if mutate != nil {
			mutate(header)
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func verifiedKitFixture(t *testing.T) ([]byte, Manifest, map[string][]byte) {
	t.Helper()
	manifest := validManifest()
	manifest.SupportedPlatforms = []Platform{amd64()}
	manifest.Payloads = filterPlatformPayloads(manifest.Payloads, "amd64")
	manifest.Images = nil
	manifest.Generation.ArchivePath = "payloads/generation/archive.tar.zst"

	generation, imageDigest := haulerGenerationFixture(t, manifest.Generation.Capsule, amd64())
	manifest.Images = []ImageIdentity{{
		Role:      ImageRoom,
		Reference: "ghcr.io/joshyorko/room-of-requirement@sha256:" + imageDigest,
		Digest:    "sha256:" + imageDigest,
		Platform:  amd64(),
	}}
	generationDigest := digestBytes(generation)
	manifest.Generation.Ref.ArchiveSHA256 = generationDigest
	manifest.Lineage.SourceGeneration = manifest.Generation.Ref

	payloads := make(map[string][]byte, len(manifest.Payloads))
	payloads[manifest.Generation.ArchivePath] = generation
	setPayloadBytes(&manifest, manifest.Generation.ArchivePath, generation, "")

	ref := domain.GenerationRef{
		Generation:    manifest.Generation.Ref.Generation,
		ArchiveSHA256: manifest.Generation.Ref.ArchiveSHA256,
	}
	parent := domain.GenerationRef{
		Generation:    manifest.Generation.Parent.Generation,
		ArchiveSHA256: manifest.Generation.Parent.ArchiveSHA256,
	}
	lineage := domain.Lineage{Branch: manifest.Generation.Branch}
	objectKey, err := coordination.GenerationObjectKey(manifest.Generation.Capsule, lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	metadataKey, err := coordination.GenerationMetadataKey(manifest.Generation.Capsule, lineage, ref)
	if err != nil {
		t.Fatal(err)
	}
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion,
		Capsule:       manifest.Generation.Capsule,
		Lineage:       lineage,
		Generation:    ref,
		Parent:        &parent,
		ObjectKey:     objectKey,
		MetadataKey:   metadataKey,
		Size:          int64(len(generation)),
		CreatedAt:     manifest.Lineage.ExportedAt.Add(-time.Minute),
		Tools:         domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.2"},
		SessionID:     "session-campkit-test",
		Verified:      domain.Verification{LocalHaulLoadable: true, RemoteBytesVerified: true},
	}
	metadataBody := mustJSON(t, metadata)
	payloads[manifest.Generation.MetadataPath] = metadataBody
	setPayloadBytes(&manifest, manifest.Generation.MetadataPath, metadataBody, "")

	for i := range manifest.Payloads {
		payload := &manifest.Payloads[i]
		if _, exists := payloads[payload.Path]; exists {
			continue
		}
		body := []byte("payload:" + payload.Path)
		executableDigest := digestBytes(body)
		if payload.Role == PayloadTool && payload.Name == "hauler" {
			body = haulerToolFixture(t, []byte("hauler executable"))
			executableDigest = digestBytes([]byte("hauler executable"))
		}
		payloads[payload.Path] = body
		setPayloadBytes(&manifest, payload.Path, body, executableDigest)
	}

	entries := fixtureEntries(manifest, payloads, sortedPayloadPaths(manifest))
	return campKitFixture(t, entries), manifest, payloads
}

func filterPlatformPayloads(payloads []PayloadIdentity, architecture string) []PayloadIdentity {
	result := make([]PayloadIdentity, 0, len(payloads))
	for _, payload := range payloads {
		if payload.Platform == nil || payload.Platform.Architecture == architecture {
			result = append(result, payload)
		}
	}
	return result
}

func fixtureEntries(manifest Manifest, payloads map[string][]byte, paths []string) []fixtureEntry {
	body, err := MarshalCanonical(manifest)
	if err != nil {
		panic(err)
	}
	entries := []fixtureEntry{{name: "manifest.json", body: body}}
	for _, path := range paths {
		entries = append(entries, fixtureEntry{name: path, body: payloads[path]})
	}
	return entries
}

func sortedPayloadPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest.Payloads))
	for _, payload := range manifest.Payloads {
		paths = append(paths, payload.Path)
	}
	sort.Strings(paths)
	return paths
}

func setPayloadBytes(manifest *Manifest, path string, body []byte, executableDigest string) {
	for i := range manifest.Payloads {
		if manifest.Payloads[i].Path != path {
			continue
		}
		manifest.Payloads[i].Size = int64(len(body))
		manifest.Payloads[i].SHA256 = digestBytes(body)
		if manifest.Payloads[i].Platform != nil {
			if executableDigest == "" {
				executableDigest = manifest.Payloads[i].SHA256
			}
			manifest.Payloads[i].ExecutableSHA256 = executableDigest
		}
		return
	}
	panic("payload not found: " + path)
}

func haulerGenerationFixture(t *testing.T, capsule string, platform Platform) ([]byte, string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	layer := []byte("root archive fixture")
	configDigest := digestBytes(config)
	layerDigest := digestBytes(layer)
	imageManifest := mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.image.config.v1+json",
			"digest":    "sha256:" + configDigest,
			"size":      len(config),
		},
		"layers": []map[string]any{{
			"mediaType": "application/vnd.oci.image.layer.v1.tar+gzip",
			"digest":    "sha256:" + layerDigest,
			"size":      len(layer),
		}},
	})
	imageDigest := digestBytes(imageManifest)

	fileConfig := []byte(`{}`)
	rootLayer := []byte("root-capsule-tar-zstd")
	fileConfigDigest := digestBytes(fileConfig)
	rootLayerDigest := digestBytes(rootLayer)
	fileManifest := mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"artifactType":  "application/vnd.hauler.file",
		"config": map[string]any{
			"mediaType": "application/vnd.oci.empty.v1+json",
			"digest":    "sha256:" + fileConfigDigest,
			"size":      len(fileConfig),
		},
		"layers": []map[string]any{{
			"mediaType": "application/octet-stream",
			"digest":    "sha256:" + rootLayerDigest,
			"size":      len(rootLayer),
		}},
	})
	fileDigest := digestBytes(fileManifest)
	index := mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests": []map[string]any{
			{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"digest":    "sha256:" + fileDigest,
				"size":      len(fileManifest),
				"annotations": map[string]string{
					"org.opencontainers.image.ref.name": "hauler/" + capsule + ".tar.zst:latest",
				},
			},
			{
				"mediaType": "application/vnd.oci.image.manifest.v1+json",
				"digest":    "sha256:" + imageDigest,
				"size":      len(imageManifest),
				"platform": map[string]string{
					"os":           platform.OS,
					"architecture": platform.Architecture,
				},
				"annotations": map[string]string{
					"io.containerd.image.name": "ghcr.io/joshyorko/room-of-requirement:fixture",
				},
			},
		},
	})
	blobs := map[string][]byte{
		configDigest:     config,
		layerDigest:      layer,
		imageDigest:      imageManifest,
		fileConfigDigest: fileConfig,
		rootLayerDigest:  rootLayer,
		fileDigest:       fileManifest,
	}
	entries := []nestedFixtureEntry{
		{name: "blobs/", mode: 0o755, typeflag: tar.TypeDir},
		{name: "blobs/sha256/", mode: 0o755, typeflag: tar.TypeDir},
	}
	digests := make([]string, 0, len(blobs))
	for digest := range blobs {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for _, digest := range digests {
		entries = append(entries, nestedFixtureEntry{name: "blobs/sha256/" + digest, mode: 0o644, typeflag: tar.TypeReg, body: blobs[digest]})
	}
	entries = append(entries,
		nestedFixtureEntry{name: "index.json", mode: 0o644, typeflag: tar.TypeReg, body: index},
		nestedFixtureEntry{name: "manifest.json", mode: 0o644, typeflag: tar.TypeReg, body: []byte("[]\n")},
		nestedFixtureEntry{name: "oci-layout", mode: 0o644, typeflag: tar.TypeReg, body: []byte(`{"imageLayoutVersion":"1.0.0"}`)},
	)
	return nestedTarZstdFixture(t, entries), imageDigest
}

type nestedFixtureEntry struct {
	name     string
	mode     int64
	typeflag byte
	body     []byte
}

func nestedTarZstdFixture(t *testing.T, entries []nestedFixtureEntry) []byte {
	t.Helper()
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed, zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(encoder)
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Typeflag: entry.typeflag,
			Size: int64(len(entry.body)), ModTime: time.Unix(0, 0), Format: tar.FormatUSTAR,
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) > 0 {
			if _, err := writer.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func haulerToolFixture(t *testing.T, executable []byte) []byte {
	t.Helper()
	var body bytes.Buffer
	gzipWriter := gzip.NewWriter(&body)
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{Name: "hauler", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(executable))}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(executable); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func digestBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

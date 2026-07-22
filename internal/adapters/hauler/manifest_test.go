package hauler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

func TestRenderedManifestContainsOnlyFilesAndImagesDocuments(t *testing.T) {
	t.Parallel()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	body, err := RenderManifest("second-brain", domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		CapturedReference: "127.0.0.1:5000/camp/z:v1", CapturedManifestDigest: digest, Source: domain.ImageSourceRegistry,
	}}})
	if err != nil {
		t.Fatalf("RenderManifest() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "hauler-manifest.yaml")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readGenerationExpectations(path); err != nil {
		t.Fatalf("rendered manifest cannot be consumed by generation assembly: %v\n%s", err, body)
	}
}

func TestManifestIsDeterministicAndLocalOnlyForDaemonSources(t *testing.T) {
	t.Parallel()
	inventory := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(100, 0), Images: []domain.Image{
		{CapturedReference: "127.0.0.1:5000/camp/z:v1", CapturedManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, Source: domain.ImageSourceRegistry},
		{CapturedReference: "example.test/a:v2", Platform: domain.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}, Source: domain.ImageSourceDaemon},
	}}
	body, err := RenderManifest("second-brain", inventory)
	if err != nil {
		t.Fatalf("RenderManifest() error = %v", err)
	}
	text := string(body)
	if strings.Index(text, "127.0.0.1:5000/camp/z@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") > strings.Index(text, "example.test/a:v2") {
		t.Fatalf("images are not sorted: %s", text)
	}
	if strings.Contains(text, "127.0.0.1:5000/camp/z:v1") || strings.Count(text, "local: true") != 1 || !strings.Contains(text, "platform: linux/arm64/v8") || !strings.Contains(text, "path: .camp/build/second-brain.tar.zst") {
		t.Fatalf("manifest semantics are wrong: %s", text)
	}
	second, err := RenderManifest("second-brain", inventory)
	if err != nil || string(second) != text {
		t.Fatalf("second manifest drifted: %v", err)
	}
}

func TestManifestRejectsMutableRegistryTagWithoutVerifiedDigest(t *testing.T) {
	t.Parallel()
	_, err := RenderManifest("second-brain", domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		CapturedReference: "127.0.0.1:5000/camp/z:v1",
		Platform:          domain.Platform{OS: "linux", Architecture: "amd64"},
		Source:            domain.ImageSourceRegistry,
	}}})
	if err == nil || !strings.Contains(err.Error(), "verified digest") {
		t.Fatalf("RenderManifest() error = %v, want verified digest rejection", err)
	}
}

func TestManifestAcceptsDigestPinnedDirectPushWithoutPlatformAndRejectsUnknownSource(t *testing.T) {
	t.Parallel()
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	body, err := RenderManifest("second-brain", domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		CapturedReference: "127.0.0.1:5000/manual/tool:latest", CapturedManifestDigest: digest, Source: domain.ImageSourceRegistry,
	}}})
	if err != nil {
		t.Fatalf("RenderManifest(direct push) error = %v", err)
	}
	if text := string(body); !strings.Contains(text, "127.0.0.1:5000/manual/tool@"+digest) || strings.Contains(text, "platform:") {
		t.Fatalf("direct push manifest = %s", text)
	}
	_, err = RenderManifest("second-brain", domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		CapturedReference: "127.0.0.1:5000/manual/tool:latest", CapturedManifestDigest: digest, Source: domain.ImageSource("mystery"),
	}}})
	if err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("RenderManifest(unknown source) error = %v", err)
	}
}

func TestManifestPrefersDeterministicSameRegistryOriginalReference(t *testing.T) {
	t.Parallel()
	digest := "sha256:" + strings.Repeat("c", 64)
	body, err := RenderManifest("second-brain", domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
		CapturedReference: "127.0.0.1:5000/camp/captured:v1", CapturedManifestDigest: digest,
		OriginalTags: []string{"127.0.0.1:5000/camp-acceptance:named", "example.test/team/app:v1"}, Source: domain.ImageSourceRegistry,
	}}})
	if err != nil {
		t.Fatalf("RenderManifest() error = %v", err)
	}
	text := string(body)
	if !strings.Contains(text, "127.0.0.1:5000/camp-acceptance@"+digest) || strings.Contains(text, "camp/captured@") || strings.Contains(text, "example.test/team/app") {
		t.Fatalf("manifest did not select the same-registry direct reference: %s", text)
	}
}

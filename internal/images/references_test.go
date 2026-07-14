package images

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

func TestAssignReferencesIsDeterministicCollisionResistantAndBounded(t *testing.T) {
	t.Parallel()
	created := time.Unix(100, 0).UTC()
	longRepository := strings.Repeat("nested-component/", 20) + "app"
	input := []EngineImage{
		{ID: "sha256:aaa", Tags: []string{"registry-a.test:5443/team/same:v1"}, Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: created},
		{ID: "sha256:bbb", Tags: []string{"registry-b.test:5443/team/same:v1"}, Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: created},
		{ID: "sha256:ccc", Tags: []string{"registry-c.test/" + longRepository + ":latest"}, Platform: domain.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}, CreatedAt: created},
	}
	first, err := AssignReferences("127.0.0.1:45001", "Second_Brain", input)
	if err != nil {
		t.Fatalf("AssignReferences() error = %v", err)
	}
	reversed := []EngineImage{input[2], input[1], input[0]}
	second, err := AssignReferences("127.0.0.1:45001", "Second_Brain", reversed)
	if err != nil {
		t.Fatalf("AssignReferences(reversed) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("assignment changed with input order:\n%#v\n%#v", first, second)
	}
	if len(first) != 3 || first[0].CapturedReference == first[1].CapturedReference {
		t.Fatalf("captured references = %#v", first)
	}
	for _, image := range first {
		if !strings.HasPrefix(image.CapturedReference, "127.0.0.1:45001/camp/") || !strings.HasSuffix(image.CapturedReference, ":captured") || len(image.CapturedReference) > 240 {
			t.Fatalf("captured reference is invalid or unbounded: %q", image.CapturedReference)
		}
		if image.Source != domain.ImageSourceRegistry || image.CapturedManifestDigest != "" || len(image.OriginalTags) != 1 {
			t.Fatalf("captured image = %#v", image)
		}
	}
}

func TestRewriteRegistryAuthorityPreservesPrivatePathTagAndDigest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		reference string
		want      string
	}{
		{"tag", "old.test:5443/camp/brain/image:captured", "127.0.0.1:5001/camp/brain/image:captured"},
		{"digest", "old.test:5443/team/direct@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "127.0.0.1:5001/team/direct@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := RewriteRegistryAuthority(test.reference, "127.0.0.1:5001")
			if err != nil || got != test.want {
				t.Fatalf("RewriteRegistryAuthority() = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestAssignReferencesIgnoresCampGeneratedRepoDigestDrift(t *testing.T) {
	t.Parallel()
	base := EngineImage{
		ID: "sha256:aaa", Tags: []string{"registry.test:5443/team/app:v1"},
		RepoDigests: []string{"registry.test:5443/team/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		Platform:    domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: time.Unix(100, 0).UTC(),
	}
	first, err := AssignReferences("127.0.0.1:45001", "brain", []EngineImage{base})
	if err != nil {
		t.Fatal(err)
	}
	drifted := base
	drifted.RepoDigests = append(append([]string(nil), base.RepoDigests...), "127.0.0.1:45001/camp/brain/image@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	second, err := AssignReferences("127.0.0.1:45001", "brain", []EngineImage{drifted})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Camp-generated repo digest changed capture identity:\n%#v\n%#v", first, second)
	}
}

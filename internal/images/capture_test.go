package images

import (
	"context"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type fakeRegistryCatalog struct {
	calls   []ports.RegistryReference
	results []string
	errors  []error
}

func (c *fakeRegistryCatalog) List(context.Context, string) ([]ports.RegistryReference, error) {
	panic("not used")
}

func (c *fakeRegistryCatalog) Resolve(_ context.Context, _ string, repository, reference string) (string, error) {
	c.calls = append(c.calls, ports.RegistryReference{Repository: repository, Tag: reference})
	index := len(c.calls) - 1
	var result string
	if index < len(c.results) {
		result = c.results[index]
	}
	if index < len(c.errors) {
		return result, c.errors[index]
	}
	return result, nil
}

type imageClock struct{ now time.Time }

func (c imageClock) Now() time.Time { return c.now }
func (c imageClock) NewTicker(time.Duration) ports.Ticker {
	panic("not used")
}

func TestCapturerDoesNotCaptureUnrelatedNamedWorkspaceImage(t *testing.T) {
	t.Parallel()
	created := time.Unix(100, 0).UTC()
	engineImage := EngineImage{
		ID: "sha256:aaa", Tags: []string{"example.test/unrelated/app:v1"},
		Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: created,
	}
	assigned, err := AssignReferences("127.0.0.1:45001", "brain", []EngineImage{engineImage})
	if err != nil {
		t.Fatal(err)
	}
	repository, _, err := splitCapturedTag(assigned[0].CapturedReference)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	repoDigest := "127.0.0.1:45001/" + repository + "@" + digest
	inspect := `[{"Id":"sha256:aaa","RepoTags":["example.test/unrelated/app:v1"],"RepoDigests":["` + repoDigest + `"],"Created":"` + created.Format(time.RFC3339Nano) + `","Architecture":"amd64","Os":"linux","Config":{"Labels":{}}}]`
	executor := &recordingWorkspaceExecutor{results: []ports.Result{
		{Stdout: []byte("26.1.0\n")},
		{Stdout: []byte("sha256:aaa\n")},
		{Stdout: []byte(inspect)},
		{Stdout: []byte(inspect)},
	}}
	catalog := &fakeRegistryCatalog{results: []string{digest}}

	result, err := NewCapturer(executor, catalog, imageClock{now: time.Unix(200, 0).UTC()}).Capture(context.Background(), CaptureRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, Capsule: "brain",
		RegistryAuthority: "127.0.0.1:45001", RegistryEndpoint: "http://127.0.0.1:45001",
		Previous: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
			EngineImageID: "sha256:aaa", OriginalTags: []string{"example.test/unrelated/app:v1"},
			CapturedReference: "127.0.0.1:45001/camp/legacy:captured", CapturedManifestDigest: digest,
			Source: domain.ImageSourceRegistry,
		}}},
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if len(result.Images) != 0 {
		t.Fatalf("captured unrelated workspace images = %#v", result.Images)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("workspace engine commands = %#v, want none", executor.commands)
	}
}

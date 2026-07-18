package images

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

func TestCapturerTagsPushesVerifiesAndRemovesOnlyTemporaryReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Unix(200, 0).UTC()
	created := time.Unix(100, 0).UTC()
	engineImage := EngineImage{
		ID: "sha256:aaa", Tags: []string{"example.test/team/app:v1", "example.test/team/app:stable"},
		Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: created,
	}
	assigned, err := AssignReferences("127.0.0.1:45001", "brain", []EngineImage{engineImage})
	if err != nil {
		t.Fatal(err)
	}
	captured := assigned[0].CapturedReference
	repository, tag, err := splitCapturedTag(captured)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	inspectOriginal := `[{"Id":"sha256:aaa","RepoTags":["example.test/team/app:v1","example.test/team/app:stable"],"RepoDigests":[],"Created":"` + created.Format(time.RFC3339Nano) + `","Architecture":"amd64","Os":"linux","Config":{"Labels":{}}}]`
	inspectCaptured := `[{"Id":"sha256:aaa","RepoTags":["` + captured + `"],"RepoDigests":["127.0.0.1:45001/` + repository + `@` + digest + `"],"Created":"` + created.Format(time.RFC3339Nano) + `","Architecture":"amd64","Os":"linux","Config":{"Labels":{}}}]`
	executor := &recordingWorkspaceExecutor{results: []ports.Result{
		{Stdout: []byte("26.1.0\n")},
		{Stdout: []byte("sha256:aaa\n")},
		{Stdout: []byte(inspectOriginal)},
		{}, {}, {Stdout: []byte(inspectCaptured)}, {},
	}}
	catalog := &fakeRegistryCatalog{results: []string{"", digest}, errors: []error{ports.ErrNotFound, nil}}
	result, err := NewCapturer(executor, catalog, imageClock{now: now}).Capture(ctx, CaptureRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, Capsule: "brain",
		RegistryAuthority: "127.0.0.1:45001", RegistryEndpoint: "http://127.0.0.1:45001",
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}
	if result.SchemaVersion != domain.SchemaVersion || result.GeneratedAt != now || len(result.Images) != 1 || result.Images[0].CapturedManifestDigest != digest || result.Images[0].Source != domain.ImageSourceRegistry {
		t.Fatalf("inventory = %#v", result)
	}
	wantTail := [][]string{
		{"docker", "image", "tag", engineImage.ID, captured},
		{"docker", "image", "push", captured},
		{"docker", "image", "inspect", captured},
		{"docker", "image", "rm", captured},
	}
	if len(executor.commands) != 7 {
		t.Fatalf("commands = %#v", executor.commands)
	}
	for index, want := range wantTail {
		if !reflect.DeepEqual(executor.commands[index+3].Argv, want) {
			t.Fatalf("command %d = %#v, want %#v", index+3, executor.commands[index+3].Argv, want)
		}
	}
	if len(catalog.calls) != 2 || catalog.calls[0].Repository != repository || catalog.calls[0].Tag != tag {
		t.Fatalf("catalog calls = %#v", catalog.calls)
	}
}

func TestCapturerRejectsKnownReferenceDigestMismatchBeforeTagOrPush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	created := time.Unix(100, 0).UTC()
	engineImage := EngineImage{ID: "sha256:aaa", Tags: []string{"example.test/team/app:v1"}, Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: created}
	assigned, _ := AssignReferences("127.0.0.1:45001", "brain", []EngineImage{engineImage})
	assigned[0].CapturedManifestDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	inspect := `[{"Id":"sha256:aaa","RepoTags":["example.test/team/app:v1"],"RepoDigests":[],"Created":"` + created.Format(time.RFC3339Nano) + `","Architecture":"amd64","Os":"linux","Config":{"Labels":{}}}]`
	executor := &recordingWorkspaceExecutor{results: []ports.Result{{Stdout: []byte("26.1.0\n")}, {Stdout: []byte("sha256:aaa\n")}, {Stdout: []byte(inspect)}}}
	catalog := &fakeRegistryCatalog{results: []string{"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	_, err := NewCapturer(executor, catalog, imageClock{now: time.Unix(200, 0).UTC()}).Capture(ctx, CaptureRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, Capsule: "brain", RegistryAuthority: "127.0.0.1:45001",
		RegistryEndpoint: "http://127.0.0.1:45001", Previous: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: assigned},
	})
	if !errors.Is(err, ErrCapturedDigestMismatch) {
		t.Fatalf("Capture() error = %v, want ErrCapturedDigestMismatch", err)
	}
	if len(executor.commands) != 3 || strings.Contains(strings.Join(executor.commands[len(executor.commands)-1].Argv, " "), " push ") {
		t.Fatalf("commands after mismatch = %#v", executor.commands)
	}
}

func TestCapturerRecoversUnjournaledPushOnlyFromExactLocalImageAndDigest(t *testing.T) {
	t.Parallel()
	created := time.Unix(100, 0).UTC()
	engineImage := EngineImage{ID: "sha256:aaa", Tags: []string{"example.test/team/app:v1"}, Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, CreatedAt: created}
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
	inspect := `[{"Id":"sha256:aaa","RepoTags":["example.test/team/app:v1"],"RepoDigests":["` + repoDigest + `"],"Created":"` + created.Format(time.RFC3339Nano) + `","Architecture":"amd64","Os":"linux","Config":{"Labels":{}}}]`
	executor := &recordingWorkspaceExecutor{results: []ports.Result{
		{Stdout: []byte("26.1.0\n")}, {Stdout: []byte("sha256:aaa\n")}, {Stdout: []byte(inspect)}, {Stdout: []byte(inspect)},
	}}
	catalog := &fakeRegistryCatalog{results: []string{digest}}

	result, err := NewCapturer(executor, catalog, imageClock{now: time.Unix(200, 0).UTC()}).Capture(context.Background(), CaptureRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, Capsule: "brain",
		RegistryAuthority: "127.0.0.1:45001", RegistryEndpoint: "http://127.0.0.1:45001",
	})
	if err != nil {
		t.Fatalf("Capture(recovery) error = %v", err)
	}
	if len(result.Images) != 1 || result.Images[0].CapturedManifestDigest != digest {
		t.Fatalf("Capture(recovery) = %#v", result)
	}
	if len(executor.commands) != 4 || !reflect.DeepEqual(executor.commands[3].Argv, []string{"docker", "image", "inspect", engineImage.ID}) {
		t.Fatalf("recovery commands = %#v", executor.commands)
	}
}

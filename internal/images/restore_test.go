package images

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestRestorerPullsDigestVerifiesAndRetagsEveryOriginalName(t *testing.T) {
	t.Parallel()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	repository := "camp/brain/app"
	current := "127.0.0.1:5001/" + repository
	pulled := current + "@" + digest
	executor := &recordingWorkspaceExecutor{results: []ports.Result{
		{Stdout: []byte("29.6.1\n")},
		{},
		{Stdout: []byte(`[{"Id":"sha256:image","RepoDigests":["` + pulled + `"],"Os":"linux","Architecture":"amd64"}]`)},
		{ExitCode: 1}, {}, {Stdout: []byte(`[{"Id":"sha256:image"}]`)},
		{ExitCode: 1}, {}, {Stdout: []byte(`[{"Id":"sha256:image"}]`)},
	}}
	catalog := &fakeRegistryCatalog{results: []string{digest}}
	result, err := NewRestorer(executor, catalog).Restore(context.Background(), RestoreRequest{
		Scope:             EngineScope{Context: "default", WorkspaceID: "brain-main"},
		RegistryAuthority: "127.0.0.1:5001", RegistryEndpoint: "http://127.0.0.1:5001",
		Inventory: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: time.Unix(100, 0), Images: []domain.Image{{
			EngineImageID: "sha256:old", OriginalTags: []string{"example.test/team/app:v1", "example.test/team/app:stable"},
			CapturedReference: "127.0.0.1:45001/" + repository + ":captured", CapturedManifestDigest: digest,
			Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, Source: domain.ImageSourceRegistry,
		}}},
	})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if result.Restored != 1 || result.Tags != 2 {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(catalog.calls, []ports.RegistryReference{{Repository: repository, Tag: digest}}) {
		t.Fatalf("registry resolution = %#v, want immutable digest lookup", catalog.calls)
	}
	want := [][]string{
		{"docker", "image", "pull", "--platform", "linux/amd64", pulled},
		{"docker", "image", "inspect", pulled},
		{"docker", "image", "inspect", "example.test/team/app:stable"},
		{"docker", "image", "tag", "sha256:image", "example.test/team/app:stable"},
		{"docker", "image", "inspect", "example.test/team/app:stable"},
		{"docker", "image", "inspect", "example.test/team/app:v1"},
		{"docker", "image", "tag", "sha256:image", "example.test/team/app:v1"},
		{"docker", "image", "inspect", "example.test/team/app:v1"},
	}
	if len(executor.commands) != 1+len(want) {
		t.Fatalf("commands = %#v", executor.commands)
	}
	for index := range want {
		if !reflect.DeepEqual(executor.commands[index+1].Argv, want[index]) {
			t.Fatalf("command %d = %#v, want %#v", index, executor.commands[index+1].Argv, want[index])
		}
	}
}

func TestRestorerIsIdempotentAndDoesNotOverwriteConflictingOriginalTag(t *testing.T) {
	t.Parallel()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	pulled := "127.0.0.1:5001/camp/app@" + digest
	base := RestoreRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, RegistryAuthority: "127.0.0.1:5001", RegistryEndpoint: "http://127.0.0.1:5001",
		Inventory: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
			OriginalTags: []string{"example.test/app:v1"}, CapturedReference: "127.0.0.1:45001/camp/app:captured", CapturedManifestDigest: digest,
			Platform: domain.Platform{OS: "linux", Architecture: "amd64"}, Source: domain.ImageSourceRegistry,
		}}},
	}
	t.Run("already restored", func(t *testing.T) {
		executor := &recordingWorkspaceExecutor{results: []ports.Result{
			{Stdout: []byte("29.6.1\n")}, {},
			{Stdout: []byte(`[{"Id":"sha256:image","RepoDigests":["` + pulled + `"],"Os":"linux","Architecture":"amd64"}]`)},
			{Stdout: []byte(`[{"Id":"sha256:image"}]`)},
		}}
		result, err := NewRestorer(executor, &fakeRegistryCatalog{results: []string{digest}}).Restore(context.Background(), base)
		if err != nil || result.Restored != 1 || result.Tags != 1 {
			t.Fatalf("Restore() = %#v, %v", result, err)
		}
		for _, command := range executor.commands {
			if reflect.DeepEqual(command.Argv[:min(len(command.Argv), 3)], []string{"docker", "image", "tag"}) {
				t.Fatalf("idempotent restore retagged existing image: %#v", executor.commands)
			}
		}
	})
	t.Run("conflict", func(t *testing.T) {
		executor := &recordingWorkspaceExecutor{results: []ports.Result{
			{Stdout: []byte("29.6.1\n")}, {},
			{Stdout: []byte(`[{"Id":"sha256:image","RepoDigests":["` + pulled + `"],"Os":"linux","Architecture":"amd64"}]`)},
			{Stdout: []byte(`[{"Id":"sha256:other"}]`)},
		}}
		_, err := NewRestorer(executor, &fakeRegistryCatalog{results: []string{digest}}).Restore(context.Background(), base)
		if !errors.Is(err, ErrOriginalTagConflict) {
			t.Fatalf("Restore() error = %v, want ErrOriginalTagConflict", err)
		}
		if len(executor.commands) != 4 {
			t.Fatalf("restore mutated a conflicting tag: %#v", executor.commands)
		}
	})
}

func TestRestorerRejectsPostPullDigestOrPlatformMismatchAndSupportsDirectOnlyEntry(t *testing.T) {
	t.Parallel()
	digest := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	request := RestoreRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, RegistryAuthority: "127.0.0.1:5001", RegistryEndpoint: "http://127.0.0.1:5001",
		Inventory: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
			CapturedReference: "127.0.0.1:45001/manual/tool:latest", CapturedManifestDigest: digest,
			Platform: domain.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}, Source: domain.ImageSourceRegistry,
		}}},
	}
	for _, test := range []struct {
		name       string
		inspection string
		wantError  bool
	}{
		{"digest mismatch", `[{"Id":"sha256:image","RepoDigests":[],"Os":"linux","Architecture":"arm64","Variant":"v8"}]`, true},
		{"platform mismatch", `[{"Id":"sha256:image","RepoDigests":["127.0.0.1:5001/manual/tool@` + digest + `"],"Os":"linux","Architecture":"amd64"}]`, true},
		{"direct only", `[{"Id":"sha256:image","RepoDigests":["127.0.0.1:5001/manual/tool@` + digest + `"],"Os":"linux","Architecture":"arm64","Variant":"v8"}]`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			executor := &recordingWorkspaceExecutor{results: []ports.Result{{Stdout: []byte("29.6.1\n")}, {}, {Stdout: []byte(test.inspection)}}}
			result, err := NewRestorer(executor, &fakeRegistryCatalog{results: []string{digest}}).Restore(context.Background(), request)
			if (err != nil) != test.wantError {
				t.Fatalf("Restore() = %#v, %v", result, err)
			}
			if got := executor.commands[1].Argv; !reflect.DeepEqual(got, []string{"docker", "image", "pull", "--platform", "linux/arm64/v8", "127.0.0.1:5001/manual/tool@" + digest}) {
				t.Fatalf("pull argv = %#v", got)
			}
		})
	}
}

func TestRestorerRejectsCatalogDigestMismatchBeforePull(t *testing.T) {
	t.Parallel()
	executor := &recordingWorkspaceExecutor{results: []ports.Result{{Stdout: []byte("29.6.1\n")}}}
	catalog := &fakeRegistryCatalog{results: []string{"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	_, err := NewRestorer(executor, catalog).Restore(context.Background(), RestoreRequest{
		Scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}, RegistryAuthority: "127.0.0.1:5001", RegistryEndpoint: "http://127.0.0.1:5001",
		Inventory: domain.ImageInventory{SchemaVersion: domain.SchemaVersion, Images: []domain.Image{{
			OriginalTags: []string{"example.test/app:v1"}, CapturedReference: "127.0.0.1:45001/camp/app:captured",
			CapturedManifestDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Source: domain.ImageSourceRegistry,
		}}},
	})
	if !errors.Is(err, ErrCapturedDigestMismatch) {
		t.Fatalf("Restore() error = %v, want ErrCapturedDigestMismatch", err)
	}
	if len(executor.commands) != 1 {
		t.Fatalf("pull ran after digest mismatch: %#v", executor.commands)
	}
}

package images

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

func TestInventoryEnumeratesNamedImagesDeterministicallyAndExcludesDanglingAndOptOut(t *testing.T) {
	t.Parallel()
	created := "2026-07-14T12:34:56.123456789Z"
	inspect := `[
  {"Id":"sha256:bbb","RepoTags":["registry-b.test/team/app:v1","registry-a.test/team/app:v1"],"RepoDigests":["registry-b.test/team/app@sha256:222","registry-a.test/team/app@sha256:111"],"Created":"` + created + `","Architecture":"amd64","Os":"linux","Variant":"v3","Config":{"Labels":{}}},
  {"Id":"sha256:dangling","RepoTags":["<none>:<none>"],"RepoDigests":[],"Created":"` + created + `","Architecture":"amd64","Os":"linux","Config":{"Labels":{}}},
  {"Id":"sha256:opted-out","RepoTags":["example.test/private:skip"],"RepoDigests":[],"Created":"` + created + `","Architecture":"arm64","Os":"linux","Labels":{"dev.camp.exclude":"true"},"Config":{"Labels":{}}},
  {"Id":"sha256:explicit","RepoTags":["example.test/private:excluded"],"RepoDigests":[],"Created":"` + created + `","Architecture":"arm64","Os":"linux","Config":{"Labels":{}}}
]`
	executor := &recordingWorkspaceExecutor{results: []ports.Result{
		{Stdout: []byte("sha256:bbb\nsha256:dangling\nsha256:bbb\nsha256:opted-out\nsha256:explicit\n")},
		{Stdout: []byte(inspect)},
	}}
	engine := Engine{Kind: EngineDocker, Executable: "docker", executor: executor, scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}}
	got, err := NewInventory().Enumerate(context.Background(), engine, InventoryOptions{ExcludeTags: []string{"example.test/private:excluded"}})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	wantTime, _ := time.Parse(time.RFC3339Nano, created)
	want := []EngineImage{{
		ID:          "sha256:bbb",
		Tags:        []string{"registry-a.test/team/app:v1", "registry-b.test/team/app:v1"},
		RepoDigests: []string{"registry-a.test/team/app@sha256:111", "registry-b.test/team/app@sha256:222"},
		Platform:    domain.Platform{OS: "linux", Architecture: "amd64", Variant: "v3"},
		CreatedAt:   wantTime,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("images = %#v, want %#v", got, want)
	}
	wantCommands := []ports.WorkspaceCommand{
		{Context: "default", WorkspaceID: "brain-main", Argv: []string{"docker", "image", "ls", "--all", "--quiet", "--no-trunc"}},
		{Context: "default", WorkspaceID: "brain-main", Argv: []string{"docker", "image", "inspect", "sha256:bbb", "sha256:dangling", "sha256:explicit", "sha256:opted-out"}},
	}
	if !reflect.DeepEqual(executor.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, wantCommands)
	}
}

func TestInventoryBatchesImageInspection(t *testing.T) {
	t.Parallel()
	const count = 129
	created := time.Unix(100, 0).UTC()
	ids := make([]string, 0, count)
	inspected := make([]inspectedImage, 0, count)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("sha256:%03d", index)
		ids = append(ids, id)
		inspected = append(inspected, inspectedImage{
			ID: id, RepoTags: []string{fmt.Sprintf("example.test/image-%03d:v1", index)}, Created: created.Format(time.RFC3339Nano),
			OS: "linux", Architecture: "amd64",
		})
	}
	first, _ := json.Marshal(inspected[:128])
	second, _ := json.Marshal(inspected[128:])
	executor := &recordingWorkspaceExecutor{results: []ports.Result{
		{Stdout: []byte(strings.Join(ids, "\n") + "\n")}, {Stdout: first}, {Stdout: second},
	}}
	engine := Engine{Kind: EngineDocker, Executable: "docker", executor: executor, scope: EngineScope{Context: "default", WorkspaceID: "brain-main"}}
	images, err := NewInventory().Enumerate(context.Background(), engine, InventoryOptions{})
	if err != nil {
		t.Fatalf("Enumerate() error = %v", err)
	}
	if len(images) != count || len(executor.commands) != 3 || len(executor.commands[1].Argv) != 3+128 || len(executor.commands[2].Argv) != 4 {
		t.Fatalf("images=%d commands=%#v", len(images), executor.commands)
	}
}

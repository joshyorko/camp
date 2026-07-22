package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type hardlinkExecutor struct {
	commands []ports.WorkspaceCommand
	err      error
}

func (e *hardlinkExecutor) Execute(_ context.Context, command ports.WorkspaceCommand) (ports.Result, error) {
	e.commands = append(e.commands, command)
	if e.err != nil {
		return ports.Result{}, e.err
	}
	return ports.Result{}, nil
}

func TestRestoreHardlinksExecutesDeterministicallyForExtraLinks(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	remoteRoot := filepath.Join(t.TempDir(), "remote-root")
	if err := os.Mkdir(remoteRoot, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	mustWrite := func(path, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(localRoot, path), []byte(contents), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
	}
	mustLink := func(oldPath, newPath string) {
		t.Helper()
		if err := os.Link(filepath.Join(localRoot, oldPath), filepath.Join(localRoot, newPath)); err != nil {
			t.Fatalf("Link(%s,%s) error = %v", oldPath, newPath, err)
		}
	}

	if err := os.MkdirAll(filepath.Join(localRoot, "z"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(localRoot, "a"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	mustWrite("solo.txt", "solo")
	mustWrite("z/third.txt", "group-z")
	mustLink("z/third.txt", "a/first.txt")
	mustLink("z/third.txt", "middle.txt")
	mustWrite("b.txt", "group-b")
	mustLink("b.txt", "c.txt")

	executor := &hardlinkExecutor{}
	restorer := NewHardlinkRestorer(executor)

	err := restorer.Restore(context.Background(), HardlinkRestoreRequest{
		WorkspaceID: "camp-abcd",
		Context:     "devpod",
		LocalRoot:   localRoot,
		RemoteRoot:  remoteRoot,
	})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	want := []ports.WorkspaceCommand{
		{
			WorkspaceID: "camp-abcd",
			Context:     "devpod",
			Workdir:     remoteRoot,
			Argv:        []string{"ln", "--force", "--", "a/first.txt", "middle.txt"},
		},
		{
			WorkspaceID: "camp-abcd",
			Context:     "devpod",
			Workdir:     remoteRoot,
			Argv:        []string{"ln", "--force", "--", "a/first.txt", "z/third.txt"},
		},
		{
			WorkspaceID: "camp-abcd",
			Context:     "devpod",
			Workdir:     remoteRoot,
			Argv:        []string{"ln", "--force", "--", "b.txt", "c.txt"},
		},
	}
	if !reflect.DeepEqual(executor.commands, want) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, want)
	}
}

func TestRestoreHardlinksRejectsUnsafeRootsAndPaths(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	remoteRoot := filepath.Join(t.TempDir(), "remote-root")
	if err := os.Mkdir(remoteRoot, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	restorer := NewHardlinkRestorer(&hardlinkExecutor{})

	for _, tc := range []struct {
		name      string
		request   HardlinkRestoreRequest
		wantError string
	}{
		{
			name: "relative local root",
			request: HardlinkRestoreRequest{
				WorkspaceID: "camp-abcd",
				LocalRoot:   "relative",
				RemoteRoot:  remoteRoot,
			},
			wantError: "invalid local root",
		},
		{
			name: "relative remote root",
			request: HardlinkRestoreRequest{
				WorkspaceID: "camp-abcd",
				LocalRoot:   localRoot,
				RemoteRoot:  "relative",
			},
			wantError: "invalid remote root",
		},
		{
			name: "workspace root missing",
			request: HardlinkRestoreRequest{
				LocalRoot:  localRoot,
				RemoteRoot: remoteRoot,
			},
			wantError: "incomplete hardlink restore request",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := restorer.Restore(context.Background(), tc.request)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("Restore() error = %v, want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestRestoreHardlinksStopsOnContextCancellation(t *testing.T) {
	t.Parallel()

	localRoot := t.TempDir()
	remoteRoot := filepath.Join(t.TempDir(), "remote-root")
	if err := os.Mkdir(remoteRoot, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "ok.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	executor := &hardlinkExecutor{}
	restorer := NewHardlinkRestorer(executor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := restorer.Restore(ctx, HardlinkRestoreRequest{
		WorkspaceID: "camp-abcd",
		LocalRoot:   localRoot,
		RemoteRoot:  remoteRoot,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore() error = %v, want context.Canceled", err)
	}
	if len(executor.commands) != 0 {
		t.Fatalf("commands = %#v, want none", executor.commands)
	}
}

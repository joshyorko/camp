package integration_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/sshtransfer"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/workspace"
)

func TestRemoteCheckpointLifecycleTransfersRealFilesystemSemantics(t *testing.T) {
	sandbox := t.TempDir()
	source := filepath.Join(sandbox, "remote root ü with spaces")
	createMirrorFixture(t, source)
	ssh := writeLocalSSH(t, sandbox)
	executor := sshtransfer.NewExecutor()

	t.Run("real rsync deletes stale destination content", func(t *testing.T) {
		destination := filepath.Join(sandbox, "rsync destination")
		if err := os.MkdirAll(destination, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, "stale.txt"), []byte("delete me"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", filepath.Dir(ssh)+":"+os.Getenv("PATH"))
		transport := workspace.NewRemote(workspace.RemoteConfig{
			WorkspaceID: "fixture", RsyncExecutable: "/usr/bin/rsync", SSHExecutable: ssh, TarExecutable: "/usr/bin/tar",
		}, staticRootResolver(source), &fixedStaging{roots: []string{destination}}, executor, executor)

		result, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "ssh", StagingRoot: sandbox, AttemptID: "integration-rsync"})
		if err != nil {
			t.Fatalf("ReturnToStaging() error = %v", err)
		}
		if result.Root != destination || result.Mode != workspace.MirrorDevPodSSH {
			t.Fatalf("result = %#v", result)
		}
		assertMirrorFixture(t, source, destination)
		if _, err := os.Lstat(filepath.Join(destination, "stale.txt")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale destination file error = %v, want not exist", err)
		}
	})

	t.Run("forced unavailable rsync uses real tar pipe into fresh destination", func(t *testing.T) {
		failed := filepath.Join(sandbox, "discarded rsync")
		destination := filepath.Join(sandbox, "tar destination")
		staging := &fixedStaging{roots: []string{failed, destination}}
		transport := workspace.NewRemote(workspace.RemoteConfig{
			WorkspaceID: "fixture", RsyncExecutable: filepath.Join(sandbox, "missing-rsync"), SSHExecutable: ssh, TarExecutable: "/usr/bin/tar",
		}, staticRootResolver(source), staging, executor, executor)

		result, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "ssh", StagingRoot: sandbox, AttemptID: "integration-tar"})
		if err != nil {
			t.Fatalf("ReturnToStaging() error = %v", err)
		}
		if result.Root != destination || result.Mode != workspace.MirrorDevPodSSH {
			t.Fatalf("result = %#v", result)
		}
		assertMirrorFixture(t, source, destination)
		if len(staging.discarded) != 1 || staging.discarded[0] != failed {
			t.Fatalf("discarded = %#v", staging.discarded)
		}
	})
}

type staticRootResolver string

func (r staticRootResolver) ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error) {
	return string(r), nil
}

type fixedStaging struct {
	roots     []string
	discarded []string
}

func (s *fixedStaging) Fresh(_ context.Context, _, _ string) (string, error) {
	if len(s.roots) == 0 {
		return "", errors.New("no staging root")
	}
	root := s.roots[0]
	s.roots = s.roots[1:]
	return root, os.MkdirAll(root, 0o700)
}

func (s *fixedStaging) Discard(_ context.Context, root string) error {
	s.discarded = append(s.discarded, root)
	return os.RemoveAll(root)
}

func createMirrorFixture(t *testing.T, root string) {
	t.Helper()
	for _, path := range []string{root, filepath.Join(root, ".camp", "build"), filepath.Join(root, ".camp", "runtime"), filepath.Join(root, "nested dir")} {
		if err := os.MkdirAll(path, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "nested dir", "Unicode ü file.txt"), []byte("preserved bytes\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	large := make([]byte, 2<<20)
	for index := range large {
		large[index] = byte(index % 251)
	}
	if err := os.WriteFile(filepath.Join(root, "large.bin"), large, 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "large.bin"), filepath.Join(root, "large-hardlink.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested dir/Unicode ü file.txt", filepath.Join(root, "unicode-link")); err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []string{filepath.Join(root, ".camp", "build", "secret"), filepath.Join(root, ".camp", "runtime", "secret")} {
		if err := os.WriteFile(excluded, []byte("excluded"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func assertMirrorFixture(t *testing.T, source, destination string) {
	t.Helper()
	for _, relative := range []string{"nested dir/Unicode ü file.txt", "large.bin", "large-hardlink.bin"} {
		want, err := os.ReadFile(filepath.Join(source, relative))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(destination, relative))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s bytes mismatch: error=%v size=%d want=%d", relative, err, len(got), len(want))
		}
		wantInfo, _ := os.Stat(filepath.Join(source, relative))
		gotInfo, _ := os.Stat(filepath.Join(destination, relative))
		if gotInfo.Mode().Perm() != wantInfo.Mode().Perm() {
			t.Fatalf("%s mode = %o, want %o", relative, gotInfo.Mode().Perm(), wantInfo.Mode().Perm())
		}
	}
	link, err := os.Readlink(filepath.Join(destination, "unicode-link"))
	if err != nil || link != "nested dir/Unicode ü file.txt" {
		t.Fatalf("symlink = %q, %v", link, err)
	}
	one, _ := os.Stat(filepath.Join(destination, "large.bin"))
	two, _ := os.Stat(filepath.Join(destination, "large-hardlink.bin"))
	if !os.SameFile(one, two) {
		t.Fatal("hard links do not share an inode")
	}
	for _, relative := range []string{".camp/build", ".camp/runtime"} {
		if _, err := os.Lstat(filepath.Join(destination, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("excluded %s error = %v, want not exist", relative, err)
		}
	}
}

func writeLocalSSH(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "ssh")
	contents := []byte("#!/bin/sh\nshift\nif [ \"$#\" -eq 1 ]; then exec /bin/sh -c \"$1\"; fi\nexec \"$@\"\n")
	if err := os.WriteFile(path, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

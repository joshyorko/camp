package sshtransfer

import (
	"reflect"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestTarPipeFallbackIsStructured(t *testing.T) {
	t.Parallel()

	got, err := BuildTarPipe(TarPipeSpec{
		SSHExecutable: "/usr/bin/ssh",
		TarExecutable: "/usr/bin/tar",
		WorkspaceID:   "camp-second-brain",
		RemoteRoot:    "/workspaces/Second Brain",
		LocalRoot:     "/var/lib/camp/staging/Second Brain",
	})
	if err != nil {
		t.Fatalf("BuildTarPipe() error = %v", err)
	}

	want := TarPipe{
		Producer: ports.Command{
			Executable: "/usr/bin/ssh",
			Argv: []string{
				"camp-second-brain.devpod",
				"tar --create --file=- --directory='/workspaces/Second Brain' --exclude='./.camp/build' --exclude='./.camp/runtime' .",
			},
		},
		Consumer: ports.Command{
			Executable: "/usr/bin/tar",
			Argv: []string{
				"--extract", "--file=-", "--directory=/var/lib/camp/staging/Second Brain",
				"--same-permissions", "--delay-directory-restore",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pipeline = %#v, want %#v", got, want)
	}
	for _, command := range []ports.Command{got.Producer, got.Consumer} {
		if command.Executable == "sh" || command.Executable == "bash" {
			t.Fatalf("pipeline invokes a shell: %#v", command)
		}
		for _, arg := range command.Argv {
			if strings.Contains(arg, "|") {
				t.Fatalf("pipeline composes a shell pipe in argv: %#v", command)
			}
		}
	}
}

func TestTarPipeQuotesNonstandardRemoteRootForOpenSSHRemoteShell(t *testing.T) {
	t.Parallel()
	got, err := BuildTarPipe(TarPipeSpec{
		SSHExecutable: "ssh", TarExecutable: "tar", WorkspaceID: "camp",
		RemoteRoot: "/workspaces/it's remote; touch /tmp/pwn", LocalRoot: "/tmp/stage",
	})
	if err != nil {
		t.Fatalf("BuildTarPipe() error = %v", err)
	}
	want := "tar --create --file=- --directory='/workspaces/it'\"'\"'s remote; touch /tmp/pwn' --exclude='./.camp/build' --exclude='./.camp/runtime' ."
	if len(got.Producer.Argv) != 2 || got.Producer.Argv[1] != want {
		t.Fatalf("remote argv = %#v, want safely quoted command %q", got.Producer.Argv, want)
	}
}

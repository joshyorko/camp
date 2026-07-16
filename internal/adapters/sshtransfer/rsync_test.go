package sshtransfer

import (
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestRsyncMirrorPreservesDeletesModesLinksAndExactExclusions(t *testing.T) {
	t.Parallel()

	got, err := BuildRsyncMirror(RsyncMirrorSpec{
		Executable:  "/opt/camp/bin/rsync",
		WorkspaceID: "camp-second-brain",
		RemoteRoot:  "/workspaces/Second Brain",
		LocalRoot:   "/var/lib/camp/materialized/Second Brain",
	})
	if err != nil {
		t.Fatalf("BuildRsyncMirror() error = %v", err)
	}

	want := ports.Command{
		Executable: "/opt/camp/bin/rsync",
		Argv: []string{
			"--archive",
			"--hard-links",
			"--delete",
			"--protect-args",
			"--exclude=/.camp/build/***",
			"--exclude=/.camp/runtime/***",
			"--",
			"camp-second-brain.devpod:/workspaces/Second Brain/",
			"/var/lib/camp/materialized/Second Brain/",
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %#v, want %#v", got, want)
	}
}

package sshtransfer

import (
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestReverseForwardUsesGeneratedDevPodHost(t *testing.T) {
	t.Parallel()

	got, err := BuildReverseForward(ReverseForwardSpec{
		SSHExecutable: "/usr/bin/ssh",
		WorkspaceID:   "camp-second-brain",
		Remote:        Endpoint{Address: "127.0.0.1", Port: 15000},
		Local:         Endpoint{Address: "127.0.0.1", Port: 5000},
	})
	if err != nil {
		t.Fatalf("BuildReverseForward() error = %v", err)
	}

	want := ReverseForward{
		Host:   "camp-second-brain.devpod",
		Remote: Endpoint{Address: "127.0.0.1", Port: 15000},
		Local:  Endpoint{Address: "127.0.0.1", Port: 5000},
		Command: ports.Command{
			Executable: "/usr/bin/ssh",
			Argv: []string{
				"-N", "-T", "-o", "ExitOnForwardFailure=yes",
				"-R", "127.0.0.1:15000:127.0.0.1:5000",
				"camp-second-brain.devpod",
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("forward = %#v, want %#v", got, want)
	}
}

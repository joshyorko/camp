package workspace

import (
	"context"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type fakeExecutor struct{ commands []ports.WorkspaceCommand }

func (e *fakeExecutor) Execute(_ context.Context, command ports.WorkspaceCommand) (ports.Result, error) {
	e.commands = append(e.commands, command)
	return ports.Result{}, nil
}

func TestReachabilityProbesBothCommittedEndpointsFromWorkspace(t *testing.T) {
	t.Parallel()
	executor := &fakeExecutor{}
	probe := NewReachability(executor, "/usr/bin/curl")
	err := probe.Probe(context.Background(), ports.ReachabilityRequest{
		WorkspaceID: "camp-abcd", Context: "default",
		Endpoints: []ports.WorkspaceEndpoint{{Name: "registry", Address: "127.0.0.1:5000", Path: "/v2/"}, {Name: "fileserver", Address: "127.0.0.1:8080", Path: "/"}},
	})
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	want := []ports.WorkspaceCommand{
		{WorkspaceID: "camp-abcd", Context: "default", Argv: []string{"/usr/bin/curl", "--fail", "--silent", "--show-error", "http://127.0.0.1:5000/v2/"}},
		{WorkspaceID: "camp-abcd", Context: "default", Argv: []string{"/usr/bin/curl", "--fail", "--silent", "--show-error", "http://127.0.0.1:8080/"}},
	}
	if !reflect.DeepEqual(executor.commands, want) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, want)
	}
}

package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestSelectorRoutesPersistedWorkspaceTypeToMatchingTransport(t *testing.T) {
	t.Parallel()

	local := &recordingTransport{result: ports.MirrorResult{Mode: ports.MirrorLocalNoop, Root: "/local"}}
	remote := &recordingTransport{result: ports.MirrorResult{Mode: MirrorDevPodSSH, Root: "/remote"}}
	selector := NewSelector(local, remote)

	localRequest := ports.MirrorRequest{Provider: "docker", LocalProvider: true, StagingRoot: "/local", WorkspaceLocalFolder: "/local"}
	if result, err := selector.ReturnToStaging(context.Background(), localRequest); err != nil || result.Root != "/local" {
		t.Fatalf("local ReturnToStaging() = %#v, %v", result, err)
	}
	remoteRequest := ports.MirrorRequest{Provider: "ssh", LocalProvider: false, StagingRoot: "/controller"}
	if result, err := selector.ReturnToStaging(context.Background(), remoteRequest); err != nil || result.Root != "/remote" {
		t.Fatalf("remote ReturnToStaging() = %#v, %v", result, err)
	}

	if !reflect.DeepEqual(local.requests, []ports.MirrorRequest{localRequest}) {
		t.Fatalf("local requests = %#v", local.requests)
	}
	if !reflect.DeepEqual(remote.requests, []ports.MirrorRequest{remoteRequest}) {
		t.Fatalf("remote requests = %#v", remote.requests)
	}
}

func TestSelectorFailsClosedWhenMatchingTransportIsUnavailable(t *testing.T) {
	t.Parallel()

	if _, err := NewSelector(nil, &recordingTransport{}).ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "docker", LocalProvider: true}); !errors.Is(err, ErrTransportUnavailable) {
		t.Fatalf("local error = %v, want ErrTransportUnavailable", err)
	}
	if _, err := NewSelector(&recordingTransport{}, nil).ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "ssh"}); !errors.Is(err, ErrTransportUnavailable) {
		t.Fatalf("remote error = %v, want ErrTransportUnavailable", err)
	}
}

type recordingTransport struct {
	result   ports.MirrorResult
	err      error
	requests []ports.MirrorRequest
}

func (t *recordingTransport) ReturnToStaging(_ context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	t.requests = append(t.requests, request)
	return t.result, t.err
}

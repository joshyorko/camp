package workspace

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/adapters/sshtransfer"
	"github.com/joshyorko/camp/internal/ports"
)

func TestRemoteMirrorQueriesEffectiveRootAndUsesFreshRsyncStaging(t *testing.T) {
	t.Parallel()

	resolver := &fakeRemoteRootResolver{root: "/workspaces/custom root"}
	staging := &fakeRemoteStaging{fresh: []string{"/var/lib/camp/staging/attempt-1"}}
	rsync := &fakeRsyncExecutor{attempt: sshtransfer.TransferAttempt{
		Method: sshtransfer.MethodRsync, ProducerStarted: true, Producer: ports.Result{ExitCode: 0},
	}}
	tar := &fakeTarPipelineExecutor{}
	transport := NewRemote(RemoteConfig{
		WorkspaceID: "camp-second-brain", Context: "default",
		RsyncExecutable: "/opt/camp/bin/rsync", SSHExecutable: "/usr/bin/ssh", TarExecutable: "/usr/bin/tar",
	}, resolver, staging, rsync, tar)

	result, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{
		Provider: "ssh", StagingRoot: "/var/lib/camp/materialized/Second Brain", AttemptID: "mirror-1",
		WorkspaceID: "camp-second-brain", Context: "default",
	})
	if err != nil {
		t.Fatalf("ReturnToStaging() error = %v", err)
	}
	if result.Mode != MirrorDevPodSSH || result.Root != "/var/lib/camp/staging/attempt-1" {
		t.Fatalf("result = %#v", result)
	}
	if resolver.context != "default" || resolver.workspaceID != "camp-second-brain" {
		t.Fatalf("root query = context %q workspace %q", resolver.context, resolver.workspaceID)
	}
	wantCommand := ports.Command{
		Executable: "/opt/camp/bin/rsync",
		Argv: []string{
			"--archive", "--hard-links", "--delete", "--protect-args",
			"--exclude=/.camp/build/***", "--exclude=/.camp/runtime/***", "--",
			"camp-second-brain.devpod:/workspaces/custom root/",
			"/var/lib/camp/staging/attempt-1/",
		},
	}
	if !reflect.DeepEqual(rsync.command, wantCommand) {
		t.Fatalf("rsync command = %#v, want %#v", rsync.command, wantCommand)
	}
	if tar.calls != 0 || len(staging.discarded) != 0 {
		t.Fatalf("unexpected fallback/discard: tar=%d discarded=%v", tar.calls, staging.discarded)
	}
}

func TestRemoteMirrorFallsBackOnlyForUnavailableBeforeStartAndUsesSecondFreshDestination(t *testing.T) {
	t.Parallel()

	resolver := &fakeRemoteRootResolver{root: "/workspace/effective"}
	staging := &fakeRemoteStaging{fresh: []string{"/stage/rsync", "/stage/tar"}}
	rsyncCause := errors.New("rsync executable unavailable")
	rsync := &fakeRsyncExecutor{attempt: sshtransfer.TransferAttempt{
		Method: sshtransfer.MethodRsync, Unavailable: true, Err: rsyncCause,
	}}
	tar := &fakeTarPipelineExecutor{attempt: sshtransfer.TransferAttempt{
		Method: sshtransfer.MethodTarPipe, ProducerStarted: true, ConsumerStarted: true,
		Producer: ports.Result{ExitCode: 0}, Consumer: ports.Result{ExitCode: 0},
	}}
	transport := NewRemote(RemoteConfig{
		WorkspaceID: "camp-brain", RsyncExecutable: "rsync", SSHExecutable: "ssh", TarExecutable: "tar",
	}, resolver, staging, rsync, tar)

	result, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{
		Provider: "ssh", StagingRoot: "/materialized/brain", AttemptID: "mirror-2", WorkspaceID: "camp-brain",
	})
	if err != nil {
		t.Fatalf("ReturnToStaging() error = %v", err)
	}
	if result.Mode != MirrorDevPodSSH || result.Root != "/stage/tar" {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(staging.freshFor, []string{"/materialized/brain", "/materialized/brain"}) {
		t.Fatalf("fresh staging requests = %v", staging.freshFor)
	}
	if !reflect.DeepEqual(staging.discarded, []string{"/stage/rsync"}) {
		t.Fatalf("discarded = %v", staging.discarded)
	}
	wantPipeline := sshtransfer.TarPipe{
		Producer: ports.Command{Executable: "ssh", Argv: []string{
			"camp-brain.devpod",
			"tar --create --file=- --directory='/workspace/effective' --exclude='./.camp/build' --exclude='./.camp/runtime' .",
		}},
		Consumer: ports.Command{Executable: "tar", Argv: []string{
			"--extract", "--file=-", "--directory=/stage/tar", "--same-permissions", "--delay-directory-restore",
		}},
	}
	if tar.calls != 1 || !reflect.DeepEqual(tar.pipeline, wantPipeline) {
		t.Fatalf("tar pipeline = %#v, calls=%d, want %#v", tar.pipeline, tar.calls, wantPipeline)
	}
}

func TestRemoteMirrorRetainsPartialRsyncDestinationForObservation(t *testing.T) {
	t.Parallel()

	staging := &fakeRemoteStaging{fresh: []string{"/stage/attempt-1"}}
	transport := NewRemote(RemoteConfig{WorkspaceID: "camp-brain", RsyncExecutable: "rsync"},
		&fakeRemoteRootResolver{root: "/workspace/effective"}, staging,
		&fakeRsyncExecutor{attempt: sshtransfer.TransferAttempt{Method: sshtransfer.MethodRsync, ProducerStarted: true, Err: errors.New("connection lost")}}, nil)

	_, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "ssh", StagingRoot: "/controller", AttemptID: "mirror-intent-1", WorkspaceID: "camp-brain"})
	var unknown *MirrorOutcomeUnknown
	if !errors.As(err, &unknown) {
		t.Fatalf("ReturnToStaging() error = %v, want MirrorOutcomeUnknown", err)
	}
	if unknown.Result.AttemptID != "mirror-intent-1-rsync" || unknown.Result.Root != "/stage/attempt-1" || unknown.Result.Method != string(sshtransfer.MethodRsync) {
		t.Fatalf("unknown result = %#v", unknown.Result)
	}
	if len(staging.discarded) != 0 {
		t.Fatalf("partial destination discarded = %#v", staging.discarded)
	}
}

func TestRemoteMirrorRetainsPartialTarDestinationAfterSafeRsyncDiscard(t *testing.T) {
	t.Parallel()

	staging := &fakeRemoteStaging{fresh: []string{"/stage/rsync", "/stage/tar"}}
	transport := NewRemote(RemoteConfig{WorkspaceID: "camp-brain", RsyncExecutable: "rsync", SSHExecutable: "ssh", TarExecutable: "tar"},
		&fakeRemoteRootResolver{root: "/workspace/effective"}, staging,
		&fakeRsyncExecutor{attempt: sshtransfer.TransferAttempt{Method: sshtransfer.MethodRsync, Unavailable: true, Err: errors.New("missing")}},
		&fakeTarPipelineExecutor{attempt: sshtransfer.TransferAttempt{Method: sshtransfer.MethodTarPipe, ProducerStarted: true, ConsumerStarted: true, Err: errors.New("connection lost")}})

	_, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{Provider: "ssh", StagingRoot: "/controller", AttemptID: "mirror-intent-2", WorkspaceID: "camp-brain"})
	var unknown *MirrorOutcomeUnknown
	if !errors.As(err, &unknown) || unknown.Result.Root != "/stage/tar" || unknown.Result.Method != string(sshtransfer.MethodTarPipe) {
		t.Fatalf("ReturnToStaging() error = %#v, unknown=%#v", err, unknown)
	}
	if !reflect.DeepEqual(staging.discarded, []string{"/stage/rsync"}) {
		t.Fatalf("discarded = %#v", staging.discarded)
	}
}

func TestRemoteMirrorFailsBeforeResolutionWhenPersistedIdentityDiffersFromComposition(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		workspaceID string
		context     string
	}{
		{name: "workspace ID", workspaceID: "other-workspace", context: "default"},
		{name: "context", workspaceID: "camp-brain", context: "other-context"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			resolver := &fakeRemoteRootResolver{root: "/workspace/effective"}
			staging := &fakeRemoteStaging{fresh: []string{"/stage/attempt"}}
			rsync := &fakeRsyncExecutor{}
			transport := NewRemote(RemoteConfig{WorkspaceID: "camp-brain", Context: "default", RsyncExecutable: "rsync"}, resolver, staging, rsync, nil)

			_, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{
				Provider: "ssh", StagingRoot: "/controller", AttemptID: "mirror-identity", WorkspaceID: test.workspaceID, Context: test.context,
			})
			if !errors.Is(err, ErrRemoteIdentityMismatch) {
				t.Fatalf("ReturnToStaging() error = %v, want ErrRemoteIdentityMismatch", err)
			}
			if resolver.workspaceID != "" || len(staging.freshFor) != 0 || rsync.calls != 0 {
				t.Fatalf("effects after identity mismatch: resolver=%q staging=%v rsync=%d", resolver.workspaceID, staging.freshFor, rsync.calls)
			}
		})
	}
}

func TestMirrorOutcomeUnknownIsNilSafe(t *testing.T) {
	t.Parallel()

	var unknown *MirrorOutcomeUnknown
	var err error = unknown
	if err.Error() == "" {
		t.Fatal("typed-nil Error() is empty")
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("typed-nil Unwrap() = %v", errors.Unwrap(err))
	}
	withoutCause := &MirrorOutcomeUnknown{}
	if withoutCause.Error() == "" || errors.Unwrap(withoutCause) != nil {
		t.Fatalf("nil-cause error = %q unwrap=%v", withoutCause.Error(), errors.Unwrap(withoutCause))
	}
}

func TestRemoteMirrorReportsUnavailableWhenTarFallbackIsNotComposed(t *testing.T) {
	t.Parallel()

	staging := &fakeRemoteStaging{fresh: []string{"/stage/rsync"}}
	transport := NewRemote(RemoteConfig{WorkspaceID: "camp-brain", Context: "default", RsyncExecutable: "rsync"},
		&fakeRemoteRootResolver{root: "/workspace/effective"}, staging,
		&fakeRsyncExecutor{attempt: sshtransfer.TransferAttempt{Method: sshtransfer.MethodRsync, Unavailable: true, Err: errors.New("missing")}}, nil)

	_, err := transport.ReturnToStaging(context.Background(), ports.MirrorRequest{
		Provider: "ssh", StagingRoot: "/controller", AttemptID: "mirror-no-tar", WorkspaceID: "camp-brain", Context: "default",
	})
	if !errors.Is(err, ErrTransportUnavailable) || errors.Is(err, ErrNotRemoteMirror) {
		t.Fatalf("ReturnToStaging() error = %v, want only ErrTransportUnavailable", err)
	}
}

type fakeRemoteRootResolver struct {
	root        string
	err         error
	context     string
	workspaceID string
}

func (f *fakeRemoteRootResolver) ResolveWorkspaceFolderInContext(_ context.Context, devpodContext, workspaceID string) (string, error) {
	f.context = devpodContext
	f.workspaceID = workspaceID
	return f.root, f.err
}

type fakeRemoteStaging struct {
	fresh      []string
	freshFor   []string
	discarded  []string
	discardErr error
}

func (f *fakeRemoteStaging) Fresh(_ context.Context, requestedRoot, _ string) (string, error) {
	f.freshFor = append(f.freshFor, requestedRoot)
	if len(f.fresh) == 0 {
		return "", errors.New("no fresh staging fixture")
	}
	root := f.fresh[0]
	f.fresh = f.fresh[1:]
	return root, nil
}

func (f *fakeRemoteStaging) Discard(_ context.Context, root string) error {
	f.discarded = append(f.discarded, root)
	return f.discardErr
}

type fakeRsyncExecutor struct {
	attempt sshtransfer.TransferAttempt
	command ports.Command
	calls   int
}

func (f *fakeRsyncExecutor) RunRsync(_ context.Context, command ports.Command) sshtransfer.TransferAttempt {
	f.calls++
	f.command = command
	return f.attempt
}

type fakeTarPipelineExecutor struct {
	attempt  sshtransfer.TransferAttempt
	pipeline sshtransfer.TarPipe
	calls    int
}

func (f *fakeTarPipelineExecutor) RunTarPipeline(_ context.Context, pipeline sshtransfer.TarPipe) sshtransfer.TransferAttempt {
	f.calls++
	f.pipeline = pipeline
	return f.attempt
}

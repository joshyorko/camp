package workspace

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/adapters/sshtransfer"
	"github.com/joshyorko/camp/internal/ports"
)

const MirrorDevPodSSH ports.MirrorMode = "devPodSSHMirror"

var ErrNotRemoteMirror = errors.New("workspace is not a remote DevPod mirror")
var ErrRemoteIdentityMismatch = errors.New("persisted remote workspace identity does not match transport composition")

type RemoteConfig struct {
	WorkspaceID     string
	Context         string
	RsyncExecutable string
	SSHExecutable   string
	TarExecutable   string
}

type RemoteRootResolver interface {
	ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error)
}

type RemoteStaging interface {
	Fresh(context.Context, string, string) (string, error)
	Discard(context.Context, string) error
}

type MirrorOutcomeUnknown struct {
	Result ports.MirrorResult
	Err    error
}

func (e *MirrorOutcomeUnknown) Error() string {
	if e == nil || e.Err == nil {
		return "workspace mirror outcome is unknown"
	}
	return "workspace mirror outcome is unknown: " + e.Err.Error()
}
func (e *MirrorOutcomeUnknown) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type RsyncExecutor interface {
	RunRsync(context.Context, ports.Command) sshtransfer.TransferAttempt
}

type TarPipelineExecutor interface {
	RunTarPipeline(context.Context, sshtransfer.TarPipe) sshtransfer.TransferAttempt
}

type Remote struct {
	config   RemoteConfig
	resolver RemoteRootResolver
	staging  RemoteStaging
	rsync    RsyncExecutor
	tar      TarPipelineExecutor
}

func NewRemote(config RemoteConfig, resolver RemoteRootResolver, staging RemoteStaging, rsync RsyncExecutor, tar TarPipelineExecutor) *Remote {
	return &Remote{config: config, resolver: resolver, staging: staging, rsync: rsync, tar: tar}
}

func (r *Remote) ReturnToStaging(ctx context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.MirrorResult{}, err
	}
	if err := r.Validate(request); err != nil {
		return ports.MirrorResult{}, err
	}
	remoteRoot, err := r.resolver.ResolveWorkspaceFolderInContext(ctx, request.Context, request.WorkspaceID)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	rsyncAttemptID := request.AttemptID + "-rsync"
	destination, err := r.staging.Fresh(ctx, request.StagingRoot, rsyncAttemptID)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	command, err := sshtransfer.BuildRsyncMirror(sshtransfer.RsyncMirrorSpec{
		Executable: r.config.RsyncExecutable, WorkspaceID: request.WorkspaceID,
		RemoteRoot: remoteRoot, LocalRoot: destination,
	})
	if err != nil {
		return ports.MirrorResult{}, errors.Join(err, r.staging.Discard(context.WithoutCancel(ctx), destination))
	}
	if _, err := sshtransfer.ClassifyTransfer(r.rsync.RunRsync(ctx, command)); err != nil {
		result := remoteMirrorResult(destination, rsyncAttemptID, sshtransfer.MethodRsync, remoteRoot)
		if !sshtransfer.TarFallbackAllowed(err) {
			var failure *sshtransfer.TransferFailure
			if errors.As(err, &failure) && failure.Kind == sshtransfer.FailurePartial {
				return ports.MirrorResult{}, &MirrorOutcomeUnknown{Result: result, Err: err}
			}
			return ports.MirrorResult{}, errors.Join(err, r.staging.Discard(context.WithoutCancel(ctx), destination))
		}
		discardErr := r.staging.Discard(context.WithoutCancel(ctx), destination)
		if discardErr != nil {
			return ports.MirrorResult{}, errors.Join(err, discardErr)
		}
		if r.tar == nil {
			return ports.MirrorResult{}, errors.Join(err, ErrTransportUnavailable)
		}
		tarAttemptID := request.AttemptID + "-tar"
		destination, freshErr := r.staging.Fresh(ctx, request.StagingRoot, tarAttemptID)
		if freshErr != nil {
			return ports.MirrorResult{}, errors.Join(err, freshErr)
		}
		pipeline, buildErr := sshtransfer.BuildTarPipe(sshtransfer.TarPipeSpec{
			SSHExecutable: r.config.SSHExecutable, TarExecutable: r.config.TarExecutable,
			WorkspaceID: request.WorkspaceID, RemoteRoot: remoteRoot, LocalRoot: destination,
		})
		if buildErr != nil {
			return ports.MirrorResult{}, errors.Join(buildErr, r.staging.Discard(context.WithoutCancel(ctx), destination))
		}
		if _, pipelineErr := sshtransfer.ClassifyTransfer(r.tar.RunTarPipeline(ctx, pipeline)); pipelineErr != nil {
			var failure *sshtransfer.TransferFailure
			if errors.As(pipelineErr, &failure) && failure.Kind == sshtransfer.FailurePartial {
				return ports.MirrorResult{}, &MirrorOutcomeUnknown{Result: remoteMirrorResult(destination, tarAttemptID, sshtransfer.MethodTarPipe, remoteRoot), Err: pipelineErr}
			}
			return ports.MirrorResult{}, errors.Join(pipelineErr, r.staging.Discard(context.WithoutCancel(ctx), destination))
		}
		return remoteMirrorResult(destination, tarAttemptID, sshtransfer.MethodTarPipe, remoteRoot), nil
	}
	return remoteMirrorResult(destination, rsyncAttemptID, sshtransfer.MethodRsync, remoteRoot), nil
}

func (r *Remote) Validate(request ports.MirrorRequest) error {
	if request.LocalProvider || request.Provider == "" || request.StagingRoot == "" || request.AttemptID == "" || request.WorkspaceID == "" || r == nil {
		return ErrNotRemoteMirror
	}
	if request.WorkspaceID != r.config.WorkspaceID || request.Context != r.config.Context {
		return ErrRemoteIdentityMismatch
	}
	if r.resolver == nil || r.staging == nil || r.rsync == nil {
		return ErrNotRemoteMirror
	}
	return nil
}

func remoteMirrorResult(root, attemptID string, method sshtransfer.Method, remoteRoot string) ports.MirrorResult {
	return ports.MirrorResult{
		Mode: MirrorDevPodSSH, Root: root, AttemptID: attemptID, Method: string(method), RemoteRoot: remoteRoot,
		Exclusions: []string{"/.camp/build/***", "/.camp/runtime/***"},
	}
}

var _ ports.WorkspaceTransport = (*Remote)(nil)

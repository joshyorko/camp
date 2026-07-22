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
	WorkspaceID      string
	Context          string
	RsyncExecutable  string
	SSHExecutable    string
	DevPodExecutable string
	TarExecutable    string
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

type RequestBoundRemote struct {
	config   RemoteConfig
	resolver RemoteRootResolver
	staging  RemoteStaging
	rsync    RsyncExecutor
	tar      TarPipelineExecutor
}

func NewRequestBoundRemote(config RemoteConfig, resolver RemoteRootResolver, staging RemoteStaging, rsync RsyncExecutor, tar TarPipelineExecutor) *RequestBoundRemote {
	return &RequestBoundRemote{config: config, resolver: resolver, staging: staging, rsync: rsync, tar: tar}
}

func (r *RequestBoundRemote) bound(request ports.MirrorRequest) *Remote {
	config := r.config
	config.WorkspaceID = request.WorkspaceID
	config.Context = request.Context
	return NewRemote(config, r.resolver, r.staging, r.rsync, r.tar)
}

func (r *RequestBoundRemote) Validate(request ports.MirrorRequest) error {
	if r == nil {
		return ErrNotRemoteMirror
	}
	return r.bound(request).Validate(request)
}

func (r *RequestBoundRemote) ReturnToStaging(ctx context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	if r == nil {
		return ports.MirrorResult{}, ErrNotRemoteMirror
	}
	return r.bound(request).ReturnToStaging(ctx, request)
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
	if r.config.RsyncExecutable == "" {
		return r.returnWithTar(ctx, request, remoteRoot, request.AttemptID+"-tar")
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
		result, tarErr := r.returnWithTar(ctx, request, remoteRoot, request.AttemptID+"-tar")
		if tarErr != nil {
			return ports.MirrorResult{}, errors.Join(err, tarErr)
		}
		return result, nil
	}
	return remoteMirrorResult(destination, rsyncAttemptID, sshtransfer.MethodRsync, remoteRoot), nil
}

func (r *Remote) returnWithTar(ctx context.Context, request ports.MirrorRequest, remoteRoot, attemptID string) (ports.MirrorResult, error) {
	destination, err := r.staging.Fresh(ctx, request.StagingRoot, attemptID)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	pipeline, err := sshtransfer.BuildTarPipe(sshtransfer.TarPipeSpec{
		SSHExecutable: r.config.SSHExecutable, DevPodExecutable: r.config.DevPodExecutable, DevPodContext: request.Context,
		TarExecutable: r.config.TarExecutable, WorkspaceID: request.WorkspaceID, RemoteRoot: remoteRoot, LocalRoot: destination,
	})
	if err != nil {
		return ports.MirrorResult{}, errors.Join(err, r.staging.Discard(context.WithoutCancel(ctx), destination))
	}
	if _, err := sshtransfer.ClassifyTransfer(r.tar.RunTarPipeline(ctx, pipeline)); err != nil {
		var failure *sshtransfer.TransferFailure
		if errors.As(err, &failure) && failure.Kind == sshtransfer.FailurePartial {
			return ports.MirrorResult{}, &MirrorOutcomeUnknown{Result: remoteMirrorResult(destination, attemptID, sshtransfer.MethodTarPipe, remoteRoot), Err: err}
		}
		return ports.MirrorResult{}, errors.Join(err, r.staging.Discard(context.WithoutCancel(ctx), destination))
	}
	return remoteMirrorResult(destination, attemptID, sshtransfer.MethodTarPipe, remoteRoot), nil
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
var _ ports.WorkspaceTransport = (*RequestBoundRemote)(nil)

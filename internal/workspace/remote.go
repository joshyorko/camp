package workspace

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/adapters/sshtransfer"
	"github.com/joshyorko/camp/internal/ports"
)

const MirrorDevPodSSH ports.MirrorMode = "devPodSSHMirror"

var ErrNotRemoteMirror = errors.New("workspace is not a remote DevPod mirror")

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
	Fresh(context.Context, string) (string, error)
	Discard(context.Context, string) error
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
	if request.LocalProvider || request.Provider == "" || request.StagingRoot == "" || r == nil || r.resolver == nil || r.staging == nil || r.rsync == nil {
		return ports.MirrorResult{}, ErrNotRemoteMirror
	}
	remoteRoot, err := r.resolver.ResolveWorkspaceFolderInContext(ctx, r.config.Context, r.config.WorkspaceID)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	destination, err := r.staging.Fresh(ctx, request.StagingRoot)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	command, err := sshtransfer.BuildRsyncMirror(sshtransfer.RsyncMirrorSpec{
		Executable: r.config.RsyncExecutable, WorkspaceID: r.config.WorkspaceID,
		RemoteRoot: remoteRoot, LocalRoot: destination,
	})
	if err != nil {
		return ports.MirrorResult{}, errors.Join(err, r.staging.Discard(context.WithoutCancel(ctx), destination))
	}
	if _, err := sshtransfer.ClassifyTransfer(r.rsync.RunRsync(ctx, command)); err != nil {
		discardErr := r.staging.Discard(context.WithoutCancel(ctx), destination)
		if !sshtransfer.TarFallbackAllowed(err) || discardErr != nil {
			return ports.MirrorResult{}, errors.Join(err, discardErr)
		}
		if r.tar == nil {
			return ports.MirrorResult{}, errors.Join(err, ErrNotRemoteMirror)
		}
		destination, freshErr := r.staging.Fresh(ctx, request.StagingRoot)
		if freshErr != nil {
			return ports.MirrorResult{}, errors.Join(err, freshErr)
		}
		pipeline, buildErr := sshtransfer.BuildTarPipe(sshtransfer.TarPipeSpec{
			SSHExecutable: r.config.SSHExecutable, TarExecutable: r.config.TarExecutable,
			WorkspaceID: r.config.WorkspaceID, RemoteRoot: remoteRoot, LocalRoot: destination,
		})
		if buildErr != nil {
			return ports.MirrorResult{}, errors.Join(buildErr, r.staging.Discard(context.WithoutCancel(ctx), destination))
		}
		if _, pipelineErr := sshtransfer.ClassifyTransfer(r.tar.RunTarPipeline(ctx, pipeline)); pipelineErr != nil {
			return ports.MirrorResult{}, errors.Join(pipelineErr, r.staging.Discard(context.WithoutCancel(ctx), destination))
		}
		return ports.MirrorResult{Mode: MirrorDevPodSSH, Root: destination}, nil
	}
	return ports.MirrorResult{Mode: MirrorDevPodSSH, Root: destination}, nil
}

var _ ports.WorkspaceTransport = (*Remote)(nil)

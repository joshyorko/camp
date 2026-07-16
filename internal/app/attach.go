package app

import (
	"context"
	"errors"

	devpodadapter "github.com/joshyorko/camp/internal/adapters/devpod"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
	"github.com/joshyorko/camp/internal/target"
	"github.com/joshyorko/camp/internal/workspace"
)

var (
	ErrAttachDependencies   = errors.New("attach dependencies are incomplete")
	ErrAttachSessionNotOpen = errors.New("attach requires an open session")
	ErrAttachT3Unavailable  = errors.New("t3-code attach is not implemented")
)

type AttachOwnership interface {
	Revalidate(domain.Materialization) error
}

type AttachTargetResolver interface {
	Resolve(context.Context, string, string) (target.Result, error)
}

type AttachDevPod interface {
	ResolveWorkspaceFolderInContext(context.Context, string, string) (string, error)
	SSH(context.Context, devpodadapter.SSHOptions) (ports.Result, error)
	OpenNestedIDE(context.Context, devpodadapter.IDEOpenOptions) (ports.Result, error)
}

type AttachDependencies struct {
	Sessions  sessionLister
	Ownership AttachOwnership
	Target    AttachTargetResolver
	DevPod    AttachDevPod
}

type AttachRequest struct {
	Selector SessionSelector
	Target   string
	Entry    devpodadapter.IDEEntry
	SSH      devpodadapter.SSHOptions
}

type AttachResult struct {
	Session      domain.JournalSnapshot
	Target       target.Result
	MappedTarget string
	EntryResult  ports.Result
}

type Attach struct {
	deps AttachDependencies
}

func NewAttach(deps AttachDependencies) *Attach {
	return &Attach{deps: deps}
}

func (a *Attach) Run(ctx context.Context, request AttachRequest) (AttachResult, error) {
	if a == nil || a.deps.Sessions == nil || a.deps.Ownership == nil || a.deps.Target == nil || a.deps.DevPod == nil {
		return AttachResult{}, ErrAttachDependencies
	}
	snapshot, err := SelectActiveSession(ctx, a.deps.Sessions, request.Selector)
	if err != nil {
		return AttachResult{}, err
	}
	if snapshot.State != domain.SessionOpen || snapshot.Materialization.CanonicalPath == "" || snapshot.Workspace.ID == "" || snapshot.Workspace.Context == "" || snapshot.Workspace.StagingRoot == "" {
		return AttachResult{}, ErrAttachSessionNotOpen
	}
	if err := request.Entry.Validate(); err != nil {
		return AttachResult{}, err
	}
	if err := a.deps.Ownership.Revalidate(snapshot.Materialization); err != nil {
		return AttachResult{}, err
	}
	resolved, err := a.deps.Target.Resolve(ctx, snapshot.Materialization.CanonicalPath, request.Target)
	if err != nil {
		return AttachResult{}, err
	}
	effectiveRoot, err := a.deps.DevPod.ResolveWorkspaceFolderInContext(ctx, snapshot.Workspace.Context, snapshot.Workspace.ID)
	if err != nil {
		return AttachResult{}, err
	}
	mapped, err := workspace.MapTarget(snapshot.Workspace.StagingRoot, effectiveRoot, resolved.Relative)
	if err != nil {
		return AttachResult{}, err
	}
	var entryResult ports.Result
	switch request.Entry.IDE {
	case devpodadapter.IDETerminal:
		options := request.SSH
		options.WorkspaceID = snapshot.Workspace.ID
		options.Context = snapshot.Workspace.Context
		options.Workdir = mapped
		options.StartServices = true
		entryResult, err = a.deps.DevPod.SSH(ctx, options)
	case devpodadapter.IDEVSCode, devpodadapter.IDEVSCodeInsiders:
		entryResult, err = a.deps.DevPod.OpenNestedIDE(ctx, devpodadapter.IDEOpenOptions{IDE: request.Entry, WorkspaceID: snapshot.Workspace.ID, ContainerTarget: mapped})
	case devpodadapter.IDET3Code:
		return AttachResult{}, ErrAttachT3Unavailable
	}
	if err != nil {
		return AttachResult{}, err
	}
	return AttachResult{Session: snapshot, Target: resolved, MappedTarget: mapped, EntryResult: entryResult}, nil
}

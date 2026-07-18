package ports

import "context"

type MirrorMode string

const MirrorLocalNoop MirrorMode = "localNoop"

type MirrorRequest struct {
	Provider             string
	LocalProvider        bool
	StagingRoot          string
	WorkspaceLocalFolder string
	WorkspaceID          string
	Context              string
	AttemptID            string
}

type MirrorResult struct {
	Mode       MirrorMode
	Root       string
	AttemptID  string
	Method     string
	RemoteRoot string
	Exclusions []string
}

type WorkspaceTransport interface {
	ReturnToStaging(context.Context, MirrorRequest) (MirrorResult, error)
}

type WorkspaceCommand struct {
	Context     string
	WorkspaceID string
	Workdir     string
	Argv        []string
}

type WorkspaceExecutor interface {
	Execute(context.Context, WorkspaceCommand) (Result, error)
}

type WorkspaceEndpoint struct {
	Name    string
	Address string
	Path    string
}

type ReachabilityRequest struct {
	Context     string
	WorkspaceID string
	Endpoints   []WorkspaceEndpoint
}

type ReachabilityProbe interface {
	Probe(context.Context, ReachabilityRequest) error
}

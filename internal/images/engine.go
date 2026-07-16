package images

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var ErrNoEngine = errors.New("no usable workspace image engine")

type EngineKind string

const (
	EngineDocker EngineKind = "docker"
	EnginePodman EngineKind = "podman"
)

type EngineScope struct {
	Context     string
	WorkspaceID string
}

type Engine struct {
	Kind       EngineKind
	Executable string
	executor   ports.WorkspaceExecutor
	scope      EngineScope
}

type Detector struct {
	executor ports.WorkspaceExecutor
}

func NewDetector(executor ports.WorkspaceExecutor) *Detector {
	return &Detector{executor: executor}
}

func (d *Detector) Detect(ctx context.Context, scope EngineScope) (Engine, error) {
	if d == nil || d.executor == nil || scope.Context == "" || scope.WorkspaceID == "" {
		return Engine{}, fmt.Errorf("engine detection scope is incomplete: %w", ErrNoEngine)
	}
	var failures []error
	for _, candidate := range []struct {
		kind       EngineKind
		executable string
		format     string
	}{{EngineDocker, "docker", "{{.Server.Version}}"}, {EnginePodman, "podman", "json"}} {
		result, err := d.executor.Execute(ctx, ports.WorkspaceCommand{
			Context: scope.Context, WorkspaceID: scope.WorkspaceID,
			Argv: []string{candidate.executable, "version", "--format", candidate.format},
		})
		if err == nil && result.ExitCode == 0 && strings.TrimSpace(string(result.Stdout)) != "" {
			return Engine{Kind: candidate.kind, Executable: candidate.executable, executor: d.executor, scope: scope}, nil
		}
		if err == nil {
			err = fmt.Errorf("%s version exited %d", candidate.executable, result.ExitCode)
		}
		failures = append(failures, err)
	}
	return Engine{}, fmt.Errorf("%w: %v", ErrNoEngine, errors.Join(failures...))
}

func (e Engine) execute(ctx context.Context, argv ...string) (ports.Result, error) {
	if e.executor == nil || e.Executable == "" || e.scope.Context == "" || e.scope.WorkspaceID == "" {
		return ports.Result{}, ErrNoEngine
	}
	return e.executor.Execute(ctx, ports.WorkspaceCommand{Context: e.scope.Context, WorkspaceID: e.scope.WorkspaceID, Argv: argv})
}

func (e Engine) run(ctx context.Context, args ...string) (ports.Result, error) {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, e.Executable)
	argv = append(argv, args...)
	return e.execute(ctx, argv...)
}

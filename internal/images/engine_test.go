package images

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type recordingWorkspaceExecutor struct {
	commands []ports.WorkspaceCommand
	results  []ports.Result
	errors   []error
}

func (e *recordingWorkspaceExecutor) Execute(_ context.Context, command ports.WorkspaceCommand) (ports.Result, error) {
	e.commands = append(e.commands, command)
	index := len(e.commands) - 1
	var result ports.Result
	if index < len(e.results) {
		result = e.results[index]
	}
	if index < len(e.errors) {
		return result, e.errors[index]
	}
	return result, nil
}

func TestDetectorSelectsDockerBeforePodmanInWorkspace(t *testing.T) {
	t.Parallel()
	executor := &recordingWorkspaceExecutor{results: []ports.Result{{Stdout: []byte("26.1.0\n")}}}
	detector := NewDetector(executor)
	engine, err := detector.Detect(context.Background(), EngineScope{Context: "default", WorkspaceID: "brain-main"})
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if engine.Kind != EngineDocker || engine.Executable != "docker" {
		t.Fatalf("engine = %#v", engine)
	}
	want := []ports.WorkspaceCommand{{Context: "default", WorkspaceID: "brain-main", Argv: []string{"docker", "version", "--format", "{{.Server.Version}}"}}}
	if !reflect.DeepEqual(executor.commands, want) {
		t.Fatalf("commands = %#v, want %#v", executor.commands, want)
	}
}

func TestDetectorFallsBackToPodmanAndReturnsTypedAbsence(t *testing.T) {
	t.Parallel()
	t.Run("podman", func(t *testing.T) {
		executor := &recordingWorkspaceExecutor{
			results: []ports.Result{{ExitCode: 1}, {Stdout: []byte(`{"Server":{"Version":"5.4.0"}}`)}},
			errors:  []error{errors.New("docker unavailable"), nil},
		}
		engine, err := NewDetector(executor).Detect(context.Background(), EngineScope{Context: "default", WorkspaceID: "brain-main"})
		if err != nil || engine.Kind != EnginePodman || engine.Executable != "podman" {
			t.Fatalf("engine = %#v, error = %v", engine, err)
		}
		want := ports.WorkspaceCommand{Context: "default", WorkspaceID: "brain-main", Argv: []string{"podman", "version", "--format", "json"}}
		if len(executor.commands) != 2 || !reflect.DeepEqual(executor.commands[1], want) {
			t.Fatalf("podman command = %#v, want %#v", executor.commands, want)
		}
	})
	t.Run("absent", func(t *testing.T) {
		executor := &recordingWorkspaceExecutor{errors: []error{errors.New("missing docker"), errors.New("missing podman")}}
		_, err := NewDetector(executor).Detect(context.Background(), EngineScope{Context: "default", WorkspaceID: "brain-main"})
		if !errors.Is(err, ErrNoEngine) {
			t.Fatalf("Detect() error = %v, want ErrNoEngine", err)
		}
		if len(executor.commands) != 2 {
			t.Fatalf("commands = %#v", executor.commands)
		}
	})
}

package ports

import (
	"context"
	"io"
)

type Redaction struct {
	ArgvIndices     []int
	EnvironmentKeys []string
}

type Command struct {
	Executable  string
	Argv        []string
	Directory   string
	Environment map[string]string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	Redaction   Redaction
}

type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

type Runner interface {
	Run(ctx context.Context, command Command) (Result, error)
}

type StartedRunner interface {
	RunStarted(ctx context.Context, command Command, started func() error) (Result, error)
}

type CommandView struct {
	Executable  string
	Argv        []string
	Directory   string
	Environment map[string]string
}

func (c Command) RedactedView() CommandView {
	argv := append([]string(nil), c.Argv...)
	for _, index := range c.Redaction.ArgvIndices {
		if index >= 0 && index < len(argv) {
			argv[index] = "[REDACTED]"
		}
	}
	environment := make(map[string]string, len(c.Environment))
	for key, value := range c.Environment {
		environment[key] = value
	}
	for _, key := range c.Redaction.EnvironmentKeys {
		if _, ok := environment[key]; ok {
			environment[key] = "[REDACTED]"
		}
	}
	return CommandView{Executable: c.Executable, Argv: argv, Directory: c.Directory, Environment: environment}
}

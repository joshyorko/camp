package subprocess

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"sort"

	"github.com/joshyorko/camp/internal/ports"
)

// Runner executes an already-tokenized command without involving a shell.
type Runner struct{}

func NewRunner() *Runner { return &Runner{} }

func (r *Runner) Run(ctx context.Context, command ports.Command) (ports.Result, error) {
	return r.run(ctx, command, nil)
}

func (r *Runner) RunStarted(ctx context.Context, command ports.Command, started func() error) (ports.Result, error) {
	return r.run(ctx, command, started)
}

func (r *Runner) run(ctx context.Context, command ports.Command, started func() error) (ports.Result, error) {
	cmd := exec.CommandContext(ctx, command.Executable, command.Argv...)
	cmd.Dir = command.Directory
	cmd.Stdin = command.Stdin
	cmd.Env = mergeEnvironment(command.Environment)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = joinedWriter(&stdout, command.Stdout)
	cmd.Stderr = joinedWriter(&stderr, command.Stderr)

	err := cmd.Start()
	if err == nil && started != nil {
		if startedErr := started(); startedErr != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			result := ports.Result{ExitCode: -1, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
			return result, startedErr
		}
	}
	if err == nil {
		err = cmd.Wait()
	}
	result := ports.Result{ExitCode: exitCode(err), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}
	return result, err
}

func joinedWriter(capture *bytes.Buffer, destination io.Writer) io.Writer {
	if destination == nil {
		return capture
	}
	return io.MultiWriter(capture, destination)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func asExitError(err error, target **exec.ExitError) bool {
	exitErr, ok := err.(*exec.ExitError)
	if ok {
		*target = exitErr
	}
	return ok
}

func mergeEnvironment(overrides map[string]string) []string {
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		for index := 0; index < len(entry); index++ {
			if entry[index] == '=' {
				environment[entry[:index]] = entry[index+1:]
				break
			}
		}
	}
	for key, value := range overrides {
		environment[key] = value
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

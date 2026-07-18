package sshtransfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/joshyorko/camp/internal/ports"
)

type Executor struct{}

func NewExecutor() *Executor { return &Executor{} }

func (e *Executor) RunRsync(ctx context.Context, command ports.Command) TransferAttempt {
	attempt := TransferAttempt{Method: MethodRsync}
	cmd, stdout, stderr := prepareCommand(ctx, command)
	if err := cmd.Start(); err != nil {
		attempt.Unavailable = errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		attempt.Err = err
		return attempt
	}
	attempt.ProducerStarted = true
	err := cmd.Wait()
	attempt.Producer = commandResult(err, stdout, stderr)
	attempt.Err = contextError(ctx, err)
	return attempt
}

func (e *Executor) RunTarPipeline(ctx context.Context, pipeline TarPipe) TransferAttempt {
	attempt := TransferAttempt{Method: MethodTarPipe}
	reader, writer, err := os.Pipe()
	if err != nil {
		attempt.Err = err
		return attempt
	}
	defer reader.Close()
	defer writer.Close()

	producerCommand := pipeline.Producer
	producerCommand.Stdout = writer
	consumerCommand := pipeline.Consumer
	consumerCommand.Stdin = reader
	producer, producerStdout, producerStderr := prepareCommand(ctx, producerCommand)
	consumer, consumerStdout, consumerStderr := prepareCommand(ctx, consumerCommand)

	if err := consumer.Start(); err != nil {
		attempt.Unavailable = errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		attempt.Err = err
		return attempt
	}
	attempt.ConsumerStarted = true
	if err := producer.Start(); err != nil {
		attempt.Unavailable = errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		attempt.Err = err
		_ = writer.Close()
		_ = consumer.Process.Kill()
		consumerErr := consumer.Wait()
		attempt.Consumer = commandResult(consumerErr, consumerStdout, consumerStderr)
		return attempt
	}
	attempt.ProducerStarted = true
	_ = reader.Close()
	producerErr := producer.Wait()
	_ = writer.Close()
	consumerErr := consumer.Wait()
	attempt.Producer = commandResult(producerErr, producerStdout, producerStderr)
	attempt.Consumer = commandResult(consumerErr, consumerStdout, consumerStderr)
	attempt.Err = contextError(ctx, errors.Join(producerErr, consumerErr))
	return attempt
}

func prepareCommand(ctx context.Context, command ports.Command) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	cmd := exec.CommandContext(ctx, command.Executable, command.Argv...)
	cmd.Dir = command.Directory
	cmd.Stdin = command.Stdin
	cmd.Env = commandEnvironment(command.Environment)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	cmd.Stdout = joinedOutput(stdout, command.Stdout)
	cmd.Stderr = joinedOutput(stderr, command.Stderr)
	return cmd, stdout, stderr
}

func joinedOutput(capture io.Writer, destination io.Writer) io.Writer {
	if destination == nil {
		return capture
	}
	return io.MultiWriter(capture, destination)
}

func commandEnvironment(overrides map[string]string) []string {
	environment := os.Environ()
	for key, value := range overrides {
		environment = append(environment, key+"="+value)
	}
	return environment
}

func commandResult(err error, stdout, stderr *bytes.Buffer) ports.Result {
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return ports.Result{ExitCode: exitCode, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

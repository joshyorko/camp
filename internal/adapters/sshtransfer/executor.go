package sshtransfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

type Executor struct{}

const diagnosticCaptureLimit = 64 << 10

func NewExecutor() *Executor { return &Executor{} }

func (e *Executor) RunRsync(ctx context.Context, command ports.Command) TransferAttempt {
	attempt := TransferAttempt{Method: MethodRsync}
	cmd, stdout, stderr := prepareCommand(ctx, command, true)
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
	producer, producerStdout, producerStderr := prepareCommand(ctx, producerCommand, false)
	consumer, consumerStdout, consumerStderr := prepareCommand(ctx, consumerCommand, true)

	if err := producer.Start(); err != nil {
		attempt.Unavailable = errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		attempt.Err = err
		return attempt
	}
	attempt.ProducerStarted = true
	if err := consumer.Start(); err != nil {
		attempt.Unavailable = errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist)
		attempt.Err = err
		_ = reader.Close()
		_ = writer.Close()
		_ = producer.Process.Kill()
		producerErr := producer.Wait()
		attempt.Producer = commandResult(producerErr, producerStdout, producerStderr)
		return attempt
	}
	attempt.ConsumerStarted = true
	_ = reader.Close()
	producerErr := producer.Wait()
	_ = writer.Close()
	consumerErr := consumer.Wait()
	attempt.Producer = commandResult(producerErr, producerStdout, producerStderr)
	attempt.Consumer = commandResult(consumerErr, consumerStdout, consumerStderr)
	attempt.Err = contextError(ctx, errors.Join(producerErr, consumerErr))
	return attempt
}

func prepareCommand(ctx context.Context, command ports.Command, captureStdout bool) (*exec.Cmd, *diagnosticBuffer, *diagnosticBuffer) {
	cmd := exec.CommandContext(ctx, command.Executable, command.Argv...)
	cmd.Dir = command.Directory
	cmd.Stdin = command.Stdin
	cmd.Env = commandEnvironment(command.Environment)
	stdout := &diagnosticBuffer{}
	stderr := &diagnosticBuffer{}
	if captureStdout {
		cmd.Stdout = joinedOutput(stdout, command.Stdout)
	} else {
		cmd.Stdout = command.Stdout
	}
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
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		if separator := strings.IndexByte(entry, '='); separator >= 0 {
			values[entry[:separator]] = entry[separator+1:]
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func commandResult(err error, stdout, stderr *diagnosticBuffer) ports.Result {
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

type diagnosticBuffer struct{ buffer bytes.Buffer }

func (b *diagnosticBuffer) Len() int      { return b.buffer.Len() }
func (b *diagnosticBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *diagnosticBuffer) Write(payload []byte) (int, error) {
	original := len(payload)
	remaining := diagnosticCaptureLimit - b.Len()
	if remaining > 0 {
		if len(payload) > remaining {
			payload = payload[:remaining]
		}
		_, _ = b.buffer.Write(payload)
	}
	return original, nil
}

func contextError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

package subprocess

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/ports"
)

func TestRunnerExecutesStructuredCommandWithoutShellExpansion(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the Camp local lifecycle is Linux-only")
	}

	var stdout bytes.Buffer
	result, err := NewRunner().Run(context.Background(), ports.Command{
		Executable:  "/usr/bin/printf",
		Argv:        []string{"%s", "$(touch should-not-exist);$HOME"},
		Environment: map[string]string{"CAMP_TEST_ENV": "present"},
		Stdout:      &stdout,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
	const want = "$(touch should-not-exist);$HOME"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := string(result.Stdout); got != want {
		t.Fatalf("captured stdout = %q, want %q", got, want)
	}
}

func TestRunnerReturnsTypedExitAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	result, err := NewRunner().Run(context.Background(), ports.Command{
		Executable: "/bin/sh",
		Argv:       []string{"-c", "printf failure >&2; exit 17"},
	})
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Run() error = %T %v, want *exec.ExitError", err, err)
	}
	if result.ExitCode != 17 || string(result.Stderr) != "failure" {
		t.Fatalf("result = %#v, want exit 17 and stderr", result)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = NewRunner().Run(ctx, ports.Command{Executable: "/bin/sh", Argv: []string{"-c", "sleep 30"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled Run() error = %v, want deadline exceeded", err)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("canceled child was not terminated promptly")
	}
}

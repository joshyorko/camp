package subprocess

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
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

func TestRunnerKeepsInteractiveCommandInForegroundProcessGroup(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("Camp interactive process-group handling is Linux-only")
	}

	result, err := NewRunner().Run(context.Background(), ports.Command{
		Executable: "/bin/sh",
		Argv: []string{"-c", `
			parent=$(ps -o pgid= -p "$PPID" | tr -d ' ')
			self=$(ps -o pgid= -p "$$" | tr -d ' ')
			test "$self" = "$parent"
		`},
		Stdin:  bytes.NewReader(nil),
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("interactive Run() error = %v, result = %#v", err, result)
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

func TestRunnerCancellationTerminatesSpawnedProcessGroup(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("Camp process-group cancellation is Linux-only")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := NewRunner().Run(ctx, ports.Command{
		Executable: "/bin/sh",
		Argv:       []string{"-c", "sleep 30 & wait"},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled process group error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("canceled process group remained alive for %s", elapsed)
	}
}

func TestRunStartedCallsCallbackOnceAfterStartAndBeforeWait(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the Camp local lifecycle is Linux-only")
	}

	marker := filepath.Join(t.TempDir(), "started")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	result, err := NewRunner().RunStarted(ctx, ports.Command{
		Executable: "/bin/sh",
		Argv:       []string{"-c", `while [ ! -f "$1" ]; do :; done; printf child-finished`, "camp-run-started", marker},
	}, func() error {
		calls.Add(1)
		return os.WriteFile(marker, []byte("ready"), 0o600)
	})
	if err != nil {
		t.Fatalf("RunStarted() error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("started callback calls = %d, want 1", got)
	}
	if result.ExitCode != 0 || string(result.Stdout) != "child-finished" {
		t.Fatalf("result = %#v, want successful child completion", result)
	}
}

func TestRunStartedDoesNotCallCallbackWhenStartFails(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	_, err := NewRunner().RunStarted(context.Background(), ports.Command{
		Executable: filepath.Join(t.TempDir(), "missing-executable"),
	}, func() error {
		calls.Add(1)
		return nil
	})
	if err == nil {
		t.Fatal("RunStarted() error = nil, want start failure")
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("started callback calls = %d, want 0", got)
	}
}

func TestRunStartedCallbackFailureKillsAndReapsChild(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the Camp local lifecycle is Linux-only")
	}

	pidPath := filepath.Join(t.TempDir(), "child.pid")
	callbackErr := errors.New("record durable start fact")
	var pid int
	result, err := NewRunner().RunStarted(context.Background(), ports.Command{
		Executable: "/bin/sh",
		Argv:       []string{"-c", `printf '%s' "$$" > "$1"; while :; do :; done`, "camp-run-started", pidPath},
	}, func() error {
		deadline := time.Now().Add(5 * time.Second)
		for {
			pidBytes, err := os.ReadFile(pidPath)
			if err == nil {
				parsed, parseErr := strconv.Atoi(string(pidBytes))
				if parseErr == nil {
					pid = parsed
					return callbackErr
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if time.Now().After(deadline) {
				return errors.New("child did not publish its PID")
			}
			runtime.Gosched()
		}
	})
	if !errors.Is(err, callbackErr) {
		t.Fatalf("RunStarted() error = %v, want callback error", err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1", result.ExitCode)
	}
	var status syscall.WaitStatus
	if waited, waitErr := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); waited != -1 || !errors.Is(waitErr, syscall.ECHILD) {
		t.Fatalf("Wait4(%d) = (%d, %v), want (-1, ECHILD) after child reap", pid, waited, waitErr)
	}
}

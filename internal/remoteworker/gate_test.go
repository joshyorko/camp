package remoteworker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGatePublishesOneDurableSuccessForConcurrentAwaiters(t *testing.T) {
	directory := t.TempDir()
	request := filepath.Join(directory, "request.json")
	if err := os.WriteFile(request, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(_ context.Context, input io.Reader, output io.Writer) error {
		calls++
		if _, err := io.ReadAll(input); err != nil {
			return err
		}
		_, err := output.Write([]byte(`{"status":"ok"}`))
		return err
	}
	runGateInvocation(t, request, "hook.gate", 3, runner)
	if calls != 1 {
		t.Fatalf("mutation calls = %d", calls)
	}
}

func TestGateScopesReceiptsToSequentialLifecycleInvocations(t *testing.T) {
	directory := t.TempDir()
	request := filepath.Join(directory, "request.json")
	if err := os.WriteFile(request, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	runner := func(_ context.Context, _ io.Reader, _ io.Writer) error {
		calls++
		return nil
	}
	runGateInvocation(t, request, "hook.gate", 2, runner)

	staleAccepted := make(chan error, 2)
	for range 2 {
		go func() {
			staleAccepted <- AwaitGate(t.Context(), directory, "hook.gate")
		}()
	}
	select {
	case err := <-staleAccepted:
		t.Fatalf("awaiter accepted stale receipt: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	if err := runGate(t.Context(), request, "hook.gate", 2, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-staleAccepted; err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("mutation calls after two invocations = %d", calls)
	}
}

func TestGateFailureAndUnknownOutcomeFailClosed(t *testing.T) {
	directory := t.TempDir()
	request := filepath.Join(directory, "request.json")
	if err := os.WriteFile(request, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("mutation failed")
	awaited := make(chan error, 1)
	go func() {
		awaited <- AwaitGate(t.Context(), directory, "failure.gate")
	}()
	if err := runGate(t.Context(), request, "failure.gate", 1, &bytes.Buffer{}, func(context.Context, io.Reader, io.Writer) error {
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("runGate() error = %v", err)
	}
	if err := <-awaited; err == nil {
		t.Fatal("AwaitGate() accepted failed mutation")
	}

	if err := os.WriteFile(filepath.Join(directory, "unknown.gate.intent"), []byte("pending\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := AwaitGate(ctx, directory, "unknown.gate"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AwaitGate() unknown error = %v", err)
	}
}

func runGateInvocation(t *testing.T, request, gate string, awaiters int, runner gateRunner) {
	t.Helper()
	results := make(chan error, awaiters)
	for range awaiters {
		go func() {
			results <- AwaitGate(t.Context(), filepath.Dir(request), gate)
		}()
	}
	if err := runGate(t.Context(), request, gate, awaiters, io.Discard, runner); err != nil {
		t.Fatal(err)
	}
	for range awaiters {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestGateRejectsUnsafePaths(t *testing.T) {
	for _, gate := range []string{"", "../gate", "nested/gate"} {
		if err := AwaitGate(t.Context(), t.TempDir(), gate); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("AwaitGate(%q) error = %v", gate, err)
		}
	}
}

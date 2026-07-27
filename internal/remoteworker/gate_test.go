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
	if err := runGate(t.Context(), request, "hook.gate", &bytes.Buffer{}, runner); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := AwaitGate(t.Context(), directory, "hook.gate"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("mutation calls = %d", calls)
	}
}

func TestGateFailureAndUnknownOutcomeFailClosed(t *testing.T) {
	directory := t.TempDir()
	request := filepath.Join(directory, "request.json")
	if err := os.WriteFile(request, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("mutation failed")
	if err := runGate(t.Context(), request, "failure.gate", &bytes.Buffer{}, func(context.Context, io.Reader, io.Writer) error {
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("runGate() error = %v", err)
	}
	if err := AwaitGate(t.Context(), directory, "failure.gate"); err == nil {
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

func TestGateRejectsUnsafePaths(t *testing.T) {
	for _, gate := range []string{"", "../gate", "nested/gate"} {
		if err := AwaitGate(t.Context(), t.TempDir(), gate); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("AwaitGate(%q) error = %v", gate, err)
		}
	}
}

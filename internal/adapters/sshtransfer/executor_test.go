package sshtransfer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestCommandEnvironmentReplacesInheritedKeys(t *testing.T) {
	t.Setenv("CAMP_TRANSFER_ENV_TEST", "inherited")
	environment := commandEnvironment(map[string]string{"CAMP_TRANSFER_ENV_TEST": "override", "CAMP_TRANSFER_ENV_NEW": "new"})

	counts := map[string]int{}
	values := map[string]string{}
	for _, entry := range environment {
		for _, key := range []string{"CAMP_TRANSFER_ENV_TEST", "CAMP_TRANSFER_ENV_NEW"} {
			if strings.HasPrefix(entry, key+"=") {
				counts[key]++
				values[key] = strings.TrimPrefix(entry, key+"=")
			}
		}
	}
	if counts["CAMP_TRANSFER_ENV_TEST"] != 1 || values["CAMP_TRANSFER_ENV_TEST"] != "override" {
		t.Fatalf("override entries=%d value=%q", counts["CAMP_TRANSFER_ENV_TEST"], values["CAMP_TRANSFER_ENV_TEST"])
	}
	if counts["CAMP_TRANSFER_ENV_NEW"] != 1 || values["CAMP_TRANSFER_ENV_NEW"] != "new" {
		t.Fatalf("new entries=%d value=%q", counts["CAMP_TRANSFER_ENV_NEW"], values["CAMP_TRANSFER_ENV_NEW"])
	}
}

func TestTarPipelineStreamsArchiveWithoutCapturingItAndBoundsDiagnostics(t *testing.T) {
	t.Parallel()
	executor := NewExecutor()
	attempt := executor.RunTarPipeline(context.Background(), TarPipe{
		Producer: ports.Command{Executable: "/bin/sh", Argv: []string{"-c", "head -c 2097152 /dev/zero; head -c 131072 /dev/zero >&2"}},
		Consumer: ports.Command{Executable: "/bin/sh", Argv: []string{"-c", "cat >/dev/null"}},
	})
	if _, err := ClassifyTransfer(attempt); err != nil {
		t.Fatalf("ClassifyTransfer() error = %v", err)
	}
	if len(attempt.Producer.Stdout) != 0 {
		t.Fatalf("producer captured %d archive bytes", len(attempt.Producer.Stdout))
	}
	if len(attempt.Producer.Stderr) != diagnosticCaptureLimit {
		t.Fatalf("producer diagnostic bytes = %d, want bounded %d", len(attempt.Producer.Stderr), diagnosticCaptureLimit)
	}
}

func TestTarPipelineProducerStartFailureNeverStartsConsumer(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "consumer-started")
	attempt := NewExecutor().RunTarPipeline(context.Background(), TarPipe{
		Producer: ports.Command{Executable: filepath.Join(t.TempDir(), "missing-producer")},
		Consumer: ports.Command{Executable: "/bin/sh", Argv: []string{"-c", "touch \"$1\"", "sh", marker}},
	})
	if attempt.ProducerStarted || attempt.ConsumerStarted {
		t.Fatalf("started producer=%v consumer=%v", attempt.ProducerStarted, attempt.ConsumerStarted)
	}
	if !attempt.Unavailable || !errors.Is(attempt.Err, os.ErrNotExist) {
		t.Fatalf("attempt = %#v", attempt)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("consumer marker error = %v, want not exist", err)
	}
	_, err := ClassifyTransfer(attempt)
	var failure *TransferFailure
	if !errors.As(err, &failure) || failure.Kind != FailureNotStarted {
		t.Fatalf("failure = %#v, want not-started", failure)
	}
}

func TestTarPipelineConsumerStartFailureIsNotAStagingMutation(t *testing.T) {
	t.Parallel()
	attempt := NewExecutor().RunTarPipeline(context.Background(), TarPipe{
		Producer: ports.Command{Executable: "/bin/sh", Argv: []string{"-c", "sleep 10"}},
		Consumer: ports.Command{Executable: filepath.Join(t.TempDir(), "missing-consumer")},
	})
	if !attempt.ProducerStarted || attempt.ConsumerStarted {
		t.Fatalf("started producer=%v consumer=%v", attempt.ProducerStarted, attempt.ConsumerStarted)
	}
	_, err := ClassifyTransfer(attempt)
	var failure *TransferFailure
	if !errors.As(err, &failure) || failure.Kind != FailureNotStarted {
		t.Fatalf("failure = %#v, want not-started because staging consumer never started", failure)
	}
}

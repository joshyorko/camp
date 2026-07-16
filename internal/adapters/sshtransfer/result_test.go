package sshtransfer

import (
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

func TestTransferResultsClassifyPartialFailures(t *testing.T) {
	t.Parallel()

	cause := errors.New("destination tar exited")
	_, err := ClassifyTransfer(TransferAttempt{
		Method:          MethodTarPipe,
		ProducerStarted: true,
		ConsumerStarted: true,
		Producer:        ports.Result{ExitCode: 0},
		Consumer:        ports.Result{ExitCode: 2, Stderr: []byte("unexpected EOF")},
		Err:             cause,
	})
	var failure *TransferFailure
	if !errors.As(err, &failure) {
		t.Fatalf("ClassifyTransfer() error = %v, want *TransferFailure", err)
	}
	if failure.Kind != FailurePartial || failure.Method != MethodTarPipe {
		t.Fatalf("failure = %#v, want partial tar-pipe failure", failure)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want wrapped cause", err)
	}
	if failure.Producer.ExitCode != 0 || failure.Consumer.ExitCode != 2 {
		t.Fatalf("failure results = %#v", failure)
	}

	_, err = ClassifyTransfer(TransferAttempt{Method: MethodRsync, Err: errors.New("rsync not found")})
	if !errors.As(err, &failure) || failure.Kind != FailureNotStarted {
		t.Fatalf("pre-start error = %#v, want not-started failure", err)
	}

	got, err := ClassifyTransfer(TransferAttempt{
		Method:          MethodRsync,
		ProducerStarted: true,
		Producer:        ports.Result{ExitCode: 0, Stdout: []byte("complete")},
	})
	if err != nil {
		t.Fatalf("successful ClassifyTransfer() error = %v", err)
	}
	if got.Method != MethodRsync || got.Producer.ExitCode != 0 || string(got.Producer.Stdout) != "complete" {
		t.Fatalf("result = %#v", got)
	}
}

func TestTarFallbackOnlyAllowedBeforeRsyncStarts(t *testing.T) {
	t.Parallel()

	_, notStarted := ClassifyTransfer(TransferAttempt{Method: MethodRsync, Unavailable: true, Err: errors.New("rsync not found")})
	if !TarFallbackAllowed(notStarted) {
		t.Fatal("TarFallbackAllowed() = false for rsync pre-start failure")
	}
	_, genericNotStarted := ClassifyTransfer(TransferAttempt{Method: MethodRsync, Err: errors.New("ssh configuration failed")})
	if TarFallbackAllowed(genericNotStarted) {
		t.Fatal("TarFallbackAllowed() = true for a pre-start failure not classified as rsync unavailable")
	}

	_, partial := ClassifyTransfer(TransferAttempt{
		Method:          MethodRsync,
		ProducerStarted: true,
		Producer:        ports.Result{ExitCode: 23},
		Err:             errors.New("rsync interrupted"),
	})
	if TarFallbackAllowed(partial) {
		t.Fatal("TarFallbackAllowed() = true after rsync may have mutated the destination")
	}
	if TarFallbackAllowed(errors.New("untyped failure")) {
		t.Fatal("TarFallbackAllowed() = true for untyped failure")
	}
}

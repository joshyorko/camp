package sshtransfer

import (
	"errors"
	"fmt"

	"github.com/joshyorko/camp/internal/ports"
)

type Method string

const (
	MethodRsync   Method = "rsync"
	MethodTarPipe Method = "tar-pipe"
)

type FailureKind string

const (
	FailureNotStarted FailureKind = "not-started"
	FailurePartial    FailureKind = "partial"
)

type TransferAttempt struct {
	Method          Method
	ProducerStarted bool
	ConsumerStarted bool
	Unavailable     bool
	Producer        ports.Result
	Consumer        ports.Result
	Err             error
}

type TransferResult struct {
	Method   Method
	Producer ports.Result
	Consumer ports.Result
}

type TransferFailure struct {
	Method      Method
	Kind        FailureKind
	Unavailable bool
	Producer    ports.Result
	Consumer    ports.Result
	Cause       error
}

func (e *TransferFailure) Error() string {
	return fmt.Sprintf("%s transfer %s: %v", e.Method, e.Kind, e.Cause)
}

func (e *TransferFailure) Unwrap() error { return e.Cause }

func ClassifyTransfer(attempt TransferAttempt) (TransferResult, error) {
	if attempt.Method != MethodRsync && attempt.Method != MethodTarPipe {
		return TransferResult{}, errors.New("unknown transfer method")
	}
	cause := attempt.Err
	if cause == nil && attempt.Producer.ExitCode != 0 {
		cause = fmt.Errorf("producer exited with status %d", attempt.Producer.ExitCode)
	}
	if cause == nil && attempt.Method == MethodTarPipe && attempt.Consumer.ExitCode != 0 {
		cause = fmt.Errorf("consumer exited with status %d", attempt.Consumer.ExitCode)
	}
	started := attempt.ProducerStarted
	if attempt.Method == MethodTarPipe {
		started = attempt.ConsumerStarted
	}
	if cause == nil && (!attempt.ProducerStarted || (attempt.Method == MethodTarPipe && !attempt.ConsumerStarted)) {
		cause = errors.New("transfer did not start completely")
	}
	if cause != nil {
		kind := FailureNotStarted
		if started {
			kind = FailurePartial
		}
		return TransferResult{}, &TransferFailure{
			Method:      attempt.Method,
			Kind:        kind,
			Unavailable: attempt.Unavailable && !started,
			Producer:    attempt.Producer,
			Consumer:    attempt.Consumer,
			Cause:       cause,
		}
	}
	return TransferResult{
		Method:   attempt.Method,
		Producer: attempt.Producer,
		Consumer: attempt.Consumer,
	}, nil
}

// TarFallbackAllowed reports whether rsync is known not to have started. A
// partial rsync failure must be recovered from a fresh staging destination,
// never continued by extracting tar into the possibly mutated destination.
func TarFallbackAllowed(err error) bool {
	var failure *TransferFailure
	return errors.As(err, &failure) && failure.Method == MethodRsync && failure.Kind == FailureNotStarted && failure.Unavailable
}

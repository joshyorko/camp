package journal

import (
	"context"
	"errors"
	"fmt"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var ErrUnknownTransition = errors.New("pending intent has no reconciliation observer")

type Observer func(context.Context, domain.JournalSnapshot, ports.IntentRecord) (ports.FactRecord, domain.JournalSnapshot, error)

func Reconcile(ctx context.Context, log ports.Journal, sessionID string, observers map[string]Observer) (domain.JournalSnapshot, error) {
	snapshot, pending, err := log.Load(ctx, sessionID)
	if err != nil {
		return domain.JournalSnapshot{}, err
	}
	for _, item := range pending {
		observer, ok := observers[item.Intent.Transition]
		if !ok {
			return snapshot, fmt.Errorf("transition %q: %w", item.Intent.Transition, ErrUnknownTransition)
		}
		fact, next, err := observer(ctx, snapshot, item.Intent)
		if err != nil {
			return snapshot, err
		}
		if fact.IntentID != item.Intent.ID || fact.SessionID != sessionID || fact.Transition != item.Intent.Transition {
			return snapshot, errors.New("reconciliation observer returned mismatched fact")
		}
		if err := log.RecordFact(ctx, fact, next); err != nil {
			return snapshot, err
		}
		reduced, _, err := log.Load(ctx, sessionID)
		if err != nil {
			return snapshot, err
		}
		snapshot = reduced
	}
	return snapshot, nil
}

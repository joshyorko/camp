package app

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type CloseEffects interface {
	CloseWorkspace(context.Context, domain.JournalSnapshot, bool) error
	StopForwarders(context.Context, domain.JournalSnapshot) error
	StopServices(context.Context, domain.JournalSnapshot) error
	StopSupervisor(context.Context, domain.JournalSnapshot) error
	ReleaseLease(context.Context, domain.JournalSnapshot) error
	RemoveMaterialization(context.Context, domain.JournalSnapshot) (bool, error)
}

type CloseRequest struct {
	SessionID     string
	KeepWorkspace bool
}

type CloseResult struct {
	PublicationSucceeded bool                 `json:"publicationSucceeded"`
	CleanupSucceeded     bool                 `json:"cleanupSucceeded"`
	Generation           domain.GenerationRef `json:"generation,omitempty"`
	RefreshError         string               `json:"refreshError,omitempty"`
	RecoveryCommand      string               `json:"recoveryCommand,omitempty"`
}

type Close struct {
	journal   ports.Journal
	locks     operationLocker
	publisher checkpointPublisher
	effects   CloseEffects
	clock     ports.Clock
}

func NewClose(journal ports.Journal, locks operationLocker, publisher checkpointPublisher, effects CloseEffects, clock ports.Clock) *Close {
	return &Close{journal: journal, locks: locks, publisher: publisher, effects: effects, clock: clock}
}

func (u *Close) Run(ctx context.Context, request CloseRequest) (result CloseResult, resultErr error) {
	if u == nil || u.journal == nil || u.locks == nil || u.publisher == nil || u.effects == nil || u.clock == nil || request.SessionID == "" {
		return CloseResult{}, errors.New("close dependencies or request are incomplete")
	}
	result.RecoveryCommand = "camp recover " + request.SessionID
	token, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: request.SessionID, Operation: "close"})
	if err != nil {
		return result, err
	}
	defer func() {
		if err := u.locks.Release(context.WithoutCancel(ctx), token); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	snapshot, pending, err := u.journal.Load(ctx, request.SessionID)
	if err != nil {
		return result, err
	}
	if snapshot.State == domain.SessionClosed {
		result.CleanupSucceeded = snapshot.Cleanup.State == domain.CleanupSucceeded
		result.PublicationSucceeded = snapshot.Checkpoint.PublicationSucceeded
		if snapshot.Checkpoint.Generation != nil {
			result.Generation = *snapshot.Checkpoint.Generation
		}
		return result, nil
	}
	if len(pending) != 0 {
		if snapshot.Mode != domain.SessionReadWrite {
			return result, fmt.Errorf("close requires recovery of %d pending transition(s)", len(pending))
		}
		recovered, err := u.publisher.Publish(ctx, token, snapshot.SessionID)
		if err != nil {
			return result, fmt.Errorf("recover pending checkpoint before close: %w", err)
		}
		if !recovered.Published || recovered.RefreshError != "" {
			return result, fmt.Errorf("recover pending checkpoint before close: %s", recovered.RefreshError)
		}
		snapshot, pending, err = u.journal.Load(ctx, request.SessionID)
		if err != nil {
			return result, err
		}
		if len(pending) != 0 {
			return result, fmt.Errorf("close recovery left %d pending transition(s)", len(pending))
		}
	}
	now := u.clock.Now().UTC()
	sequence := 1
	if err := u.record(ctx, &snapshot, "CloseStarted", sequence, now); err != nil {
		return result, err
	}
	sequence++
	snapshot.Cleanup = domain.Cleanup{State: domain.CleanupRunning}

	if snapshot.Mode == domain.SessionReadWrite {
		published, err := u.publisher.Publish(ctx, token, snapshot.SessionID)
		if err != nil {
			return result, err
		}
		if !published.Published {
			return result, errors.New("final checkpoint did not publish")
		}
		if loaded, _, loadErr := u.journal.Load(ctx, snapshot.SessionID); loadErr == nil {
			snapshot = loaded
		}
		result.PublicationSucceeded = true
		result.Generation = published.Generation
		result.RefreshError = published.RefreshError
		snapshot.Checkpoint.State = domain.CheckpointPublished
		snapshot.Checkpoint.Generation = cloneGeneration(&published.Generation)
		snapshot.Checkpoint.PublicationSucceeded = true
		if err := u.record(context.WithoutCancel(ctx), &snapshot, "FinalCheckpointComplete", sequence, now); err != nil {
			return result, err
		}
	} else {
		if err := u.record(ctx, &snapshot, "ReadonlyDiscardRecorded", sequence, now); err != nil {
			return result, err
		}
	}
	sequence++

	steps := []struct {
		transition string
		effect     func() error
	}{
		{"WorkspaceStoppedOrDeleted", func() error { return u.effects.CloseWorkspace(ctx, snapshot, request.KeepWorkspace) }},
		{"ForwardersStopped", func() error { return u.effects.StopForwarders(ctx, snapshot) }},
		{"ServicesStopped", func() error { return u.effects.StopServices(ctx, snapshot) }},
		{"SupervisorStopped", func() error { return u.effects.StopSupervisor(ctx, snapshot) }},
	}
	if snapshot.Mode == domain.SessionReadWrite && snapshot.Lease.Lease != nil {
		steps = append(steps, struct {
			transition string
			effect     func() error
		}{"LeaseReleased", func() error { return u.effects.ReleaseLease(ctx, snapshot) }})
	}
	materializationTransition := "AdoptedPreserved"
	if snapshot.Materialization.Mode == domain.MaterializationCreated {
		materializationTransition = "MaterializationRemoved"
	}
	steps = append(steps, struct {
		transition string
		effect     func() error
	}{materializationTransition, func() error {
		removed, err := u.effects.RemoveMaterialization(ctx, snapshot)
		if err != nil {
			return err
		}
		if snapshot.Materialization.Mode == domain.MaterializationCreated && !removed {
			return errors.New("owned materialization was not removed")
		}
		if snapshot.Materialization.Mode == domain.MaterializationAdopted && removed {
			return errors.New("adopted materialization was removed")
		}
		return nil
	}})

	for _, step := range steps {
		intent := closeIntent(snapshot.SessionID, step.transition, sequence, now)
		if err := u.journal.RecordIntent(ctx, intent); err != nil {
			return result, err
		}
		if err := step.effect(); err != nil {
			snapshot.Cleanup = domain.Cleanup{State: domain.CleanupFailed, LastErr: err.Error()}
			_ = u.record(context.WithoutCancel(ctx), &snapshot, "CleanupFailed", sequence, now)
			return result, err
		}
		if err := u.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(intent, now), snapshot); err != nil {
			return result, err
		}
		sequence++
	}
	snapshot.State = domain.SessionClosed
	snapshot.Cleanup = domain.Cleanup{State: domain.CleanupSucceeded}
	if err := u.record(context.WithoutCancel(ctx), &snapshot, "SessionClosed", sequence, now); err != nil {
		return result, err
	}
	result.CleanupSucceeded = true
	if snapshot.Mode == domain.SessionReadOnly {
		result.RecoveryCommand = ""
	}
	return result, nil
}

func (u *Close) record(ctx context.Context, snapshot *domain.JournalSnapshot, transition string, sequence int, timestamp time.Time) error {
	// This indirection keeps all close-only durable transitions paired while
	// external effects remain explicitly bracketed in Run.
	intent := closeIntent(snapshot.SessionID, transition, sequence, timestamp)
	if err := u.journal.RecordIntent(ctx, intent); err != nil {
		return err
	}
	return u.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(intent, timestamp), *snapshot)
}

func closeIntent(sessionID, transition string, sequence int, timestamp time.Time) ports.IntentRecord {
	return ports.IntentRecord{ID: sessionID + "-close-" + strconv.Itoa(sequence) + "-" + transition, SessionID: sessionID, Transition: transition, Attempt: 1, Timestamp: timestamp}
}

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
	Discard       bool
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
	result.RecoveryCommand = "camp close --session " + request.SessionID
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
	resumeIntent, resumeIndex, resumeCleanup, err := selectCloseCleanupPending(snapshot, pending)
	if err != nil {
		return result, err
	}
	now := u.clock.Now().UTC()
	sequence := 1
	if resumeCleanup {
		result.PublicationSucceeded = snapshot.Checkpoint.PublicationSucceeded
		if snapshot.Checkpoint.Generation != nil {
			result.Generation = *snapshot.Checkpoint.Generation
		}
	} else {
		if err := u.record(ctx, &snapshot, "CloseStarted", sequence, now); err != nil {
			return result, err
		}
		sequence++
		snapshot.Cleanup = domain.Cleanup{State: domain.CleanupRunning}

		if snapshot.Mode == domain.SessionReadWrite && !request.Discard {
			published, err := u.publisher.Publish(ctx, token, snapshot.SessionID)
			if published.Published {
				result.PublicationSucceeded = true
				result.Generation = published.Generation
				result.RefreshError = published.RefreshError
				result.RecoveryCommand = "camp recover " + request.SessionID
			}
			if err != nil {
				return result, err
			}
			if !published.Published {
				if published.Disposition == CheckpointDispositionRemotePrepared && published.Remote != nil {
					return result, errors.New("remote checkpoint is prepared and remains quiesced pending inbound publication")
				}
				return result, errors.New("final checkpoint did not publish")
			}
			if loaded, _, loadErr := u.journal.Load(ctx, snapshot.SessionID); loadErr == nil {
				snapshot = loaded
			}
			snapshot.Checkpoint.State = domain.CheckpointPublished
			snapshot.Checkpoint.Generation = cloneGeneration(&published.Generation)
			snapshot.Checkpoint.PublicationSucceeded = true
			if err := u.record(context.WithoutCancel(ctx), &snapshot, "FinalCheckpointComplete", sequence, now); err != nil {
				return result, err
			}
		} else {
			transition := "ReadonlyDiscardRecorded"
			if request.Discard {
				transition = "WritableDiscardRecorded"
			}
			if err := u.record(ctx, &snapshot, transition, sequence, now); err != nil {
				return result, err
			}
		}
		sequence++
	}

	steps := u.closeCleanupSteps(ctx, request.KeepWorkspace, snapshot)
	for i, step := range steps {
		intentSequence := i + 3
		if resumeCleanup && i < resumeIndex {
			continue
		}
		intent := closeIntent(snapshot.SessionID, step.transition, intentSequence, now)
		if resumeCleanup && i == resumeIndex {
			intent = resumeIntent.Intent
		} else if err := u.journal.RecordIntent(ctx, intent); err != nil {
			return result, err
		}
		if err := step.effect(); err != nil {
			snapshot.Cleanup = domain.Cleanup{State: domain.CleanupFailed, LastErr: err.Error()}
			_ = u.record(context.WithoutCancel(ctx), &snapshot, "CleanupFailed", intentSequence, now)
			return result, err
		}
		if err := u.journal.RecordFact(context.WithoutCancel(ctx), checkpointFact(intent, now), snapshot); err != nil {
			return result, err
		}
		if err := reportProgress(ctx, closeProgressEvent(step.transition)); err != nil {
			return result, err
		}
	}
	snapshot.State = domain.SessionClosed
	snapshot.Cleanup = domain.Cleanup{State: domain.CleanupSucceeded}
	if err := u.record(context.WithoutCancel(ctx), &snapshot, "SessionClosed", len(steps)+3, now); err != nil {
		return result, err
	}
	result.CleanupSucceeded = true
	if snapshot.Mode == domain.SessionReadOnly || request.Discard {
		result.RecoveryCommand = ""
	}
	return result, nil
}

func closeProgressEvent(transition string) ProgressEvent {
	stages := map[string]ProgressStage{
		"WorkspaceStoppedOrDeleted": ProgressWorkspaceClosed,
		"ForwardersStopped":         ProgressForwardersStopped,
		"ServicesStopped":           ProgressServicesStopped,
		"SupervisorStopped":         ProgressSupervisorStopped,
		"LeaseReleased":             ProgressLeaseReleased,
		"MaterializationRemoved":    ProgressMaterializationRemoved,
		"AdoptedPreserved":          ProgressMaterializationPreserved,
	}
	return ProgressEvent{Stage: stages[transition]}
}

func selectCloseCleanupPending(snapshot domain.JournalSnapshot, pending []ports.PendingIntent) (ports.PendingIntent, int, bool, error) {
	if snapshot.Cleanup.State != domain.CleanupRunning && snapshot.Cleanup.State != domain.CleanupFailed {
		for _, item := range pending {
			if isCloseCleanupTransition(item.Intent.Transition) {
				return ports.PendingIntent{}, 0, false, fmt.Errorf("unsupported pending cleanup state %q", snapshot.Cleanup.State)
			}
		}
		return ports.PendingIntent{}, 0, false, nil
	}
	if len(pending) != 1 {
		return ports.PendingIntent{}, 0, false, fmt.Errorf("close recovery requires exactly one pending cleanup intent, got %d", len(pending))
	}
	index, ok := closeCleanupTransitionIndex(pending[0].Intent.Transition)
	if !ok {
		return ports.PendingIntent{}, 0, false, fmt.Errorf("unsupported pending cleanup transition %q", pending[0].Intent.Transition)
	}
	return pending[0], index, true, nil
}

func isCloseCleanupTransition(transition string) bool {
	switch transition {
	case "WorkspaceStoppedOrDeleted", "ForwardersStopped", "ServicesStopped", "SupervisorStopped", "LeaseReleased", "MaterializationRemoved", "AdoptedPreserved":
		return true
	default:
		return false
	}
}

func closeCleanupTransitionIndex(transition string) (int, bool) {
	switch transition {
	case "WorkspaceStoppedOrDeleted":
		return 0, true
	case "ForwardersStopped":
		return 1, true
	case "ServicesStopped":
		return 2, true
	case "SupervisorStopped":
		return 3, true
	default:
		return 0, false
	}
}

func (u *Close) closeCleanupSteps(ctx context.Context, keepWorkspace bool, snapshot domain.JournalSnapshot) []struct {
	transition string
	effect     func() error
} {
	steps := []struct {
		transition string
		effect     func() error
	}{
		{"WorkspaceStoppedOrDeleted", func() error { return u.effects.CloseWorkspace(ctx, snapshot, keepWorkspace) }},
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
	return steps
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

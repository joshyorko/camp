package app

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type operationLocker interface {
	Acquire(context.Context, ports.OperationOwner) (ports.OperationToken, error)
	Release(context.Context, ports.OperationToken) error
}

type checkpointPublisher interface {
	Publish(context.Context, ports.OperationToken, string) (CheckpointResult, error)
}

type sessionReader interface {
	Load(context.Context, string) (domain.JournalSnapshot, []ports.PendingIntent, error)
}

type Sync struct {
	sessions  sessionReader
	locks     operationLocker
	publisher checkpointPublisher
}

func NewSync(sessions sessionReader, locks operationLocker, publisher checkpointPublisher) *Sync {
	return &Sync{sessions: sessions, locks: locks, publisher: publisher}
}

func (u *Sync) Run(ctx context.Context, sessionID string) (result CheckpointResult, resultErr error) {
	if u == nil || u.sessions == nil || u.locks == nil || u.publisher == nil || sessionID == "" {
		return CheckpointResult{}, errors.New("sync dependencies or session are incomplete")
	}
	snapshot, _, err := u.sessions.Load(ctx, sessionID)
	if err != nil {
		return CheckpointResult{}, err
	}
	if snapshot.SessionID != sessionID {
		return CheckpointResult{}, errors.New("sync journal returned the wrong session")
	}
	if snapshot.Mode == domain.SessionReadOnly {
		return CheckpointResult{Disposition: CheckpointDispositionSkippedReadOnly}, nil
	}
	token, err := u.locks.Acquire(ctx, ports.OperationOwner{SessionID: sessionID, Operation: "sync"})
	if err != nil {
		return CheckpointResult{}, err
	}
	defer func() {
		if err := u.locks.Release(context.WithoutCancel(ctx), token); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	result, err = u.publisher.Publish(ctx, token, sessionID)
	if err != nil {
		return result, err
	}
	if result.Disposition == CheckpointDispositionRemotePrepared && (result.Published || result.Remote == nil) {
		return result, errors.New("remote checkpoint preparation returned an invalid publication state")
	}
	return result, nil
}

package ports

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrOperationLocked = errors.New("session operation is already locked")

type OperationOwner struct {
	SessionID string `json:"sessionId"`
	Operation string `json:"operation"`
}

type OperationToken struct {
	ID       string                 `json:"id"`
	Owner    OperationOwner         `json:"owner"`
	Identity domain.ProcessIdentity `json:"identity"`
}

type IdentityVerifier interface {
	CurrentProcess(context.Context) (domain.ProcessIdentity, error)
	IsCurrent(context.Context, domain.ProcessIdentity) (bool, error)
}

type OperationLocker interface {
	Acquire(context.Context, OperationOwner) (OperationToken, error)
	Validate(context.Context, OperationToken) error
	Release(context.Context, OperationToken) error
}

type OperationTokenValidator interface {
	Validate(context.Context, OperationToken) error
}

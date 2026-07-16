package ports

import (
	"context"

	"github.com/joshyorko/camp/internal/domain"
)

type HostIdentity interface {
	MachineID(context.Context) (string, error)
	CurrentProcess(context.Context) (domain.ProcessIdentity, error)
}

package ports

import (
	"context"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

type ProcessSpec struct {
	Command    Command
	NewSession bool
	LogPath    string
}

type ProcessStatus struct {
	Identity   domain.ProcessIdentity
	Running    bool
	Executable string
	Argv       []string
	ParentPID  int
	PGID       int
	SID        int
	NetNS      string
}

type ProcessManager interface {
	Start(context.Context, ProcessSpec) (domain.ProcessIdentity, error)
	Inspect(context.Context, domain.ProcessIdentity) (ProcessStatus, error)
	InspectPID(context.Context, int) (ProcessStatus, error)
	Children(context.Context, domain.ProcessIdentity) ([]ProcessStatus, error)
	Group(context.Context, int) ([]ProcessStatus, error)
	Stop(context.Context, domain.ProcessIdentity, time.Duration) error
}

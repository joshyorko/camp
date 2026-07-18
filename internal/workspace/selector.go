package workspace

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/ports"
)

var ErrTransportUnavailable = errors.New("workspace transport is unavailable")

type Selector struct {
	local  ports.WorkspaceTransport
	remote ports.WorkspaceTransport
}

func NewSelector(local, remote ports.WorkspaceTransport) *Selector {
	return &Selector{local: local, remote: remote}
}

func (s *Selector) ReturnToStaging(ctx context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	if s == nil {
		return ports.MirrorResult{}, ErrTransportUnavailable
	}
	transport := s.remote
	if request.LocalProvider {
		transport = s.local
	}
	if transport == nil {
		return ports.MirrorResult{}, ErrTransportUnavailable
	}
	return transport.ReturnToStaging(ctx, request)
}

var _ ports.WorkspaceTransport = (*Selector)(nil)

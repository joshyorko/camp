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

type mirrorRequestValidator interface {
	Validate(ports.MirrorRequest) error
}

func NewSelector(local, remote ports.WorkspaceTransport) *Selector {
	return &Selector{local: local, remote: remote}
}

func (s *Selector) ReturnToStaging(ctx context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	if err := s.Validate(request); err != nil {
		return ports.MirrorResult{}, err
	}
	transport := s.remote
	if request.LocalProvider {
		transport = s.local
	}
	return transport.ReturnToStaging(ctx, request)
}

func (s *Selector) Validate(request ports.MirrorRequest) error {
	if s == nil {
		return ErrTransportUnavailable
	}
	transport := s.remote
	if request.LocalProvider {
		transport = s.local
	}
	if transport == nil {
		return ErrTransportUnavailable
	}
	if validator, ok := transport.(mirrorRequestValidator); ok {
		return validator.Validate(request)
	}
	return nil
}

var _ ports.WorkspaceTransport = (*Selector)(nil)

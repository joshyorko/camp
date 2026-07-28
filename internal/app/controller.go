package app

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/domain"
)

type ControllerIdentityReader interface {
	ControllerIdentity(context.Context) (domain.ControllerIdentity, error)
}

type ControllerInspector struct{ reader ControllerIdentityReader }

func NewControllerInspector(reader ControllerIdentityReader) *ControllerInspector {
	return &ControllerInspector{reader: reader}
}

func (i *ControllerInspector) Inspect(ctx context.Context) (domain.ControllerIdentity, error) {
	if i == nil || i.reader == nil {
		return domain.ControllerIdentity{}, errors.New("controller identity reader is nil")
	}
	identity, err := i.reader.ControllerIdentity(ctx)
	if err != nil {
		return domain.ControllerIdentity{}, err
	}
	if err := identity.Validate(); err != nil {
		return domain.ControllerIdentity{}, err
	}
	return identity, nil
}

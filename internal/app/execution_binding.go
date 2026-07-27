package app

import (
	"context"
	"errors"

	"github.com/joshyorko/camp/internal/domain"
)

var (
	ErrExecutionRetarget       = domain.ErrExecutionRetarget
	ErrExecutionBindingUnknown = errors.New("execution binding is unknown")
)

type ExecutionBindingStore interface {
	BindExecution(context.Context, string, domain.ExecutionBinding) error
	Binding(context.Context, string) (domain.ExecutionBinding, bool, error)
}

type ExecutionGuard struct{ store ExecutionBindingStore }

func NewExecutionGuard(store ExecutionBindingStore) *ExecutionGuard {
	return &ExecutionGuard{store: store}
}

func (g *ExecutionGuard) BeforeEffects(ctx context.Context, sessionID string, binding domain.ExecutionBinding, effects func(context.Context) error) error {
	if g == nil || g.store == nil {
		return errors.New("execution binding store is nil")
	}
	if effects == nil {
		return errors.New("execution effects are nil")
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if err := g.store.BindExecution(ctx, sessionID, binding); err != nil {
		return err
	}
	return effects(ctx)
}

func (g *ExecutionGuard) Require(ctx context.Context, sessionID string, selected domain.ExecutionBinding) error {
	if g == nil || g.store == nil {
		return errors.New("execution binding store is nil")
	}
	if err := selected.Validate(); err != nil {
		return err
	}
	recorded, found, err := g.store.Binding(ctx, sessionID)
	if err != nil {
		return err
	}
	if !found {
		return ErrExecutionBindingUnknown
	}
	if err := recorded.Validate(); err != nil {
		return err
	}
	if recorded != selected {
		return ErrExecutionRetarget
	}
	return nil
}

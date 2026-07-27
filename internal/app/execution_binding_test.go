package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestExecutionGuardPersistsBindingBeforeEffects(t *testing.T) {
	t.Parallel()
	store := &bindingStoreStub{}
	guard := NewExecutionGuard(store)
	binding := testExecutionBinding(t, "a", "b")

	err := guard.BeforeEffects(context.Background(), "session-1", binding, func(context.Context) error {
		store.events = append(store.events, "effect")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(store.events, ","); got != "bind,effect" {
		t.Fatalf("events = %q, want bind,effect", got)
	}
}

func TestExecutionGuardDoesNotRunEffectsWhenBindingFails(t *testing.T) {
	t.Parallel()
	bindErr := errors.New("binding unavailable")
	store := &bindingStoreStub{bindErr: bindErr}
	called := false
	err := NewExecutionGuard(store).BeforeEffects(context.Background(), "session-1", testExecutionBinding(t, "a", "b"), func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, bindErr) || called {
		t.Fatalf("BeforeEffects() = %v, called=%t; want binding error and no effect", err, called)
	}
}

func TestExecutionGuardRejectsRetargetAndUnknownLegacyBinding(t *testing.T) {
	t.Parallel()
	selected := testExecutionBinding(t, "a", "b")
	store := &bindingStoreStub{binding: selected, found: true}
	guard := NewExecutionGuard(store)
	if err := guard.Require(context.Background(), "session-1", selected); err != nil {
		t.Fatalf("Require(same) error = %v", err)
	}
	if err := guard.Require(context.Background(), "session-1", testExecutionBinding(t, "c", "b")); !errors.Is(err, ErrExecutionRetarget) {
		t.Fatalf("Require(different blueprint) error = %v, want ErrExecutionRetarget", err)
	}
	if err := guard.Require(context.Background(), "session-1", testExecutionBinding(t, "a", "d")); !errors.Is(err, ErrExecutionRetarget) {
		t.Fatalf("Require(different profile) error = %v, want ErrExecutionRetarget", err)
	}
	store.found = false
	if err := guard.Require(context.Background(), "legacy", selected); !errors.Is(err, ErrExecutionBindingUnknown) {
		t.Fatalf("Require(legacy) error = %v, want ErrExecutionBindingUnknown", err)
	}
}

type bindingStoreStub struct {
	binding domain.ExecutionBinding
	found   bool
	bindErr error
	events  []string
}

func (s *bindingStoreStub) BindExecution(_ context.Context, _ string, binding domain.ExecutionBinding) error {
	s.events = append(s.events, "bind")
	if s.bindErr == nil {
		s.binding, s.found = binding, true
	}
	return s.bindErr
}

func (s *bindingStoreStub) Binding(context.Context, string) (domain.ExecutionBinding, bool, error) {
	return s.binding, s.found, nil
}

func testExecutionBinding(t *testing.T, blueprint, profile string) domain.ExecutionBinding {
	t.Helper()
	binding, err := domain.NewExecutionBinding(
		domain.BlueprintRef{SchemaVersion: domain.BlueprintRefSchemaVersion, Digest: strings.Repeat(blueprint, 64)},
		strings.Repeat(profile, 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

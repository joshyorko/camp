package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestControllerInspectorReturnsControllerIdentity(t *testing.T) {
	t.Parallel()
	inspector := NewControllerInspector(controllerIdentityStub{identity: domain.ControllerIdentity{SchemaVersion: domain.ControllerIdentitySchemaVersion, Name: domain.ControllerNameCamp, Version: "v1.2.3"}})
	identity, err := inspector.Inspect(context.Background())
	if err != nil || identity.Name != "camp" {
		t.Fatalf("Inspect() = %#v, %v", identity, err)
	}
}

func TestControllerInspectorRejectsInvalidReaderValue(t *testing.T) {
	t.Parallel()
	_, err := NewControllerInspector(controllerIdentityStub{identity: domain.ControllerIdentity{}}).Inspect(context.Background())
	if !errors.Is(err, domain.ErrInvalidControllerIdentity) {
		t.Fatalf("Inspect() error = %v, want ErrInvalidControllerIdentity", err)
	}
}

type controllerIdentityStub struct{ identity domain.ControllerIdentity }

func (s controllerIdentityStub) ControllerIdentity(context.Context) (domain.ControllerIdentity, error) {
	return s.identity, nil
}

package app

import (
	"context"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestControllerInspectorReturnsControllerIdentity(t *testing.T) {
	t.Parallel()
	inspector := NewControllerInspector(controllerIdentityStub{identity: domain.ControllerIdentity{SchemaVersion: domain.ControllerIdentitySchemaVersion, Name: "camp", Version: "v1"}})
	identity, err := inspector.Inspect(context.Background())
	if err != nil || identity.Name != "camp" {
		t.Fatalf("Inspect() = %#v, %v", identity, err)
	}
}

type controllerIdentityStub struct{ identity domain.ControllerIdentity }

func (s controllerIdentityStub) ControllerIdentity(context.Context) (domain.ControllerIdentity, error) {
	return s.identity, nil
}

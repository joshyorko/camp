package app

import (
	"context"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestBlueprintInspectorBuildsCanonicalReference(t *testing.T) {
	t.Parallel()
	inspector := NewBlueprintInspector(blueprintSourceStub{blueprint: domain.CampBlueprint{SchemaVersion: domain.CampBlueprintSchemaVersion, Controller: domain.ControllerIdentity{SchemaVersion: domain.ControllerIdentitySchemaVersion, Name: "camp", Version: "v1"}, Capsule: "brain", Lineage: "main", WorkspaceEngine: "devpod"}})
	inspection, err := inspector.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Ref.Digest == "" || inspection.Blueprint.Capsule != "brain" {
		t.Fatalf("inspection = %#v", inspection)
	}
}

type blueprintSourceStub struct {
	blueprint domain.CampBlueprint
	err       error
}

func (s blueprintSourceStub) CurrentBlueprint(context.Context) (domain.CampBlueprint, error) {
	return s.blueprint, s.err
}

package app

import (
	"context"
	"errors"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
)

func TestBlueprintInspectorBuildsCanonicalReference(t *testing.T) {
	t.Parallel()
	inspector := NewBlueprintInspector(blueprintSourceStub{blueprint: validBlueprint()})
	inspection, err := inspector.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Ref.Digest == "" || inspection.Blueprint.Capsule != "brain" {
		t.Fatalf("inspection = %#v", inspection)
	}
}

func TestBlueprintInspectorRejectsInvalidSourceValue(t *testing.T) {
	t.Parallel()
	blueprint := validBlueprint()
	blueprint.SchemaVersion = 0
	_, err := NewBlueprintInspector(blueprintSourceStub{blueprint: blueprint}).Inspect(context.Background())
	if !errors.Is(err, domain.ErrInvalidBlueprint) {
		t.Fatalf("Inspect() error = %v, want ErrInvalidBlueprint", err)
	}
}

func validBlueprint() domain.CampBlueprint {
	return domain.CampBlueprint{
		SchemaVersion: domain.CampBlueprintSchemaVersion,
		Controller: domain.ControllerIdentity{
			SchemaVersion: domain.ControllerIdentitySchemaVersion,
			Name:          domain.ControllerNameCamp,
			Version:       "v1.2.3",
		},
		Capsule:         "brain",
		Lineage:         "main",
		WorkspaceEngine: domain.WorkspaceEngineDevPod,
		ToolVersions:    domain.ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.2"},
	}
}

type blueprintSourceStub struct {
	blueprint domain.CampBlueprint
	err       error
}

func (s blueprintSourceStub) CurrentBlueprint(context.Context) (domain.CampBlueprint, error) {
	return s.blueprint, s.err
}

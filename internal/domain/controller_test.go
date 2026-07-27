package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCampBlueprintCanonicalJSONIsStableAndPortable(t *testing.T) {
	t.Parallel()
	blueprint, err := NewCampBlueprint(
		ControllerIdentity{SchemaVersion: ControllerIdentitySchemaVersion, Name: ControllerNameCamp, Version: "v1.2.3"},
		"brain",
		"main",
		WorkspaceEngineDevPod,
		ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.2"},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := blueprint.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"schemaVersion":1,"controller":{"schemaVersion":1,"name":"camp","version":"v1.2.3"},"capsule":"brain","lineage":"main","workspaceEngine":"devpod","toolVersions":{"devpod":"v0.26.1","hauler":"v2.0.2"}}`
	if string(encoded) != want {
		t.Fatalf("CanonicalJSON() = %s, want %s", encoded, want)
	}
	decoded, err := DecodeCampBlueprint(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != blueprint {
		t.Fatalf("round trip = %#v, want %#v", decoded, blueprint)
	}
}

func TestCampBlueprintRejectsRuntimeAndUnboundedIdentityInputs(t *testing.T) {
	t.Parallel()
	valid := CampBlueprint{
		SchemaVersion:   CampBlueprintSchemaVersion,
		Controller:      ControllerIdentity{SchemaVersion: ControllerIdentitySchemaVersion, Name: ControllerNameCamp, Version: "v1.2.3"},
		Capsule:         "brain",
		Lineage:         "main",
		WorkspaceEngine: WorkspaceEngineDevPod,
		ToolVersions:    ToolVersions{DevPod: "v0.26.1", Hauler: "v2.0.2"},
	}
	tests := []struct {
		name   string
		mutate func(*CampBlueprint)
	}{
		{"host path", func(value *CampBlueprint) { value.Capsule = "/home/josh/brain" }},
		{"secret-shaped capsule", func(value *CampBlueprint) { value.Capsule = "access-token" }},
		{"port-shaped lineage", func(value *CampBlueprint) { value.Lineage = "port-45001" }},
		{"timestamp-shaped lineage", func(value *CampBlueprint) { value.Lineage = "2026-07-27T12:00:00Z" }},
		{"session-shaped lineage", func(value *CampBlueprint) { value.Lineage = "session-123" }},
		{"unsupported engine", func(value *CampBlueprint) { value.WorkspaceEngine = "docker" }},
		{"missing devpod version", func(value *CampBlueprint) { value.ToolVersions.DevPod = "" }},
		{"invalid hauler version", func(value *CampBlueprint) { value.ToolVersions.Hauler = "latest" }},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			value := valid
			tt.mutate(&value)
			if _, err := value.CanonicalJSON(); !errors.Is(err, ErrInvalidBlueprint) {
				t.Fatalf("CanonicalJSON() error = %v, want ErrInvalidBlueprint", err)
			}
		})
	}
}

func TestBlueprintDecodersRejectUnsupportedVersionsUnknownFieldsAndBadDigests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		decode func() error
	}{
		{"controller version", func() error {
			_, err := DecodeControllerIdentity([]byte(`{"schemaVersion":2,"name":"camp","version":"v1.2.3"}`))
			return err
		}},
		{"blueprint unknown field", func() error {
			_, err := DecodeCampBlueprint([]byte(`{"schemaVersion":1,"controller":{"schemaVersion":1,"name":"camp","version":"v1.2.3"},"capsule":"brain","lineage":"main","workspaceEngine":"devpod","toolVersions":{"devpod":"v0.26.1","hauler":"v2.0.2"},"sessionId":"host-session"}`))
			return err
		}},
		{"blueprint trailing document", func() error {
			_, err := DecodeCampBlueprint([]byte(`{"schemaVersion":1} {}`))
			return err
		}},
		{"binding version", func() error {
			_, err := DecodeExecutionBinding([]byte(`{"schemaVersion":0,"blueprint":{"schemaVersion":1,"digest":"` + strings.Repeat("a", 64) + `"}}`))
			return err
		}},
		{"uppercase digest", func() error {
			_, err := DecodeExecutionBinding([]byte(`{"schemaVersion":1,"blueprint":{"schemaVersion":1,"digest":"` + strings.Repeat("A", 64) + `"}}`))
			return err
		}},
		{"short profile digest", func() error {
			_, err := DecodeExecutionBinding([]byte(`{"schemaVersion":1,"blueprint":{"schemaVersion":1,"digest":"` + strings.Repeat("a", 64) + `"},"profileDigest":"abc"}`))
			return err
		}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if err := tt.decode(); err == nil {
				t.Fatal("decode succeeded, want error")
			}
		})
	}
}

func TestExecutionBindingRoundTripsOnlyValidatedPortableReferences(t *testing.T) {
	t.Parallel()
	binding, err := NewExecutionBinding(
		BlueprintRef{SchemaVersion: BlueprintRefSchemaVersion, Digest: strings.Repeat("a", 64)},
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeExecutionBinding(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != binding {
		t.Fatalf("decoded = %#v, want %#v", decoded, binding)
	}
}

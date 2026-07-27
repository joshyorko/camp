package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPortableBlueprintCanonicalJSONExcludesRuntimeState(t *testing.T) {
	t.Parallel()
	blueprint := CampBlueprint{
		SchemaVersion:   CampBlueprintSchemaVersion,
		Controller:      ControllerIdentity{SchemaVersion: ControllerIdentitySchemaVersion, Name: "camp", Version: "v1.2.3"},
		Capsule:         "brain",
		Lineage:         "main",
		WorkspaceEngine: "devpod",
		ToolVersions:    map[string]string{"hauler": "v2.0.2", "devpod": "v0.26.1"},
	}
	encoded, err := blueprint.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "password", "/home/", "port", "session", "createdAt"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("portable blueprint contains %q: %s", forbidden, encoded)
		}
	}
	var decoded CampBlueprint
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, blueprint) {
		t.Fatalf("round trip = %#v, want %#v", decoded, blueprint)
	}
}

func TestBlueprintRefIsStableAcrossMapOrder(t *testing.T) {
	t.Parallel()
	first := CampBlueprint{SchemaVersion: CampBlueprintSchemaVersion, Controller: ControllerIdentity{SchemaVersion: ControllerIdentitySchemaVersion, Name: "camp", Version: "v1"}, Capsule: "brain", Lineage: "main", WorkspaceEngine: "devpod", ToolVersions: map[string]string{"hauler": "v2", "devpod": "v0"}}
	second := first
	second.ToolVersions = map[string]string{"devpod": "v0", "hauler": "v2"}
	left, err := first.Ref()
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.Ref()
	if err != nil {
		t.Fatal(err)
	}
	if left != right || len(left.Digest) != 64 {
		t.Fatalf("refs = %#v and %#v", left, right)
	}
}

func TestExecutionBindingCarriesOnlyPortableReferences(t *testing.T) {
	t.Parallel()
	binding := ExecutionBinding{SchemaVersion: ExecutionBindingSchemaVersion, Blueprint: BlueprintRef{SchemaVersion: BlueprintRefSchemaVersion, Digest: strings.Repeat("a", 64)}, ProfileDigest: strings.Repeat("b", 64)}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "session") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("binding leaked runtime state: %s", encoded)
	}
}

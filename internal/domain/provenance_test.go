package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExecutionProvenanceIsIndependentlyVersionedAndNonSecret(t *testing.T) {
	t.Parallel()
	provenance := ExecutionProvenance{SchemaVersion: ExecutionProvenanceSchemaVersion, Binding: ExecutionBinding{SchemaVersion: ExecutionBindingSchemaVersion, Blueprint: BlueprintRef{SchemaVersion: BlueprintRefSchemaVersion, Digest: strings.Repeat("a", 64)}, ProfileDigest: strings.Repeat("b", 64)}, CandidateSHA256: strings.Repeat("c", 64)}
	encoded, err := json.Marshal(provenance)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "session") || strings.Contains(string(encoded), "credential") {
		t.Fatalf("provenance leaked excluded state: %s", encoded)
	}
}

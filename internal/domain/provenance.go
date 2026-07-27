package domain

const ExecutionProvenanceSchemaVersion = 1

// ExecutionProvenance connects an execution to immutable portable inputs and
// an exact build candidate without retaining runtime credentials or locations.
type ExecutionProvenance struct {
	SchemaVersion   int              `json:"schemaVersion" yaml:"schemaVersion"`
	Binding         ExecutionBinding `json:"binding" yaml:"binding"`
	CandidateSHA256 string           `json:"candidateSha256,omitempty" yaml:"candidateSha256,omitempty"`
}

package domain

import (
	"errors"
	"fmt"
)

const ExecutionProvenanceSchemaVersion = 1

var ErrInvalidExecutionProvenance = errors.New("invalid execution provenance")

// ExecutionProvenance connects an execution to immutable portable inputs and
// an exact build candidate without retaining runtime credentials or locations.
type ExecutionProvenance struct {
	SchemaVersion   int              `json:"schemaVersion" yaml:"schemaVersion"`
	Binding         ExecutionBinding `json:"binding" yaml:"binding"`
	CandidateSHA256 string           `json:"candidateSha256,omitempty" yaml:"candidateSha256,omitempty"`
}

func NewExecutionProvenance(binding ExecutionBinding, candidateSHA256 string) (ExecutionProvenance, error) {
	provenance := ExecutionProvenance{
		SchemaVersion:   ExecutionProvenanceSchemaVersion,
		Binding:         binding,
		CandidateSHA256: candidateSHA256,
	}
	if err := provenance.Validate(); err != nil {
		return ExecutionProvenance{}, err
	}
	return provenance, nil
}

func (p ExecutionProvenance) Validate() error {
	if p.SchemaVersion != ExecutionProvenanceSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidExecutionProvenance, p.SchemaVersion)
	}
	if err := p.Binding.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExecutionProvenance, err)
	}
	if p.CandidateSHA256 != "" && !validLowerSHA256(p.CandidateSHA256) {
		return fmt.Errorf("%w: candidate digest must be 64 lowercase hexadecimal characters", ErrInvalidExecutionProvenance)
	}
	return nil
}

func DecodeExecutionProvenance(encoded []byte) (ExecutionProvenance, error) {
	var provenance ExecutionProvenance
	if err := decodeExactJSON(encoded, &provenance); err != nil {
		return ExecutionProvenance{}, fmt.Errorf("%w: %v", ErrInvalidExecutionProvenance, err)
	}
	if err := provenance.Validate(); err != nil {
		return ExecutionProvenance{}, err
	}
	return provenance, nil
}

package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	CampBlueprintSchemaVersion    = 1
	BlueprintRefSchemaVersion     = 1
	ExecutionBindingSchemaVersion = 1
)

// CampBlueprint is the portable, non-secret execution shape. Runtime facts
// (paths, ports, timestamps, and session IDs) deliberately belong elsewhere.
type CampBlueprint struct {
	SchemaVersion   int                `json:"schemaVersion" yaml:"schemaVersion"`
	Controller      ControllerIdentity `json:"controller" yaml:"controller"`
	Capsule         string             `json:"capsule" yaml:"capsule"`
	Lineage         string             `json:"lineage" yaml:"lineage"`
	WorkspaceEngine string             `json:"workspaceEngine" yaml:"workspaceEngine"`
	ToolVersions    map[string]string  `json:"toolVersions,omitempty" yaml:"toolVersions,omitempty"`
}

type BlueprintRef struct {
	SchemaVersion int    `json:"schemaVersion" yaml:"schemaVersion"`
	Digest        string `json:"digest" yaml:"digest"`
}

// ExecutionBinding freezes the portable inputs selected for one execution.
type ExecutionBinding struct {
	SchemaVersion int          `json:"schemaVersion" yaml:"schemaVersion"`
	Blueprint     BlueprintRef `json:"blueprint" yaml:"blueprint"`
	ProfileDigest string       `json:"profileDigest,omitempty" yaml:"profileDigest,omitempty"`
}

func (b CampBlueprint) CanonicalJSON() ([]byte, error) { return json.Marshal(b) }

func (b CampBlueprint) Ref() (BlueprintRef, error) {
	encoded, err := b.CanonicalJSON()
	if err != nil {
		return BlueprintRef{}, err
	}
	digest := sha256.Sum256(encoded)
	return BlueprintRef{SchemaVersion: BlueprintRefSchemaVersion, Digest: hex.EncodeToString(digest[:])}, nil
}

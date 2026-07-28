package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	CampBlueprintSchemaVersion    = 1
	BlueprintRefSchemaVersion     = 1
	ExecutionBindingSchemaVersion = 1
	WorkspaceEngineDevPod         = "devpod"
)

var (
	ErrInvalidBlueprint        = errors.New("invalid camp blueprint")
	ErrInvalidBlueprintRef     = errors.New("invalid blueprint reference")
	ErrInvalidExecutionBinding = errors.New("invalid execution binding")
	ErrExecutionRetarget       = errors.New("execution binding retarget is not allowed")
)

// CampBlueprint is a closed portable, non-secret execution shape. Runtime
// facts (paths, ports, timestamps, and session IDs) deliberately belong
// elsewhere.
type CampBlueprint struct {
	SchemaVersion   int                `json:"schemaVersion" yaml:"schemaVersion"`
	Controller      ControllerIdentity `json:"controller" yaml:"controller"`
	Capsule         string             `json:"capsule" yaml:"capsule"`
	Lineage         string             `json:"lineage" yaml:"lineage"`
	WorkspaceEngine string             `json:"workspaceEngine" yaml:"workspaceEngine"`
	ToolVersions    ToolVersions       `json:"toolVersions" yaml:"toolVersions"`
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

func NewCampBlueprint(controller ControllerIdentity, capsule, lineage, workspaceEngine string, tools ToolVersions) (CampBlueprint, error) {
	blueprint := CampBlueprint{
		SchemaVersion:   CampBlueprintSchemaVersion,
		Controller:      controller,
		Capsule:         capsule,
		Lineage:         lineage,
		WorkspaceEngine: workspaceEngine,
		ToolVersions:    tools,
	}
	if err := blueprint.Validate(); err != nil {
		return CampBlueprint{}, err
	}
	return blueprint, nil
}

func (b CampBlueprint) Validate() error {
	if b.SchemaVersion != CampBlueprintSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidBlueprint, b.SchemaVersion)
	}
	if err := b.Controller.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidBlueprint, err)
	}
	if !validPortableID(b.Capsule) {
		return fmt.Errorf("%w: capsule is not a portable identifier", ErrInvalidBlueprint)
	}
	if !validPortableID(b.Lineage) {
		return fmt.Errorf("%w: lineage is not a portable identifier", ErrInvalidBlueprint)
	}
	if b.WorkspaceEngine != WorkspaceEngineDevPod {
		return fmt.Errorf("%w: unsupported workspace engine %q", ErrInvalidBlueprint, b.WorkspaceEngine)
	}
	if !semanticVersionPattern.MatchString(b.ToolVersions.DevPod) || !semanticVersionPattern.MatchString(b.ToolVersions.Hauler) {
		return fmt.Errorf("%w: tool versions must contain exact DevPod and Hauler semantic versions", ErrInvalidBlueprint)
	}
	return nil
}

func DecodeCampBlueprint(encoded []byte) (CampBlueprint, error) {
	var blueprint CampBlueprint
	if err := decodeExactJSON(encoded, &blueprint); err != nil {
		return CampBlueprint{}, fmt.Errorf("%w: %v", ErrInvalidBlueprint, err)
	}
	if err := blueprint.Validate(); err != nil {
		return CampBlueprint{}, err
	}
	return blueprint, nil
}

func (b CampBlueprint) CanonicalJSON() ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(b)
}

func (b CampBlueprint) Ref() (BlueprintRef, error) {
	encoded, err := b.CanonicalJSON()
	if err != nil {
		return BlueprintRef{}, err
	}
	digest := sha256.Sum256(encoded)
	return BlueprintRef{SchemaVersion: BlueprintRefSchemaVersion, Digest: hex.EncodeToString(digest[:])}, nil
}

func (r BlueprintRef) Validate() error {
	if r.SchemaVersion != BlueprintRefSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidBlueprintRef, r.SchemaVersion)
	}
	if !validLowerSHA256(r.Digest) {
		return fmt.Errorf("%w: digest must be 64 lowercase hexadecimal characters", ErrInvalidBlueprintRef)
	}
	return nil
}

func DecodeBlueprintRef(encoded []byte) (BlueprintRef, error) {
	var ref BlueprintRef
	if err := decodeExactJSON(encoded, &ref); err != nil {
		return BlueprintRef{}, fmt.Errorf("%w: %v", ErrInvalidBlueprintRef, err)
	}
	if err := ref.Validate(); err != nil {
		return BlueprintRef{}, err
	}
	return ref, nil
}

func NewExecutionBinding(blueprint BlueprintRef, profileDigest string) (ExecutionBinding, error) {
	binding := ExecutionBinding{
		SchemaVersion: ExecutionBindingSchemaVersion,
		Blueprint:     blueprint,
		ProfileDigest: profileDigest,
	}
	if err := binding.Validate(); err != nil {
		return ExecutionBinding{}, err
	}
	return binding, nil
}

func (b ExecutionBinding) Validate() error {
	if b.SchemaVersion != ExecutionBindingSchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidExecutionBinding, b.SchemaVersion)
	}
	if err := b.Blueprint.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExecutionBinding, err)
	}
	if b.ProfileDigest != "" && !validLowerSHA256(b.ProfileDigest) {
		return fmt.Errorf("%w: profile digest must be 64 lowercase hexadecimal characters", ErrInvalidExecutionBinding)
	}
	return nil
}

func DecodeExecutionBinding(encoded []byte) (ExecutionBinding, error) {
	var binding ExecutionBinding
	if err := decodeExactJSON(encoded, &binding); err != nil {
		return ExecutionBinding{}, fmt.Errorf("%w: %v", ErrInvalidExecutionBinding, err)
	}
	if err := binding.Validate(); err != nil {
		return ExecutionBinding{}, err
	}
	return binding, nil
}

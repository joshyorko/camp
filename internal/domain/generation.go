package domain

import "time"

type GenerationRef struct {
	Generation    uint64 `json:"generation" yaml:"generation"`
	ArchiveSHA256 string `json:"archiveSHA256" yaml:"archiveSHA256"`
}

type ToolVersions struct {
	DevPod string `json:"devpod" yaml:"devpod"`
	Hauler string `json:"hauler" yaml:"hauler"`
}

type LatestPointer struct {
	SchemaVersion int            `json:"schemaVersion" yaml:"schemaVersion"`
	Capsule       string         `json:"capsule" yaml:"capsule"`
	Lineage       Lineage        `json:"lineage" yaml:"lineage"`
	Generation    GenerationRef  `json:"generation" yaml:"generation"`
	Parent        *GenerationRef `json:"parent,omitempty" yaml:"parent,omitempty"`
	ObjectKey     string         `json:"objectKey" yaml:"objectKey"`
	Size          int64          `json:"size" yaml:"size"`
	CreatedAt     time.Time      `json:"createdAt" yaml:"createdAt"`
	Tools         ToolVersions   `json:"tools" yaml:"tools"`
	SessionID     string         `json:"sessionId" yaml:"sessionId"`
}

type GenerationMetadata struct {
	SchemaVersion int            `json:"schemaVersion" yaml:"schemaVersion"`
	Capsule       string         `json:"capsule" yaml:"capsule"`
	Lineage       Lineage        `json:"lineage" yaml:"lineage"`
	Generation    GenerationRef  `json:"generation" yaml:"generation"`
	Parent        *GenerationRef `json:"parent,omitempty" yaml:"parent,omitempty"`
	ObjectKey     string         `json:"objectKey" yaml:"objectKey"`
	MetadataKey   string         `json:"metadataKey" yaml:"metadataKey"`
	Size          int64          `json:"size" yaml:"size"`
	CreatedAt     time.Time      `json:"createdAt" yaml:"createdAt"`
	Tools         ToolVersions   `json:"tools" yaml:"tools"`
	SessionID     string         `json:"sessionId" yaml:"sessionId"`
	Verified      Verification   `json:"verified" yaml:"verified"`
}

type Verification struct {
	LocalHaulLoadable   bool `json:"localHaulLoadable" yaml:"localHaulLoadable"`
	RemoteBytesVerified bool `json:"remoteBytesVerified" yaml:"remoteBytesVerified"`
}

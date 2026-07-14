package domain

import "time"

type WriterLease struct {
	SchemaVersion    int            `json:"schemaVersion" yaml:"schemaVersion"`
	Capsule          string         `json:"capsule" yaml:"capsule"`
	Lineage          Lineage        `json:"lineage" yaml:"lineage"`
	SessionID        string         `json:"sessionId" yaml:"sessionId"`
	Machine          string         `json:"machine" yaml:"machine"`
	OpenedGeneration *GenerationRef `json:"openedGeneration,omitempty" yaml:"openedGeneration,omitempty"`
	CreatedAt        time.Time      `json:"createdAt" yaml:"createdAt"`
	HeartbeatAt      time.Time      `json:"heartbeatAt" yaml:"heartbeatAt"`
	ExpiresAt        time.Time      `json:"expiresAt" yaml:"expiresAt"`
}

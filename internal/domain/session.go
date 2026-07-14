package domain

import "time"

type SessionMode string

const (
	SessionReadWrite SessionMode = "readWrite"
	SessionReadOnly  SessionMode = "readOnly"
)

type SessionState string

const (
	SessionOpening    SessionState = "opening"
	SessionOpen       SessionState = "open"
	SessionRecovering SessionState = "recovering"
	SessionClosed     SessionState = "closed"
)

type JournalSnapshot struct {
	SchemaVersion           int             `json:"schemaVersion" yaml:"schemaVersion"`
	SessionID               string          `json:"sessionId" yaml:"sessionId"`
	Capsule                 string          `json:"capsule" yaml:"capsule"`
	Lineage                 Lineage         `json:"lineage" yaml:"lineage"`
	Mode                    SessionMode     `json:"mode" yaml:"mode"`
	OpenedGeneration        *GenerationRef  `json:"openedGeneration,omitempty" yaml:"openedGeneration,omitempty"`
	CurrentBase             *GenerationRef  `json:"currentBase,omitempty" yaml:"currentBase,omitempty"`
	ExpectedPointerRevision string          `json:"expectedPointerRevision,omitempty" yaml:"expectedPointerRevision,omitempty"`
	State                   SessionState    `json:"state" yaml:"state"`
	Materialization         Materialization `json:"materialization" yaml:"materialization"`
	Checkpoint              Checkpoint      `json:"checkpoint" yaml:"checkpoint"`
	Cleanup                 Cleanup         `json:"cleanup" yaml:"cleanup"`
	CreatedAt               time.Time       `json:"createdAt" yaml:"createdAt"`
	UpdatedAt               time.Time       `json:"updatedAt" yaml:"updatedAt"`
}

package domain

type CheckpointState string

const (
	CheckpointNone      CheckpointState = "none"
	CheckpointBuilding  CheckpointState = "building"
	CheckpointVerified  CheckpointState = "verified"
	CheckpointUploaded  CheckpointState = "uploaded"
	CheckpointPublished CheckpointState = "published"
)

type Checkpoint struct {
	State                CheckpointState `json:"state" yaml:"state"`
	Generation           *GenerationRef  `json:"generation,omitempty" yaml:"generation,omitempty"`
	LocalHaulPath        string          `json:"localHaulPath,omitempty" yaml:"localHaulPath,omitempty"`
	ObjectKey            string          `json:"objectKey,omitempty" yaml:"objectKey,omitempty"`
	PublicationSucceeded bool            `json:"publicationSucceeded" yaml:"publicationSucceeded"`
}

type CleanupState string

const (
	CleanupPending   CleanupState = "pending"
	CleanupRunning   CleanupState = "running"
	CleanupSucceeded CleanupState = "succeeded"
	CleanupFailed    CleanupState = "failed"
)

type Cleanup struct {
	State   CleanupState `json:"state" yaml:"state"`
	LastErr string       `json:"lastError,omitempty" yaml:"lastError,omitempty"`
}

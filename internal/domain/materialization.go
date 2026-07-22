package domain

type MaterializationMode string

const (
	MaterializationCreated MaterializationMode = "created"
	MaterializationAdopted MaterializationMode = "adopted"
)

type Materialization struct {
	SchemaVersion    int                 `json:"schemaVersion" yaml:"schemaVersion"`
	CanonicalPath    string              `json:"canonicalPath" yaml:"canonicalPath"`
	OriginalPath     string              `json:"originalPath" yaml:"originalPath"`
	OwnershipMarker  string              `json:"ownershipMarker" yaml:"ownershipMarker"`
	Mode             MaterializationMode `json:"mode" yaml:"mode"`
	Device           uint64              `json:"device,omitempty" yaml:"device,omitempty"`
	Inode            uint64              `json:"inode,omitempty" yaml:"inode,omitempty"`
	BirthTimeNS      int64               `json:"birthTimeNs,omitempty" yaml:"birthTimeNs,omitempty"`
	CleanupPermitted bool                `json:"cleanupPermitted" yaml:"cleanupPermitted"`
}

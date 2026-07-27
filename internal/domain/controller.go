package domain

const ControllerIdentitySchemaVersion = 1

// ControllerIdentity identifies the Camp binary contract that produced a
// portable blueprint. It intentionally contains neither a host identity nor
// executable path.
type ControllerIdentity struct {
	SchemaVersion int    `json:"schemaVersion" yaml:"schemaVersion"`
	Name          string `json:"name" yaml:"name"`
	Version       string `json:"version" yaml:"version"`
}

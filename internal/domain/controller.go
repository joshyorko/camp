package domain

import (
	"errors"
	"fmt"
)

const ControllerIdentitySchemaVersion = 1
const ControllerNameCamp = "camp"

var ErrInvalidControllerIdentity = errors.New("invalid controller identity")

// ControllerIdentity identifies the Camp binary contract that produced a
// portable blueprint. It intentionally contains neither a host identity nor
// executable path.
type ControllerIdentity struct {
	SchemaVersion int    `json:"schemaVersion" yaml:"schemaVersion"`
	Name          string `json:"name" yaml:"name"`
	Version       string `json:"version" yaml:"version"`
}

func NewControllerIdentity(version string) (ControllerIdentity, error) {
	identity := ControllerIdentity{
		SchemaVersion: ControllerIdentitySchemaVersion,
		Name:          ControllerNameCamp,
		Version:       version,
	}
	if err := identity.Validate(); err != nil {
		return ControllerIdentity{}, err
	}
	return identity, nil
}

func (i ControllerIdentity) Validate() error {
	if i.SchemaVersion != ControllerIdentitySchemaVersion {
		return fmt.Errorf("%w: unsupported schema version %d", ErrInvalidControllerIdentity, i.SchemaVersion)
	}
	if i.Name != ControllerNameCamp {
		return fmt.Errorf("%w: unsupported controller %q", ErrInvalidControllerIdentity, i.Name)
	}
	if !semanticVersionPattern.MatchString(i.Version) {
		return fmt.Errorf("%w: version must be an exact v-prefixed semantic version", ErrInvalidControllerIdentity)
	}
	return nil
}

func DecodeControllerIdentity(encoded []byte) (ControllerIdentity, error) {
	var identity ControllerIdentity
	if err := decodeExactJSON(encoded, &identity); err != nil {
		return ControllerIdentity{}, fmt.Errorf("%w: %v", ErrInvalidControllerIdentity, err)
	}
	if err := identity.Validate(); err != nil {
		return ControllerIdentity{}, err
	}
	return identity, nil
}

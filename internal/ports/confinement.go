package ports

import "context"

type ConfinementCapability struct {
	Executable             string   `json:"executable" yaml:"executable"`
	Version                string   `json:"version" yaml:"version"`
	EnvironmentFingerprint string   `json:"environmentFingerprint" yaml:"environmentFingerprint"`
	Boundary               string   `json:"boundary" yaml:"boundary"`
	ChildContextPrefix     []string `json:"childContextPrefix,omitempty" yaml:"childContextPrefix,omitempty"`
}

type ConfinementResolver interface {
	Resolve(context.Context) (ConfinementCapability, error)
}

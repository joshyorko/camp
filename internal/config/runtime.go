package config

import (
	"fmt"
	"strconv"
)

type CapsuleValues struct {
	RegistryPort     int    `json:"registryPort,omitempty" yaml:"registryPort,omitempty"`
	FileserverPort   int    `json:"fileserverPort,omitempty" yaml:"fileserverPort,omitempty"`
	DevcontainerPath string `json:"devcontainerPath,omitempty" yaml:"devcontainerPath,omitempty"`
}

type RuntimeInput struct {
	Bootstrap   Bootstrap
	Capsule     CapsuleValues
	Environment map[string]string
	Flags       Overrides
}

type Runtime struct {
	Bootstrap
	DevcontainerPath string `json:"devcontainerPath,omitempty" yaml:"devcontainerPath,omitempty"`
}

func ResolveRuntime(input RuntimeInput) (Runtime, error) {
	result := Runtime{Bootstrap: input.Bootstrap}
	if input.Capsule.RegistryPort != 0 {
		result.RegistryPort = input.Capsule.RegistryPort
	}
	if input.Capsule.FileserverPort != 0 {
		result.FileserverPort = input.Capsule.FileserverPort
	}
	result.DevcontainerPath = input.Capsule.DevcontainerPath
	if value, ok := environmentValue(input.Environment, "CAMP_REGISTRY_PORT"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Runtime{}, fmt.Errorf("CAMP_REGISTRY_PORT: %w", err)
		}
		result.RegistryPort = parsed
	}
	if value, ok := environmentValue(input.Environment, "CAMP_FILESERVER_PORT"); ok {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return Runtime{}, fmt.Errorf("CAMP_FILESERVER_PORT: %w", err)
		}
		result.FileserverPort = parsed
	}
	if value, ok := environmentValue(input.Environment, "CAMP_DEVCONTAINER_PATH"); ok {
		result.DevcontainerPath = value
	}
	applyBootstrapFlags(&result.Bootstrap, input.Flags)
	if input.Flags.DevcontainerPath != nil {
		result.DevcontainerPath = *input.Flags.DevcontainerPath
	}
	if err := validatePort(result.RegistryPort); err != nil {
		return Runtime{}, err
	}
	if err := validatePort(result.FileserverPort); err != nil {
		return Runtime{}, err
	}
	return result, nil
}

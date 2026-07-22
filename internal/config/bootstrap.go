package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Overrides struct {
	Capsule          *string
	Backend          *string
	Source           *string
	RegistryPort     *int
	FileserverPort   *int
	DevcontainerPath *string
	S3Endpoint       *string
	S3Region         *string
	S3PathStyle      *bool
	S3Insecure       *bool
}

type BootstrapInput struct {
	ConfigPath  string
	Environment map[string]string
	Flags       Overrides
}

type Bootstrap struct {
	Capsule        string   `json:"capsule" yaml:"capsule"`
	Backend        string   `json:"backend" yaml:"backend"`
	Source         string   `json:"source,omitempty" yaml:"source,omitempty"`
	DevPodProvider string   `json:"devpodProvider,omitempty" yaml:"devpodProvider,omitempty"`
	RegistryPort   int      `json:"registryPort" yaml:"registryPort"`
	FileserverPort int      `json:"fileserverPort" yaml:"fileserverPort"`
	AccessToken    string   `json:"accessToken,omitempty" yaml:"accessToken,omitempty"`
	S3             S3Values `json:"s3,omitempty" yaml:"s3,omitempty"`
}

type userConfig struct {
	DefaultCapsule string   `yaml:"defaultCapsule"`
	Backend        string   `yaml:"backend"`
	Source         string   `yaml:"source"`
	DevPodProvider string   `yaml:"devpodProvider"`
	RegistryPort   int      `yaml:"registryPort"`
	FileserverPort int      `yaml:"fileserverPort"`
	S3             S3Values `yaml:"s3"`
}

func ResolveBootstrap(input BootstrapInput) (Bootstrap, error) {
	config := Bootstrap{Capsule: "default", RegistryPort: 5000, FileserverPort: 8080}
	path := input.ConfigPath
	if path == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return Bootstrap{}, fmt.Errorf("resolve XDG config home: %w", err)
		}
		path = filepath.Join(root, "camp", "config.yaml")
	}
	user, _, err := readUserConfig(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Bootstrap{}, err
	}
	if err == nil {
		applyUser(&config, user)
	}
	applyBootstrapEnvironment(&config, input.Environment)
	applyBootstrapFlags(&config, input.Flags)
	if config.Capsule == "" {
		return Bootstrap{}, errors.New("effective capsule is empty")
	}
	if err := ValidateDevPodProvider(config.DevPodProvider); err != nil {
		return Bootstrap{}, err
	}
	if err := validatePort(config.RegistryPort); err != nil {
		return Bootstrap{}, fmt.Errorf("registry port: %w", err)
	}
	if err := validatePort(config.FileserverPort); err != nil {
		return Bootstrap{}, fmt.Errorf("fileserver port: %w", err)
	}
	return config, nil
}

func readUserConfig(path string) (userConfig, os.FileInfo, error) {
	file, err := os.Open(path)
	if err != nil {
		return userConfig{}, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return userConfig{}, nil, err
	}
	if !info.Mode().IsRegular() {
		return userConfig{}, nil, errors.New("host config is not a regular file")
	}
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var config userConfig
	if err := decoder.Decode(&config); err != nil {
		return userConfig{}, nil, fmt.Errorf("decode host config: %w", err)
	}
	return config, info, nil
}

func applyUser(config *Bootstrap, user userConfig) {
	if user.DefaultCapsule != "" {
		config.Capsule = user.DefaultCapsule
	}
	if user.Backend != "" {
		config.Backend = user.Backend
	}
	if user.Source != "" {
		config.Source = user.Source
	}
	if user.DevPodProvider != "" {
		config.DevPodProvider = user.DevPodProvider
	}
	if user.RegistryPort != 0 {
		config.RegistryPort = user.RegistryPort
	}
	if user.FileserverPort != 0 {
		config.FileserverPort = user.FileserverPort
	}
	config.S3 = user.S3
}

func applyBootstrapEnvironment(config *Bootstrap, environment map[string]string) {
	if value, ok := environmentValue(environment, "CAMP_CAPSULE"); ok {
		config.Capsule = value
	}
	if value, ok := environmentValue(environment, "CAMP_BACKEND"); ok {
		config.Backend = value
	}
	if value, ok := environmentValue(environment, "CAMP_SOURCE"); ok {
		config.Source = value
	}
	if value, ok := environmentValue(environment, "CAMP_DEVPOD_PROVIDER"); ok {
		config.DevPodProvider = value
	}
	if value, ok := environmentValue(environment, "CAMP_ACCESS_TOKEN"); ok {
		config.AccessToken = value
	}
	if value, ok := environmentValue(environment, "CAMP_S3_ENDPOINT"); ok {
		config.S3.Endpoint = value
	}
	if value, ok := environmentValue(environment, "CAMP_S3_REGION"); ok {
		config.S3.Region = value
	}
	if value, ok := environmentValue(environment, "CAMP_S3_PATH_STYLE"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			config.S3.PathStyle = parsed
		}
	}
	if value, ok := environmentValue(environment, "CAMP_S3_INSECURE"); ok {
		if parsed, err := strconv.ParseBool(value); err == nil {
			config.S3.Insecure = parsed
		}
	}
	if value, ok := environmentValue(environment, "CAMP_REGISTRY_PORT"); ok {
		if parsed, err := strconv.Atoi(value); err == nil {
			config.RegistryPort = parsed
		}
	}
	if value, ok := environmentValue(environment, "CAMP_FILESERVER_PORT"); ok {
		if parsed, err := strconv.Atoi(value); err == nil {
			config.FileserverPort = parsed
		}
	}
}

func applyBootstrapFlags(config *Bootstrap, flags Overrides) {
	if flags.Capsule != nil {
		config.Capsule = *flags.Capsule
	}
	if flags.Backend != nil {
		config.Backend = *flags.Backend
	}
	if flags.Source != nil {
		config.Source = *flags.Source
	}
	if flags.RegistryPort != nil {
		config.RegistryPort = *flags.RegistryPort
	}
	if flags.FileserverPort != nil {
		config.FileserverPort = *flags.FileserverPort
	}
	if flags.S3Endpoint != nil {
		config.S3.Endpoint = *flags.S3Endpoint
	}
	if flags.S3Region != nil {
		config.S3.Region = *flags.S3Region
	}
	if flags.S3PathStyle != nil {
		config.S3.PathStyle = *flags.S3PathStyle
	}
	if flags.S3Insecure != nil {
		config.S3.Insecure = *flags.S3Insecure
	}
}

func environmentValue(environment map[string]string, key string) (string, bool) {
	if environment != nil {
		value, ok := environment[key]
		return strings.TrimSpace(value), ok
	}
	value, ok := os.LookupEnv(key)
	return strings.TrimSpace(value), ok
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%d is outside 1..65535", port)
	}
	return nil
}

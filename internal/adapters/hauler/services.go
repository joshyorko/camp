package hauler

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

const (
	RegistryServiceName     = "registry"
	FileserverServiceName   = "fileserver"
	RegistryReadinessPath   = "/v2/"
	FileserverReadinessPath = "/"
)

var ErrInvalidServiceDefinition = errors.New("invalid Hauler service definition")

// ServiceDefinition binds the exact Hauler service command to the runtime
// facts that the supervisor must validate before exposing it.
type ServiceDefinition struct {
	Name             string
	Subcommand       string
	HaulerExecutable string
	StoreDirectory   string
	OverlayDirectory string
	GuestPort        int
	ReadinessPath    string
	LogPath          string
	PIDPath          string
	ReadOnly         bool
	TimeoutSeconds   int
}

type ServiceDefinitionOptions struct {
	HaulerExecutable string
	StoreDirectory   string
	OverlayDirectory string
	GuestPort        int
	ReadinessPath    string
	LogPath          string
	PIDPath          string
	ReadOnly         bool
	TimeoutSeconds   int
}

func NewRegistryServiceDefinition(options ServiceDefinitionOptions) (ServiceDefinition, error) {
	definition := ServiceDefinition{
		Name:             RegistryServiceName,
		Subcommand:       RegistryServiceName,
		HaulerExecutable: options.HaulerExecutable,
		StoreDirectory:   options.StoreDirectory,
		OverlayDirectory: options.OverlayDirectory,
		GuestPort:        options.GuestPort,
		ReadinessPath:    options.ReadinessPath,
		LogPath:          options.LogPath,
		PIDPath:          options.PIDPath,
		ReadOnly:         options.ReadOnly,
		TimeoutSeconds:   options.TimeoutSeconds,
	}
	if definition.ReadinessPath == "" {
		definition.ReadinessPath = RegistryReadinessPath
	}
	return definition, definition.validate(true)
}

func NewFileserverServiceDefinition(options ServiceDefinitionOptions) (ServiceDefinition, error) {
	definition := ServiceDefinition{
		Name:             FileserverServiceName,
		Subcommand:       FileserverServiceName,
		HaulerExecutable: options.HaulerExecutable,
		StoreDirectory:   options.StoreDirectory,
		OverlayDirectory: options.OverlayDirectory,
		GuestPort:        options.GuestPort,
		ReadinessPath:    options.ReadinessPath,
		LogPath:          options.LogPath,
		PIDPath:          options.PIDPath,
		ReadOnly:         options.ReadOnly,
		TimeoutSeconds:   options.TimeoutSeconds,
	}
	if definition.ReadinessPath == "" {
		definition.ReadinessPath = FileserverReadinessPath
	}
	if definition.TimeoutSeconds == 0 {
		definition.TimeoutSeconds = 60
	}
	return definition, definition.validate(true)
}

func (d ServiceDefinition) Command() (ports.Command, error) {
	if err := d.validate(true); err != nil {
		return ports.Command{}, err
	}
	return d.command(), nil
}

func (d ServiceDefinition) command() ports.Command {
	argv := []string{"store", "--store", d.StoreDirectory, "serve", d.Subcommand, "--directory", d.OverlayDirectory, "--port", strconv.Itoa(d.GuestPort)}
	if d.Name == RegistryServiceName {
		argv = append(argv, "--readonly="+strconv.FormatBool(d.ReadOnly))
	} else {
		argv = append(argv, "--timeout", strconv.Itoa(d.TimeoutSeconds))
	}
	return ports.Command{Executable: d.HaulerExecutable, Argv: argv}
}

func (d ServiceDefinition) validate(requireAbsoluteExecutable bool) error {
	if d.Name != RegistryServiceName && d.Name != FileserverServiceName {
		return fmt.Errorf("%w: unsupported service name %q", ErrInvalidServiceDefinition, d.Name)
	}
	if d.Subcommand != d.Name {
		return fmt.Errorf("%w: service name %q does not match subcommand %q", ErrInvalidServiceDefinition, d.Name, d.Subcommand)
	}
	if d.HaulerExecutable == "" || (requireAbsoluteExecutable && !filepath.IsAbs(d.HaulerExecutable)) {
		return fmt.Errorf("%w: Hauler executable must be absolute", ErrInvalidServiceDefinition)
	}
	if err := absoluteCleanDirectory("store", d.StoreDirectory); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidServiceDefinition, err)
	}
	if err := absoluteCleanDirectory("overlay", d.OverlayDirectory); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidServiceDefinition, err)
	}
	if d.StoreDirectory == d.OverlayDirectory {
		return fmt.Errorf("%w: store and overlay directories must differ", ErrInvalidServiceDefinition)
	}
	if d.GuestPort < 1 || d.GuestPort > 65535 {
		return fmt.Errorf("%w: guest port %d is outside 1..65535", ErrInvalidServiceDefinition, d.GuestPort)
	}
	wantPath := FileserverReadinessPath
	if d.Name == RegistryServiceName {
		wantPath = RegistryReadinessPath
	}
	if d.ReadinessPath != wantPath {
		return fmt.Errorf("%w: service %q requires readiness path %q", ErrInvalidServiceDefinition, d.Name, wantPath)
	}
	if err := privatePath("log", d.LogPath); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidServiceDefinition, err)
	}
	if err := privatePath("pid", d.PIDPath); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidServiceDefinition, err)
	}
	if d.LogPath == d.PIDPath {
		return fmt.Errorf("%w: log and pid paths must differ", ErrInvalidServiceDefinition)
	}
	if d.Name == FileserverServiceName && (d.TimeoutSeconds < 1 || d.TimeoutSeconds > 24*60*60) {
		return fmt.Errorf("%w: fileserver timeout is outside 1..86400", ErrInvalidServiceDefinition)
	}
	return nil
}

func absoluteCleanDirectory(label, value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return fmt.Errorf("%s directory must be absolute and clean", label)
	}
	return nil
}

func privatePath(label, value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value || value == string(filepath.Separator) {
		return fmt.Errorf("%s path must be absolute, clean, and private", label)
	}
	return nil
}

func serviceDefinitionForClient(executable, store, directory string, port int, logPath, pidPath string, name string, readOnly bool, timeout int) ServiceDefinition {
	readiness := FileserverReadinessPath
	if name == RegistryServiceName {
		readiness = RegistryReadinessPath
	}
	return ServiceDefinition{
		Name: name, Subcommand: name, HaulerExecutable: executable,
		StoreDirectory: store, OverlayDirectory: directory, GuestPort: port,
		ReadinessPath: readiness, LogPath: logPath, PIDPath: pidPath,
		ReadOnly: readOnly, TimeoutSeconds: timeout,
	}
}

func (d ServiceDefinition) String() string {
	return strings.Join([]string{d.Name, d.Subcommand, d.HaulerExecutable, d.StoreDirectory, d.OverlayDirectory}, " ")
}

package hauler

import (
	"reflect"
	"testing"
)

func TestExactHaulerV201ServiceArgvTypedDefinition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		definition func() (ServiceDefinition, error)
		wantName   string
		wantPath   string
		wantArgv   []string
	}{
		{
			name: "registry",
			definition: func() (ServiceDefinition, error) {
				return NewRegistryServiceDefinition(ServiceDefinitionOptions{
					HaulerExecutable: "/opt/hauler",
					StoreDirectory:   "/state/store",
					OverlayDirectory: "/state/registry",
					GuestPort:        5000,
					ReadinessPath:    "/v2/",
					LogPath:          "/state/private/registry.log",
					PIDPath:          "/state/private/registry.pid",
					ReadOnly:         true,
				})
			},
			wantName: "registry", wantPath: "/v2/",
			wantArgv: []string{"store", "--store", "/state/store", "serve", "registry", "--directory", "/state/registry", "--port", "5000", "--readonly=true"},
		},
		{
			name: "fileserver",
			definition: func() (ServiceDefinition, error) {
				return NewFileserverServiceDefinition(ServiceDefinitionOptions{
					HaulerExecutable: "/opt/hauler",
					StoreDirectory:   "/state/store",
					OverlayDirectory: "/state/files",
					GuestPort:        8080,
					ReadinessPath:    "/",
					LogPath:          "/state/private/fileserver.log",
					PIDPath:          "/state/private/fileserver.pid",
					TimeoutSeconds:   90,
				})
			},
			wantName: "fileserver", wantPath: "/",
			wantArgv: []string{"store", "--store", "/state/store", "serve", "fileserver", "--directory", "/state/files", "--port", "8080", "--timeout", "90"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition, err := tt.definition()
			if err != nil {
				t.Fatalf("definition error = %v", err)
			}
			if definition.Name != tt.wantName || definition.Subcommand != tt.wantName || definition.ReadinessPath != tt.wantPath {
				t.Fatalf("definition identity = %#v", definition)
			}
			if definition.HaulerExecutable != "/opt/hauler" || definition.StoreDirectory == "" || definition.OverlayDirectory == "" || definition.GuestPort == 0 || definition.LogPath == "" || definition.PIDPath == "" {
				t.Fatalf("definition contract = %#v", definition)
			}
			command, err := definition.Command()
			if err != nil {
				t.Fatalf("Command() error = %v", err)
			}
			if command.Executable != "/opt/hauler" || !reflect.DeepEqual(command.Argv, tt.wantArgv) {
				t.Fatalf("command = %#v, want executable /opt/hauler argv %#v", command, tt.wantArgv)
			}
		})
	}
}

func TestExactHaulerV201ServiceArgvTypedDefinitionRejectsUnsafePathsAndShape(t *testing.T) {
	t.Parallel()
	base := ServiceDefinitionOptions{
		HaulerExecutable: "/opt/hauler",
		StoreDirectory:   "/state/store",
		OverlayDirectory: "/state/registry",
		GuestPort:        5000,
		LogPath:          "/state/private/registry.log",
		PIDPath:          "/state/private/registry.pid",
	}
	for _, mutate := range []func(*ServiceDefinitionOptions){
		func(options *ServiceDefinitionOptions) { options.HaulerExecutable = "hauler" },
		func(options *ServiceDefinitionOptions) { options.StoreDirectory = "relative/store" },
		func(options *ServiceDefinitionOptions) { options.OverlayDirectory = "/state/private/../registry" },
		func(options *ServiceDefinitionOptions) { options.GuestPort = 0 },
		func(options *ServiceDefinitionOptions) { options.LogPath = options.PIDPath },
		func(options *ServiceDefinitionOptions) { options.PIDPath = "relative.pid" },
	} {
		options := base
		mutate(&options)
		if _, err := NewRegistryServiceDefinition(options); err == nil {
			t.Fatalf("NewRegistryServiceDefinition(%#v) accepted unsafe shape", options)
		}
	}
}

package config

import "testing"

func TestRuntimeUsesFlagsThenEnvironmentThenCapsuleThenUser(t *testing.T) {
	t.Parallel()
	bootstrap := Bootstrap{Capsule: "brain", Backend: "file:///user", RegistryPort: 5000, FileserverPort: 8080}
	runtime, err := ResolveRuntime(RuntimeInput{
		Bootstrap:   bootstrap,
		Capsule:     CapsuleValues{RegistryPort: 5100, FileserverPort: 8100, DevcontainerPath: ".devcontainer/devcontainer.json"},
		Environment: map[string]string{"CAMP_REGISTRY_PORT": "5200"},
		Flags:       Overrides{FileserverPort: intPtr(8200)},
	})
	if err != nil {
		t.Fatalf("ResolveRuntime() error = %v", err)
	}
	if runtime.RegistryPort != 5200 || runtime.FileserverPort != 8200 || runtime.DevcontainerPath != ".devcontainer/devcontainer.json" || runtime.Backend != bootstrap.Backend {
		t.Fatalf("runtime = %#v", runtime)
	}
}

func intPtr(value int) *int { return &value }

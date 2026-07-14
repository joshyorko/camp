package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBootstrapPrecedenceFlagsEnvironmentUserDefaultsAndSecretMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := []byte("defaultCapsule: user-capsule\nbackend: file:///user/backend\nsource: /user/source\naccessToken: user-secret\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveBootstrap(BootstrapInput{ConfigPath: path})
	if err == nil {
		t.Fatal("ResolveBootstrap() accepted world-readable secret config")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := ResolveBootstrap(BootstrapInput{
		ConfigPath: path,
		Environment: map[string]string{
			"CAMP_CAPSULE": "env-capsule",
			"CAMP_BACKEND": "file:///env/backend",
		},
		Flags: Overrides{Capsule: ptr("flag-capsule")},
	})
	if err != nil {
		t.Fatalf("ResolveBootstrap() error = %v", err)
	}
	if result.Capsule != "flag-capsule" || result.Backend != "file:///env/backend" || result.Source != "/user/source" || result.AccessToken != "user-secret" {
		t.Fatalf("bootstrap = %#v", result)
	}
}

func ptr(value string) *string { return &value }

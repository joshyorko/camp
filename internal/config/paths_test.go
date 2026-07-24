package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveXDGPathsUsesDisjointConventionalLocations(t *testing.T) {
	t.Parallel()
	paths, err := ResolveXDGPaths(XDGInput{Home: "/home/josh", Environment: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if paths.ConfigPath != "/home/josh/.config/camp/config.yaml" ||
		paths.DataRoot != "/home/josh/.local/share/camp" ||
		paths.WorkRoot != "/home/josh/.local/share/camp/work" ||
		paths.StoreRoot != "/home/josh/.local/share/camp/stores" ||
		paths.SessionRoot != "/home/josh/.local/share/camp/sessions" ||
		paths.CacheRoot != "/home/josh/.cache/camp" {
		t.Fatalf("default XDG paths = %#v", paths)
	}

	overridden, err := ResolveXDGPaths(XDGInput{Home: "/home/josh", Environment: map[string]string{
		"XDG_CONFIG_HOME": "/config", "XDG_DATA_HOME": "/data", "XDG_CACHE_HOME": "/cache", "XDG_RUNTIME_DIR": "/run/user/1000",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.ConfigPath != "/config/camp/config.yaml" || overridden.WorkRoot != "/data/camp/work" || overridden.CacheRoot != "/cache/camp" || overridden.RuntimeRoot != "/run/user/1000/camp" {
		t.Fatalf("overridden XDG paths = %#v", overridden)
	}

	for name, input := range map[string]XDGInput{
		"relative override": {Home: "/home/josh", Environment: map[string]string{"XDG_DATA_HOME": "relative"}},
		"overlapping roots": {Home: "/home/josh", Environment: map[string]string{"XDG_CONFIG_HOME": "/shared", "XDG_DATA_HOME": "/shared"}},
		"root data home":    {Home: "/home/josh", Environment: map[string]string{"XDG_DATA_HOME": "/"}},
		"relative runtime":  {Home: "/home/josh", Environment: map[string]string{"XDG_RUNTIME_DIR": "relative"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ResolveXDGPaths(input); err == nil {
				t.Fatalf("ResolveXDGPaths(%#v) accepted unsafe paths", input)
			}
		})
	}
}

func TestResolveFileBackendRequiresStrictAbsoluteCredentialFreeURL(t *testing.T) {
	t.Parallel()
	resolved, err := ResolveFileBackend("file:///mnt/camp-backend")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Root != filepath.Clean("/mnt/camp-backend") || resolved.SanitizedURL != "file:///mnt/camp-backend" || len(resolved.Fingerprint) != 64 {
		t.Fatalf("resolved file backend = %#v", resolved)
	}
	for _, raw := range []string{
		"/mnt/camp-backend",
		"file:relative",
		"file://host/mnt/camp",
		"file://user:secret@host/mnt/camp",
		"file:///",
		"file:///mnt/camp?token=secret",
		"file:///mnt/camp#fragment",
		"file:///mnt/%63amp",
		"s3://bucket/camp",
	} {
		t.Run(strings.ReplaceAll(raw, "/", "_"), func(t *testing.T) {
			if _, err := ResolveFileBackend(raw); err == nil {
				t.Fatalf("ResolveFileBackend(%q) accepted unsafe or unsupported URL", raw)
			}
		})
	}
}

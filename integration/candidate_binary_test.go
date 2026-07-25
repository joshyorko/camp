package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCandidateBinaryRequiresOrchestratorProvidedExecutable(t *testing.T) {
	if _, err := resolveCandidateBinary(""); err == nil || !strings.Contains(err.Error(), "CAMP_TEST_BINARY") {
		t.Fatalf("resolveCandidateBinary(empty) error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "camp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveCandidateBinary(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolveCandidateBinary() = %q, want %q", got, path)
	}
}

func TestResolveCandidateBinaryRejectsNonExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "camp")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCandidateBinary(path); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("resolveCandidateBinary(non-executable) error = %v", err)
	}
}

func TestMergeCommandEnvironmentOverridesInheritedValues(t *testing.T) {
	got := mergeCommandEnvironment(
		[]string{"PATH=/usr/bin", "XDG_DATA_HOME=/shared", "UNCHANGED=yes"},
		[]string{"XDG_DATA_HOME=/controller", "PATH=/managed/bin"},
	)
	want := map[string]string{
		"PATH":          "/managed/bin",
		"XDG_DATA_HOME": "/controller",
		"UNCHANGED":     "yes",
	}
	for _, entry := range got {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			if expected, exists := want[name]; exists {
				if value != expected {
					t.Fatalf("%s = %q, want %q in %v", name, value, expected, got)
				}
				delete(want, name)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("merged environment missing %#v: %v", want, got)
	}
}

package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionDoctorRendersReadOnlyCapabilityReport(t *testing.T) {
	bin := t.TempDir()
	writeDoctorExecutable(t, bin, "devpod", "devpod v0.26.1")
	writeDoctorExecutable(t, bin, "hauler", "hauler v2.0.2")
	writeDoctorExecutable(t, bin, "pasta", "pasta 2026 --foreground --quiet --log-file --pid --ipv4-only --host-lo-to-ns-lo --tcp-ports --udp-ports --tcp-ns --udp-ns")
	writeDoctorExecutable(t, bin, "runcon", "runcon")
	t.Setenv("PATH", bin)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
	t.Setenv("CAMP_BACKEND", "")
	t.Setenv("CAMP_ACCESS_TOKEN", "doctor-secret")

	var output bytes.Buffer
	if err := NewProductionLifecycle().Doctor(context.Background(), ModeJSON, &output); err == nil {
		t.Fatal("Doctor succeeded with unlocked test executables")
	}
	body := output.String()
	for _, expected := range []string{`"kind": "doctor"`, `"capability": "backend"`, `"capability": "devpod"`, `"capability": "hauler"`, `"capability": "pasta"`, `"capability": "proc-self-fd"`, `"capability": "tun"`, `"capability": "user-namespace"`, `"capability": "lsm"`, `"capability": "container-boundary"`, `"code": "backend_transaction_verified"`, `"code": "managed_tool_identity_unverified"`, `"code": "pasta_option_surface_available_runtime_unprobed"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("output missing %s:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "doctor-secret") {
		t.Fatalf("doctor leaked credential: %s", body)
	}
}

func TestProductionDoctorBlockedReportRendersExactlyOnceAndExitsNonzero(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "human", true: "json"}[jsonMode], func(t *testing.T) {
			t.Setenv("PATH", t.TempDir())
			t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
			t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
			t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))
			args := []string{"doctor"}
			if jsonMode {
				args = append(args, "--json")
			}
			var stdout, stderr bytes.Buffer
			code := Execute(context.Background(), NewRoot(), args, Streams{Out: &stdout, ErrOut: &stderr})
			if code != int(ExitFailure) {
				t.Fatalf("exit = %d, want %d", code, ExitFailure)
			}
			combined := stdout.String() + stderr.String()
			if strings.Count(combined, "Camp doctor:")+strings.Count(combined, `"kind": "doctor"`) != 1 {
				t.Fatalf("aggregate report count != 1; stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if strings.Contains(combined, "error [") || strings.Contains(combined, `"kind":"failure"`) {
				t.Fatalf("blocked report was followed by duplicate failure: %q", combined)
			}
		})
	}
}

func writeDoctorExecutable(t *testing.T, directory, name, output string) {
	t.Helper()
	body := "#!/bin/sh\nprintf '%s\\n' '" + output + "'\n"
	if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/ports"
)

func TestToolProbeReportsObservedExecutableIdentityWithoutClaimingLockVerification(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "devpod")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'devpod v0.26.1\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	probe := ToolProbe{Name: "devpod", Arguments: []string{"version"}, LookPath: func(string) (string, error) { return executable, nil }}
	result := probe.Probe(context.Background())
	if result.Status != StatusDegraded || result.Code != "tool_identity_observed" || result.Evidence["path"] != executable || len(result.Evidence["sha256"]) != 64 || result.Evidence["version"] != "devpod v0.26.1" {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(strings.ToLower(result.Summary), "verified") && !strings.Contains(strings.ToLower(result.Summary), "not lock-verified") {
		t.Fatalf("summary overclaims identity: %q", result.Summary)
	}
}

func TestToolProbeBlocksWhenExecutableIsMissingWithoutLeakingCause(t *testing.T) {
	probe := ToolProbe{Name: "hauler", LookPath: func(string) (string, error) { return "", errors.New("credential=super-secret") }}
	result := probe.Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "tool_missing" || strings.Contains(result.Summary+result.Remediation, "super-secret") {
		t.Fatalf("result = %#v", result)
	}
}

func TestToolProbeRedactsCredentialBearingIdentityOutput(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "devpod")
	if err := os.WriteFile(executable, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	probe := ToolProbe{
		Name: "devpod", LookPath: func(string) (string, error) { return executable, nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("https://user:pass@example.test token=secret"), nil
		},
	}
	result := probe.Probe(context.Background())
	if strings.Contains(result.Evidence["version"], "user:pass") || strings.Contains(result.Evidence["version"], "secret") {
		t.Fatalf("identity evidence leaked credentials: %#v", result)
	}
}

func TestSafeSingleLineRedactsCommonCredentialFormsBeforeTruncation(t *testing.T) {
	tests := []string{
		"CAMP_TOKEN=assignment-secret",
		"Authorization: Bearer bearer-secret",
		"https://user:pass@example.test/path",
		"https://example.test/path?token=query-secret&safe=yes",
		"unconfined_u:unconfined_r:unconfined_t:s0\x00",
		strings.Repeat("x", 240) + " CAMP_PASSWORD=" + strings.Repeat("s", 300),
	}
	for _, input := range tests {
		got := safeSingleLine(input)
		for _, leaked := range []string{"assignment-secret", "bearer-secret", "user:pass", "query-secret", strings.Repeat("s", 10)} {
			if strings.Contains(got, leaked) {
				t.Fatalf("safeSingleLine leaked %q from %q: %q", leaked, input, got)
			}
		}
		if len(got) > 256 {
			t.Fatalf("safeSingleLine length = %d, want <= 256", len(got))
		}
		if strings.ContainsRune(got, '\x00') {
			t.Fatalf("safeSingleLine retained NUL from %q: %q", input, got)
		}
	}
}

func TestToolProbeSelectsVersionLineAfterBanner(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "hauler")
	if err := os.WriteFile(executable, []byte("executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	probe := ToolProbe{
		Name: "hauler", LookPath: func(string) (string, error) { return executable, nil },
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("__ HAULER ASCII BANNER __\nhauler: Airgap Swiss Army Knife\n\nGitVersion:    v2.0.2\nGitCommit: 4ece589\n"), nil
		},
	}
	result := probe.Probe(context.Background())
	if result.Evidence["version"] != "GitVersion:    v2.0.2" {
		t.Fatalf("version evidence = %q", result.Evidence["version"])
	}
}

type staticConfinementResolver struct {
	capability ports.ConfinementCapability
	err        error
}

func (r staticConfinementResolver) Resolve(context.Context) (ports.ConfinementCapability, error) {
	return r.capability, r.err
}

func TestConfinementProbeReportsSyntaxOnlyCapabilityAsDegraded(t *testing.T) {
	result := (ConfinementProbe{Resolver: staticConfinementResolver{capability: ports.ConfinementCapability{Executable: "/usr/bin/pasta", Version: "pasta 2026\nGNU General Public License, version 2 or later", Boundary: "host", EnvironmentFingerprint: "abc"}}}).Probe(context.Background())
	if result.Status != StatusDegraded || result.Code != "pasta_option_surface_available_runtime_unprobed" || result.Evidence["boundary"] != "host" || result.Evidence["fingerprint"] != "abc" || result.Evidence["version"] != "pasta 2026" {
		t.Fatalf("result = %#v", result)
	}
}

func TestConfinementProbeBlocksWithoutLeakingResolverCause(t *testing.T) {
	result := (ConfinementProbe{Resolver: staticConfinementResolver{err: errors.New("token=secret")}}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "pasta_confinement_unavailable" || strings.Contains(result.Summary+result.Remediation, "secret") {
		t.Fatalf("result = %#v", result)
	}
}

func TestBackendProbeDistinguishesConfigurationFromHealth(t *testing.T) {
	tests := []struct {
		name       string
		probe      BackendProbe
		wantStatus Status
		wantCode   string
	}{
		{name: "not configured", probe: BackendProbe{ConfigPath: filepath.Join(t.TempDir(), "missing")}, wantStatus: StatusSkippedNotConfigured, wantCode: "backend_not_configured"},
		{name: "default file", probe: BackendProbe{ConfigPath: filepath.Join(t.TempDir(), "missing"), DefaultBackend: "file:///tmp/camp-backend"}, wantStatus: StatusDegraded, wantCode: "file_backend_configuration_valid_io_unprobed"},
		{name: "s3 credentials resolved", probe: BackendProbe{ConfigPath: filepath.Join(t.TempDir(), "missing"), Environment: map[string]string{"CAMP_BACKEND": "s3://camp-bucket/team", "CAMP_S3_ENDPOINT": "https://s3.example.test", "CAMP_S3_REGION": "us-east-1"}, CheckCredentials: func(context.Context, config.Backend) error { return nil }}, wantStatus: StatusDegraded, wantCode: "s3_credentials_available_backend_unprobed"},
		{name: "s3 credentials unavailable", probe: BackendProbe{ConfigPath: filepath.Join(t.TempDir(), "missing"), Environment: map[string]string{"CAMP_BACKEND": "s3://camp-bucket/team", "CAMP_S3_ENDPOINT": "https://s3.example.test", "CAMP_S3_REGION": "us-east-1"}, CheckCredentials: func(context.Context, config.Backend) error { return errors.New("accessKey=secret") }}, wantStatus: StatusBlocked, wantCode: "s3_credentials_unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.probe.Probe(context.Background())
			if result.Status != tt.wantStatus || result.Code != tt.wantCode || strings.Contains(result.Summary+result.Remediation, "secret") {
				t.Fatalf("result = %#v", result)
			}
			if strings.Contains(result.Summary, "healthy") {
				t.Fatalf("configuration probe claimed backend health: %#v", result)
			}
		})
	}
}

func TestBackendProbeReportsHealthyOnlyAfterFunctionalTransaction(t *testing.T) {
	store, err := filebackend.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	probe := BackendProbe{
		ConfigPath: filepath.Join(t.TempDir(), "missing"), DefaultBackend: "file:///tmp/camp-backend",
		OpenStore: func(context.Context, config.Backend) (ports.ObjectStore, error) { return store, nil },
		NewSuffix: func() (string, error) { return "unique", nil },
	}
	result := probe.Probe(context.Background())
	if result.Status != StatusHealthy || result.Code != "backend_transaction_verified" || result.Evidence["kind"] != "file" {
		t.Fatalf("result = %#v", result)
	}
}

type waitingProbe struct{}

func (waitingProbe) Capability() string { return "waiting" }
func (waitingProbe) Probe(ctx context.Context) Result {
	<-ctx.Done()
	return Result{Capability: "waiting", Status: StatusBlocked, Code: "ignored"}
}

func TestRunnerBoundsEachProbe(t *testing.T) {
	report := (Runner{Probes: []Probe{waitingProbe{}}, Timeout: time.Millisecond}).Run(context.Background())
	if len(report.Results) != 1 || report.Results[0].Code != "probe_timeout" || report.Results[0].Status != StatusBlocked {
		t.Fatalf("report = %#v", report)
	}
}

package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestConfiguredProbeReportsNotConfiguredTruthfully(t *testing.T) {
	result := (ConfiguredProbe{Name: "workspace", Configured: false}).Probe(context.Background())
	if result.Status != StatusSkippedNotConfigured || result.Code != "workspace_not_configured" || !strings.Contains(result.Summary, "not configured") {
		t.Fatalf("result = %#v", result)
	}
}

func TestConfiguredProbeReportsFunctionalReachability(t *testing.T) {
	result := (ConfiguredProbe{Name: "service", Configured: true, Check: func(context.Context) (map[string]string, error) {
		return map[string]string{"count": "2", "operation": "identity-and-http"}, nil
	}}).Probe(context.Background())
	if result.Status != StatusHealthy || result.Code != "service_reachable" || result.Evidence["operation"] != "identity-and-http" {
		t.Fatalf("result = %#v", result)
	}
}

func TestConfiguredProbeBlocksWithoutLeakingReachabilityCause(t *testing.T) {
	result := (ConfiguredProbe{Name: "forwarding", Configured: true, Check: func(context.Context) (map[string]string, error) {
		return nil, errors.New("access_token=secret")
	}}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "forwarding_unreachable" || strings.Contains(result.Summary+result.Remediation, "secret") {
		t.Fatalf("result = %#v", result)
	}
}

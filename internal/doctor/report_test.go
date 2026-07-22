package doctor

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderJSONUsesStableVersionedSortedConfiguredReport(t *testing.T) {
	report := NewReport([]Result{
		{Capability: "pasta", Status: StatusHealthy, Code: "pasta_runtime_verified", Summary: "pasta runtime is verified", Evidence: map[string]string{"boundary": "host"}},
		{Capability: "workspace", Status: StatusSkippedNotConfigured, Code: "workspace_not_configured", Summary: "workspace is not configured", Remediation: "configure workspace when required"},
		{Capability: "devpod", Status: StatusHealthy, Code: "managed_tool_identity_verified", Summary: "devpod executable matches its locked identity", Evidence: map[string]string{"sha256": strings.Repeat("a", 64)}},
	})

	var output bytes.Buffer
	if err := RenderJSON(&output, report); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"schemaVersion\": 1,\n  \"kind\": \"doctor\",\n  \"status\": \"healthy\",\n  \"results\": [\n    {\n      \"capability\": \"devpod\",\n      \"status\": \"healthy\",\n      \"code\": \"managed_tool_identity_verified\",\n      \"summary\": \"devpod executable matches its locked identity\",\n      \"evidence\": {\n        \"sha256\": \"" + strings.Repeat("a", 64) + "\"\n      }\n    },\n    {\n      \"capability\": \"pasta\",\n      \"status\": \"healthy\",\n      \"code\": \"pasta_runtime_verified\",\n      \"summary\": \"pasta runtime is verified\",\n      \"evidence\": {\n        \"boundary\": \"host\"\n      }\n    },\n    {\n      \"capability\": \"workspace\",\n      \"status\": \"skipped-not-configured\",\n      \"code\": \"workspace_not_configured\",\n      \"summary\": \"workspace is not configured\",\n      \"remediation\": \"configure workspace when required\"\n    }\n  ]\n}\n"
	if output.String() != want {
		t.Fatalf("JSON output =\n%s\nwant\n%s", output.String(), want)
	}
}

func TestRenderHumanShowsActionableResults(t *testing.T) {
	report := NewReport([]Result{{Capability: "pasta", Status: StatusBlocked, Code: "pasta_missing", Summary: "pasta is unavailable", Remediation: "install passt outside Camp, then rerun camp doctor"}})
	var output bytes.Buffer
	if err := RenderHuman(&output, report); err != nil {
		t.Fatal(err)
	}
	want := "Camp doctor: blocked\nBLOCKED  pasta  pasta_missing  pasta is unavailable\n  remediation: install passt outside Camp, then rerun camp doctor\n"
	if output.String() != want {
		t.Fatalf("human output = %q, want %q", output.String(), want)
	}
}

func TestRenderHumanDistinguishesConfiguredReachabilityFromNotConfigured(t *testing.T) {
	report := NewReport([]Result{
		{Capability: "provider", Status: StatusHealthy, Code: "provider_reachable", Summary: "provider functional reachability is verified", Evidence: map[string]string{"provider": "docker"}},
		{Capability: "workspace", Status: StatusSkippedNotConfigured, Code: "workspace_not_configured", Summary: "workspace is not configured", Remediation: "configure workspace when this capability is required"},
	})
	var output bytes.Buffer
	if err := RenderHuman(&output, report); err != nil {
		t.Fatal(err)
	}
	want := "Camp doctor: healthy\nHEALTHY  provider  provider_reachable  provider functional reachability is verified\nSKIPPED-NOT-CONFIGURED  workspace  workspace_not_configured  workspace is not configured\n  remediation: configure workspace when this capability is required\n"
	if output.String() != want {
		t.Fatalf("human output = %q, want %q", output.String(), want)
	}
}

func TestNewReportBlockedDominatesDegraded(t *testing.T) {
	report := NewReport([]Result{{Capability: "tool", Status: StatusDegraded}, {Capability: "pasta", Status: StatusBlocked}})
	if report.Status != StatusBlocked || !report.Blocked() {
		t.Fatalf("report = %#v", report)
	}
}

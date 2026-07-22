package doctor

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderJSONUsesStableVersionedSortedReport(t *testing.T) {
	report := NewReport([]Result{
		{Capability: "pasta", Status: StatusHealthy, Code: "pasta_confinement_available", Summary: "confinement is available", Evidence: map[string]string{"boundary": "host"}},
		{Capability: "backend", Status: StatusSkippedNotConfigured, Code: "backend_not_configured", Summary: "backend is not configured", Remediation: "set CAMP_BACKEND"},
		{Capability: "devpod", Status: StatusDegraded, Code: "tool_identity_observed", Summary: "identity observed but not lock-verified", Evidence: map[string]string{"sha256": strings.Repeat("a", 64)}},
	})

	var output bytes.Buffer
	if err := RenderJSON(&output, report); err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"schemaVersion\": 1,\n  \"kind\": \"doctor\",\n  \"status\": \"degraded\",\n  \"results\": [\n    {\n      \"capability\": \"backend\",\n      \"status\": \"skipped-not-configured\",\n      \"code\": \"backend_not_configured\",\n      \"summary\": \"backend is not configured\",\n      \"remediation\": \"set CAMP_BACKEND\"\n    },\n    {\n      \"capability\": \"devpod\",\n      \"status\": \"degraded\",\n      \"code\": \"tool_identity_observed\",\n      \"summary\": \"identity observed but not lock-verified\",\n      \"evidence\": {\n        \"sha256\": \"" + strings.Repeat("a", 64) + "\"\n      }\n    },\n    {\n      \"capability\": \"pasta\",\n      \"status\": \"healthy\",\n      \"code\": \"pasta_confinement_available\",\n      \"summary\": \"confinement is available\",\n      \"evidence\": {\n        \"boundary\": \"host\"\n      }\n    }\n  ]\n}\n"
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

func TestNewReportBlockedDominatesDegraded(t *testing.T) {
	report := NewReport([]Result{{Capability: "tool", Status: StatusDegraded}, {Capability: "pasta", Status: StatusBlocked}})
	if report.Status != StatusBlocked || !report.Blocked() {
		t.Fatalf("report = %#v", report)
	}
}

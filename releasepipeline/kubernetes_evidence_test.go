package releasepipeline_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestKubernetesEvidenceValidatorAcceptsOnlyBoundPassingEvidence(t *testing.T) {
	root := filepath.Clean("..")
	evidence := t.TempDir()
	commit := strings.Repeat("a", 40)
	digest := strings.Repeat("b", 64)
	writeEvidenceJSON(t, evidence, "candidate-identity.json", map[string]any{
		"schemaVersion": 1, "candidateCommit": commit, "candidateSha256": digest,
		"relevantChangeCommit": strings.Repeat("c", 40),
	})
	writeEvidenceJSON(t, evidence, "gate-status.json", map[string]any{
		"schemaVersion": 1, "result": "passed", "candidateCommit": commit,
		"candidateSha256": digest, "provider": "kubernetes-protected",
		"devpodContext": "camp-protected", "kubernetesContext": "authorized",
		"ociCapability": "unsupported",
	})
	writeEvidenceJSON(t, evidence, "cleanup-receipt.json", map[string]any{
		"schemaVersion": 1, "result": "passed", "candidateCommit": commit,
		"candidateSha256": digest, "scenarioId": "scenario", "namespace": "camp-evidence-a",
		"resources": []any{}, "workspaceIds": []any{},
	})
	writeEvidenceJSON(t, evidence, "robot-results.json", map[string]any{
		"schemaVersion": 1, "result": "passed",
		"tests": []any{map[string]any{"name": "TestKubernetesLifecycleVertical", "result": "passed"}},
	})

	command := exec.Command("python3", filepath.Join(root, "scripts", "kubernetes_evidence.py"),
		"validate", "--directory", evidence, "--commit", commit, "--sha256", digest, "--require-pass")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("validator rejected bound passing evidence: %v\n%s", err, output)
	}
}

func TestKubernetesEvidenceValidatorRejectsUndeclaredAndSensitiveArtifacts(t *testing.T) {
	root := filepath.Clean("..")
	for _, test := range []struct {
		name     string
		filename string
		body     string
		want     string
	}{
		{name: "undeclared file", filename: "command.log", body: "harmless", want: "undeclared evidence file"},
		{name: "kubeconfig", filename: "gate-status.json", body: `{"kubeconfig":"apiVersion: v1"}`, want: "forbidden key"},
		{name: "token shape", filename: "gate-status.json", body: `{"detail":"Bearer abcdefghijklmnopqrstuvwxyz"}`, want: "sensitive value shape"},
		{name: "credential URL", filename: "gate-status.json", body: `{"detail":"https://user:pass@example.test"}`, want: "sensitive value shape"},
		{name: "PEM certificate", filename: "gate-status.json", body: `{"detail":"-----BEGIN CERTIFICATE-----"}`, want: "sensitive value shape"},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := t.TempDir()
			if err := os.WriteFile(filepath.Join(evidence, test.filename), []byte(test.body), 0o600); err != nil {
				t.Fatal(err)
			}
			command := exec.Command("python3", filepath.Join(root, "scripts", "kubernetes_evidence.py"),
				"validate", "--directory", evidence, "--commit", strings.Repeat("a", 40), "--sha256", strings.Repeat("b", 64))
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), test.want) {
				t.Fatalf("validator error = %v, output = %q, want %q", err, output, test.want)
			}
		})
	}
}

func TestKubernetesEvidenceSanitizerDropsGoTestOutput(t *testing.T) {
	root := filepath.Clean("..")
	input := filepath.Join(t.TempDir(), "go-test.json")
	output := filepath.Join(t.TempDir(), "robot-results.json")
	lines := strings.Join([]string{
		`{"Action":"run","Test":"TestKubernetesLifecycleVertical"}`,
		`{"Action":"output","Test":"TestKubernetesLifecycleVertical","Output":"Bearer must-not-survive"}`,
		`{"Action":"pass","Test":"TestKubernetesLifecycleVertical","Elapsed":1.25}`,
	}, "\n")
	if err := os.WriteFile(input, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", filepath.Join(root, "scripts", "kubernetes_evidence.py"),
		"sanitize-go-test", "--input", input, "--output", output)
	if commandOutput, err := command.CombinedOutput(); err != nil {
		t.Fatalf("sanitize: %v\n%s", err, commandOutput)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "Bearer") || !strings.Contains(string(body), `"result": "passed"`) {
		t.Fatalf("sanitized results = %s", body)
	}
}

func TestProtectedProviderWorkflowRunsBoundFailClosedKubernetesEvidence(t *testing.T) {
	body := readRepositoryFile(t, filepath.Clean(".."), ".github/workflows/provider-evidence.yml")
	requireContains(t, body,
		"workflow_dispatch:",
		"candidate_commit:",
		"candidate_sha256:",
		"provider_profile:",
		"devpod_context:",
		"kubernetes_context:",
		"oci_capability:",
		"environment: release-providers",
		"secrets.CAMP_PROTECTED_KUBECONFIG_B64",
		"secrets.CAMP_PROTECTED_DEVPOD_CONFIG_B64",
		"CAMP_KUBERNETES_EVIDENCE: \"1\"",
		"CAMP_KUBERNETES_CANDIDATE_COMMIT:",
		"CAMP_KUBERNETES_CANDIDATE_SHA256:",
		"CAMP_KUBERNETES_RELEVANT_CHANGE_COMMIT:",
		"-t robotKubernetes",
		"kubernetes_evidence.py sanitize-go-test",
		"kubernetes_evidence.py validate",
		"if: always()",
	)
	if strings.Contains(body, "schedule:") || strings.Contains(body, `"result":"gated"`) {
		t.Fatal("protected provider workflow must be explicit dispatch, not a scheduled placeholder")
	}
}

func TestReleaseWorkflowCannotClaimKubernetesWithoutMatchingProtectedEvidence(t *testing.T) {
	body := readRepositoryFile(t, filepath.Clean(".."), ".github/workflows/release.yml")
	requireContains(t, body,
		"claim_kubernetes_support:",
		"kubernetes_evidence_run_id:",
		"kubernetes_candidate_sha256:",
		"name: Bind optional Kubernetes support claim",
		"if: inputs.claim_kubernetes_support == true",
		"environment: release-providers",
		`--name "kubernetes-evidence-${GITHUB_SHA}"`,
		"kubernetes_evidence.py validate",
		"--relevant-change",
		"needs: [verified-artifacts, kubernetes-evidence]",
	)
}

func writeEvidenceJSON(t *testing.T, directory, name string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), append(body, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

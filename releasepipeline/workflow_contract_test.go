package releasepipeline_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var immutableAction = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+@[0-9a-f]{40}$`)

func TestCIWorkflowIsMandatoryLeastPrivilegeAndForkSafe(t *testing.T) {
	workflow := readWorkflow(t, "ci.yml")
	requireContains(t, workflow,
		"pull_request:",
		"contents: read",
		"cancel-in-progress: true",
		"timeout-minutes:",
		"retention-days:",
		"go test ./... -count=1",
		"go test -race ./... -count=1",
		"go vet ./...",
		"govulncheck ./...",
		"TestMinIO",
		"packaging",
		"tools.lock.yaml",
		"CAMP_TEST_REAL_TOOLS",
	)
	for _, forbidden := range []string{"pull_request_target", "contents: write", "id-token: write", "secrets."} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("ci.yml contains unsafe pull-request token surface %q", forbidden)
		}
	}
	assertActionsPinned(t, workflow)
}

func TestCIAddsRCCParityWithoutCachingPrivateRuntimeHomes(t *testing.T) {
	workflow := readWorkflow(t, "ci.yml")
	requireContains(t, workflow,
		"name: RCC local candidate",
		"name: RCC source gates",
		"name: RCC exact-candidate lifecycle evidence",
		"./developer/rccw run -r developer/toolkit.yaml --dev -t local",
		"./developer/rccw run -r developer/toolkit.yaml --dev -t test",
		"./developer/rccw run -r developer/toolkit.yaml -t robot",
		"actions/download-artifact@",
		"developer/verify_pr_receipt.py",
		"build/evidence/candidate.json",
		"build/evidence/test-gates.json",
		"build/evidence/robot-gates.json",
		"build/evidence/robot/",
		"build/evidence/ci-cleanup-receipt.json",
		"name: rcc-robot-evidence-${{ github.sha }}",
		"if: always()",
		"name: Direct Go and RCC parity record",
		"needs: [test, integration, minio, locked-tools, packaging, rcc-local, rcc-test, rcc-robot]",
		"candidateCommit",
		"requiredConsecutiveCompleteRuns",
		"qualifiedHistoricalRuns",
		"build/evidence/parity.json",
		`"robot": os.environ["RCC_ROBOT"]`,
	)
	for _, directJob := range []string{"  test:", "  integration:", "  minio:", "  locked-tools:", "  packaging:"} {
		if !strings.Contains(workflow, directJob) {
			t.Errorf("direct parity lane %q was removed before two immutable complete run references exist", directJob)
		}
	}
	if !strings.Contains(workflow, `"qualifiedHistoricalRuns": []`) {
		t.Fatal("hosted parity history must remain empty until two real immutable complete run references exist")
	}
	if strings.Contains(workflow, "build/rcc-homes") && strings.Contains(workflow, "actions/cache") {
		t.Fatal("CI must not cache private RCC homes")
	}
}

func TestReleaseWorkflowVerifiesDownloadsBeforeProtectedPublication(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	requireContains(t, workflow,
		"workflow_dispatch:",
		"candidate_ci_run_id:",
		"candidate_commit:",
		"candidate_sha256:",
		"actions: read",
		"gh run download",
		"rcc-candidate-${CANDIDATE_COMMIT}",
		"candidate.json",
		"candidateSha256",
		"Run RCC release packaging for verified candidate",
		"environment: release",
		"contents: write",
		"id-token: write",
		"attestations: write",
		"needs: verified-artifacts",
		"download-artifact",
		"sha256sum --check checksums.txt",
		"build-release-evidence.sh verify",
		"./developer/rccw run -r developer/toolkit.yaml -t package",
		"name: RCC release package",
		"name: Seal verified artifact set",
		"verified-release-set-${{ inputs.candidate_commit }}",
		"verified_artifacts.py create dist",
		"verified_artifacts.py recheck dist",
		"dist/verified-artifacts.json",
		"inputs.publish == true",
		"retention-days:",
	)
	for _, forbiddenTrigger := range []string{"pull_request:", `push:
    tags:`} {
		if strings.Contains(workflow, forbiddenTrigger) {
			t.Fatalf("release.yml contains release trigger that cannot supply exact CI evidence: %q", forbiddenTrigger)
		}
	}
	if strings.Count(workflow, "inputs.publish == true") != 2 {
		t.Fatal("manual dry runs must gate both attestation and publication")
	}
	if strings.Count(workflow, "verified_artifacts.py recheck dist") != 2 {
		t.Fatal("attestation and publication must independently recheck the verified set")
	}
	attest := workflow[strings.Index(workflow, "  attest:"):strings.LastIndex(workflow, "  publish:")]
	publish := workflow[strings.LastIndex(workflow, "  publish:"):]
	for name, job := range map[string]string{"attest": attest, "publish": publish} {
		if strings.Contains(job, "build-release-evidence.sh build") ||
			strings.Contains(job, "build-archives.sh") ||
			strings.Contains(job, "-t package") {
			t.Errorf("%s job can rebuild release archives", name)
		}
	}
	assertActionsPinned(t, workflow)
}

func TestProviderEvidenceUsesExplicitProtectedProfilesNotSecretConditionals(t *testing.T) {
	workflow := readWorkflow(t, "provider-evidence.yml")
	requireContains(t, workflow,
		"workflow_dispatch:",
		"environment: release-providers",
		"provider_profile",
		"secrets.CAMP_PROTECTED_KUBECONFIG_B64",
		"secrets.CAMP_PROTECTED_DEVPOD_CONFIG_B64",
		"evidence",
		"gated",
	)
	for _, forbidden := range []string{"contents: write", "id-token: write"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("provider-evidence.yml contains forbidden implicit credential gate %q", forbidden)
		}
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatalf("parse provider-evidence.yml: %v", err)
	}
	triggers, ok := document["on"].(map[string]any)
	if !ok {
		t.Fatal("provider-evidence.yml lacks a mapping-valued on trigger")
	}
	if _, scheduled := triggers["schedule"]; scheduled {
		t.Fatal("provider-evidence.yml must be explicitly dispatched, not scheduled")
	}
	if path, condition, found := secretConditional(document, ""); found {
		t.Fatalf("provider-evidence.yml condition %s gates on a secret: %q", path, condition)
	}
	assertActionsPinned(t, workflow)
}

func TestSecretConditionalFindsFoldedAndNestedExpressions(t *testing.T) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(`
jobs:
  evidence:
    steps:
      - if: >-
          always() &&
          secrets.CAMP_PROTECTED_TOKEN != ''
`), &document); err != nil {
		t.Fatal(err)
	}
	path, _, found := secretConditional(document, "")
	if !found || path != "jobs.evidence.steps[0].if" {
		t.Fatalf("secret conditional = %q, found %t", path, found)
	}
}

func secretConditional(value any, path string) (string, string, bool) {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			childPath := key
			if path != "" {
				childPath = path + "." + key
			}
			if key == "if" {
				if condition, ok := child.(string); ok && strings.Contains(condition, "secrets.") {
					return childPath, condition, true
				}
			}
			if foundPath, condition, found := secretConditional(child, childPath); found {
				return foundPath, condition, true
			}
		}
	case []any:
		for index, child := range node {
			childPath := fmt.Sprintf("%s[%d]", path, index)
			if foundPath, condition, found := secretConditional(child, childPath); found {
				return foundPath, condition, true
			}
		}
	}
	return "", "", false
}

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	var document any
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return string(contents)
}

func assertActionsPinned(t *testing.T, workflow string) {
	t.Helper()
	if strings.Contains(workflow, "actions/checkout@") && !strings.Contains(workflow, "# v5") {
		t.Error("checkout must use the Node.js 24 v5 action")
	}
	if strings.Contains(workflow, "actions/setup-go@") && !strings.Contains(workflow, "# v6") {
		t.Error("setup-go must use the Node.js 24 v6 action")
	}
	uses := regexp.MustCompile(`(?m)^\s*-?\s*uses:\s*([^\s#]+)`).FindAllStringSubmatch(workflow, -1)
	if len(uses) == 0 {
		t.Fatal("workflow contains no actions")
	}
	for _, match := range uses {
		if strings.HasPrefix(match[1], "./") || strings.HasPrefix(match[1], "docker://") {
			continue
		}
		if !immutableAction.MatchString(match[1]) {
			t.Errorf("action is not pinned to a full commit SHA: %s", match[1])
		}
	}
}

func requireContains(t *testing.T, body string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(body, fragment) {
			t.Errorf("workflow is missing %q", fragment)
		}
	}
}

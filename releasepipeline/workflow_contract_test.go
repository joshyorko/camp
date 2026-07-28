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
		"build/evidence/parity.json",
		"developer/ci_release_evidence.py write-parity",
	)
	for _, directJob := range []string{"  test:", "  integration:", "  minio:", "  locked-tools:", "  packaging:"} {
		if !strings.Contains(workflow, directJob) {
			t.Errorf("direct parity lane %q was removed before two immutable complete run references exist", directJob)
		}
	}
	if strings.Contains(workflow, "build/rcc-homes") && strings.Contains(workflow, "actions/cache") {
		t.Fatal("CI must not cache private RCC homes")
	}
	document := parseWorkflow(t, workflow)
	stage := findStepByName(t, document, "rcc-local", "Stage candidate with preserved build root")
	stageRun := stage["run"].(string)
	for _, staged := range []string{
		"ci-artifact/build/camp",
		"ci-artifact/build/evidence/candidate.json",
	} {
		if !strings.Contains(stageRun, staged) {
			t.Errorf("candidate staging omits preserved path %s", staged)
		}
	}
	candidateUpload := findStepByUses(t, document, "rcc-local", "actions/upload-artifact@")
	candidateUploadWith := candidateUpload["with"].(map[string]any)
	if candidateUploadWith["path"] != "ci-artifact/" {
		t.Fatalf("candidate upload root = %#v, want ci-artifact/", candidateUploadWith["path"])
	}
	download := findStepByUses(t, document, "rcc-robot", "actions/download-artifact@")
	with := download["with"].(map[string]any)
	if with["path"] != "." {
		t.Fatalf("RCC Robot candidate download path = %#v, want repository root", with["path"])
	}
	findStepByUses(t, document, "parity-evidence", "actions/checkout@")
	requireEvidence := findStepByName(t, document, "rcc-robot", "Require mandatory RCC Robot evidence")
	run := requireEvidence["run"].(string)
	for _, required := range []string{
		"build/evidence/candidate.json",
		"build/evidence/robot-gates.json",
		"build/evidence/robot/output.xml",
		"build/evidence/robot/log.html",
		"build/evidence/robot/report.html",
		"build/evidence/robot-cleanup.jsonl",
		"build/evidence/ci-cleanup-receipt.json",
	} {
		if !strings.Contains(run, "test -f "+required) {
			t.Errorf("mandatory Robot evidence validation omits %s", required)
		}
	}
	upload := findStepByName(t, document, "rcc-robot", "Upload RCC Robot evidence and cleanup receipt")
	uploadWith := upload["with"].(map[string]any)
	if uploadWith["if-no-files-found"] != "error" {
		t.Fatalf("mandatory Robot evidence missing-file policy = %#v, want error", uploadWith["if-no-files-found"])
	}
}

func TestRCCRobotAuthorizesPastaUserNamespacesOnRestrictedUbuntu(t *testing.T) {
	workflow := parseWorkflow(t, readWorkflow(t, "ci.yml"))
	step := findStepByName(t, workflow, "rcc-robot", "Authorize RCC pasta user namespace")
	run := step["run"].(string)
	for _, required := range []string{
		"/proc/sys/kernel/apparmor_restrict_unprivileged_userns",
		"sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0",
		"unshare --user --map-root-user true",
	} {
		if !strings.Contains(run, required) {
			t.Errorf("Pasta user-namespace setup omits %q", required)
		}
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
		"ci_release_evidence.py verify-candidate",
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
		"ci_release_evidence.py fetch-verify-tag",
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
	document := parseWorkflow(t, workflow)
	publishStep := findStepByName(t, document, "publish", "Publish verified assets")
	publishRun := publishStep["run"].(string)
	verifyIndex := strings.Index(publishRun, "ci_release_evidence.py fetch-verify-tag")
	publishIndex := strings.Index(publishRun, "gh release create")
	if verifyIndex < 0 || publishIndex < 0 || verifyIndex >= publishIndex {
		t.Fatal("release tag fetch and exact target verification must finish before publication")
	}
	assertActionsPinned(t, workflow)
}

func parseWorkflow(t *testing.T, workflow string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(workflow), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func findStepByUses(t *testing.T, document map[string]any, jobName, prefix string) map[string]any {
	t.Helper()
	for _, step := range workflowSteps(t, document, jobName) {
		if uses, _ := step["uses"].(string); strings.HasPrefix(uses, prefix) {
			return step
		}
	}
	t.Fatalf("job %s has no step using %s", jobName, prefix)
	return nil
}

func findStepByName(t *testing.T, document map[string]any, jobName, name string) map[string]any {
	t.Helper()
	for _, step := range workflowSteps(t, document, jobName) {
		if step["name"] == name {
			return step
		}
	}
	t.Fatalf("job %s has no step named %s", jobName, name)
	return nil
}

func workflowSteps(t *testing.T, document map[string]any, jobName string) []map[string]any {
	t.Helper()
	jobs := document["jobs"].(map[string]any)
	job := jobs[jobName].(map[string]any)
	rawSteps := job["steps"].([]any)
	steps := make([]map[string]any, 0, len(rawSteps))
	for _, raw := range rawSteps {
		steps = append(steps, raw.(map[string]any))
	}
	return steps
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

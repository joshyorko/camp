package releasepipeline_test

import (
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
		"./developer/rccw run -r developer/toolkit.yaml --dev -t local",
		"./developer/rccw run -r developer/toolkit.yaml --dev -t test",
		"developer/verify_pr_receipt.py",
		"build/evidence/candidate.json",
		"build/evidence/test-gates.json",
		"name: Direct Go and RCC parity record",
		"needs: [test, integration, minio, locked-tools, packaging, rcc-local, rcc-test]",
		"candidateCommit",
		"requiredConsecutiveCompleteRuns",
		"qualifiedHistoricalRuns",
		"build/evidence/parity.json",
		"if: always()",
	)
	if strings.Contains(workflow, "./developer/rccw run -r developer/toolkit.yaml -t robot") {
		t.Fatal("RCC Robot must not become mandatory CI before its roadmap-gated product evidence passes")
	}
	if strings.Contains(workflow, "build/rcc-homes") && strings.Contains(workflow, "actions/cache") {
		t.Fatal("CI must not cache private RCC homes")
	}
}

func TestReleaseWorkflowVerifiesDownloadsBeforeProtectedPublication(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	requireContains(t, workflow,
		"workflow_dispatch:",
		"tags:",
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
		"verified-release-set-${{ github.sha }}",
		"verified_artifacts.py create dist",
		"verified_artifacts.py recheck dist",
		"dist/verified-artifacts.json",
		"github.event_name == 'push' || inputs.publish == true",
		"retention-days:",
	)
	if strings.Contains(workflow, "pull_request:") {
		t.Fatal("release.yml must not run on pull requests")
	}
	if strings.Count(workflow, "github.event_name == 'push' || inputs.publish == true") != 2 {
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
	for _, forbidden := range []string{"secrets. != ''", "if: secrets.", "if: ${{ secrets.", "contents: write", "id-token: write"} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("provider-evidence.yml contains forbidden implicit credential gate %q", forbidden)
		}
	}
	assertActionsPinned(t, workflow)
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

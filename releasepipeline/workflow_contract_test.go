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

func TestReleaseWorkflowVerifiesDownloadsBeforeProtectedPublication(t *testing.T) {
	workflow := readWorkflow(t, "release.yml")
	requireContains(t, workflow,
		"workflow_dispatch:",
		"tags:",
		"environment: release",
		"contents: write",
		"id-token: write",
		"attestations: write",
		"needs: attest",
		"download-artifact",
		"sha256sum --check checksums.txt",
		"build-release-evidence.sh verify",
		"retention-days:",
	)
	if strings.Contains(workflow, "pull_request:") {
		t.Fatal("release.yml must not run on pull requests")
	}
	assertActionsPinned(t, workflow)
}

func TestProviderEvidenceUsesExplicitProtectedProfilesNotSecretConditionals(t *testing.T) {
	workflow := readWorkflow(t, "provider-evidence.yml")
	requireContains(t, workflow,
		"workflow_dispatch:",
		"schedule:",
		"environment: release-providers",
		"provider_profile",
		"evidence",
		"gated",
	)
	for _, forbidden := range []string{"secrets. != ''", "secrets.", "contents: write", "id-token: write"} {
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

package releasepipeline_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestOperationalSkillIndexCoversEveryCanonicalGuide(t *testing.T) {
	skillsDir := filepath.Join("..", "docs", "skills")
	index := readDocumentationContractFile(t, filepath.Join(skillsDir, "README.md"))
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		t.Fatal(err)
	}

	datedDiary := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}`)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".md" || name == "README.md" {
			continue
		}
		if datedDiary.MatchString(name) {
			t.Errorf("canonical skill %q looks like a dated run diary", name)
		}
		if !strings.Contains(index, "]("+name+")") {
			t.Errorf("docs/skills/README.md does not link canonical guide %q", name)
		}
	}
}

func TestSelfImprovementReceiptRemainsReviewable(t *testing.T) {
	agents := readDocumentationContractFile(t, filepath.Join("..", "AGENTS.md"))
	for _, phrase := range []string{
		"docs/skills/README.md",
		"same commit or pull request",
		"Documentation improvement:",
		"Canonical file changed or proposed:",
		"Durable learning captured:",
		"Evidence:",
		"Stale or ambiguous guidance removed:",
		"Remaining uncertainty:",
	} {
		if !strings.Contains(agents, phrase) {
			t.Errorf("AGENTS.md lacks documentation contract phrase %q", phrase)
		}
	}
}

func TestCanonicalChangelogKeepsAnUnreleasedReviewSurface(t *testing.T) {
	changelog := readDocumentationContractFile(t, filepath.Join("..", "docs", "changelog.md"))
	for _, phrase := range []string{
		"# Camp change log",
		"## Unreleased",
		"### Developer Experience",
		"### Portability and Safety",
		"### Documentation",
	} {
		if !strings.Contains(changelog, phrase) {
			t.Errorf("docs/changelog.md lacks canonical review surface %q", phrase)
		}
	}
}

func TestPullRequestReceiptRequiresReleaseNoteClassification(t *testing.T) {
	verifier := readDocumentationContractFile(t, filepath.Join("..", "developer", "verify_pr_receipt.py"))
	template := readDocumentationContractFile(t, filepath.Join("..", ".github", "pull_request_template.md"))
	for path, body := range map[string]string{
		"developer/verify_pr_receipt.py":   verifier,
		".github/pull_request_template.md": template,
	} {
		if !strings.Contains(body, "Release-note classification:") {
			t.Errorf("%s lacks release-note classification", path)
		}
	}
}

func TestRCCSourceGateLedgerNamesEveryMandatoryContract(t *testing.T) {
	tasks := readDocumentationContractFile(t, filepath.Join("..", "tasks.py"))
	for _, gate := range []string{
		`"unit"`,
		`"race"`,
		`"vet"`,
		`"vulnerability"`,
		`"generated-documentation"`,
		`"rcc-freeze"`,
		`"packaging"`,
		`"release-pipeline"`,
		`"deterministic-amd64"`,
		`"deterministic-arm64"`,
		`"contribution-receipt"`,
		`"whitespace"`,
	} {
		if !strings.Contains(tasks, gate) {
			t.Errorf("tasks.py lacks mandatory RCC source gate %s", gate)
		}
	}
	for _, field := range []string{`"command"`, `"durationMs"`, `"result"`, `result["reason"] = sanitize_failure(error)`} {
		if !strings.Contains(tasks, field) {
			t.Errorf("tasks.py evidence ledger lacks %s", field)
		}
	}
}

func readDocumentationContractFile(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

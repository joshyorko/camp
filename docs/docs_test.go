package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/docsgen"
)

func TestEveryDocumentedCampTranscriptIsExecuted(t *testing.T) {
	want := make(map[string]bool)
	for _, invocation := range docsgen.DocumentedInvocations() {
		want["camp "+strings.Join(invocation.Args, " ")] = true
	}
	contents, err := os.ReadFile("generated/transcripts.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\$ (camp(?: .*)?)$`)
	for _, match := range re.FindAllStringSubmatch(string(contents), -1) {
		if !want[match[1]] {
			t.Errorf("transcript command is not in the executable manifest: %s", match[1])
		}
		delete(want, match[1])
	}
	if len(want) != 0 {
		t.Errorf("executable manifest commands are missing transcripts: %v", want)
	}
}

func TestOperatorIndexLinksExistingFiles(t *testing.T) {
	contents, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`\[[^]]+\]\(([^):#]+\.md)\)`)
	for _, match := range re.FindAllStringSubmatch(string(contents), -1) {
		if _, err := os.Stat(filepath.Clean(match[1])); err != nil {
			t.Errorf("operator index link %q: %v", match[1], err)
		}
	}
}

func TestOperatorDocsMentionOnlyPublicCommands(t *testing.T) {
	generated, err := os.ReadFile("generated/commands.md")
	if err != nil {
		t.Fatal(err)
	}
	public := map[string]bool{}
	for _, match := range regexp.MustCompile("(?m)^## `camp(?: ([a-z-]+))?`$").FindAllStringSubmatch(string(generated), -1) {
		public[match[1]] = true
	}

	paths := []string{"../README.md", "README.md", "architecture.md", "backends.md", "install.md", "recovery.md", "release.md", "setup-and-doctor.md", "../packaging/INSTALL.md"}
	mention := regexp.MustCompile("`camp(?: ([a-z-]+))?(?: [^`]*)?`")
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range mention.FindAllStringSubmatch(string(contents), -1) {
			if !public[match[1]] {
				t.Errorf("%s documents non-public command %q", path, match[0])
			}
		}
	}
}

func TestBackendDocsDescribeExplicitInsecureHTTPPolicy(t *testing.T) {
	contents, err := os.ReadFile("backends.md")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, "Insecure HTTP is limited to loopback endpoints") {
		t.Fatal("backend docs incorrectly limit explicit insecure HTTP to loopback endpoints")
	}
	if !strings.Contains(text, "Plaintext HTTP endpoints require explicit insecure opt-in; this policy is not limited to loopback hosts.") {
		t.Fatal("backend docs do not describe the explicit insecure HTTP policy")
	}
}

func TestOperationalEvidenceDocsNameExactGatesAndMissingEvidenceRule(t *testing.T) {
	required := map[string][]string{
		"release.md": {
			"scripts/verify-real-evidence.sh list",
			"TestMountedFileBackendParity",
			"TestS3TwoWriterConflict",
			"skipped real-tool test is missing evidence",
		},
		"recovery.md": {
			"TestLocalLifecycleVertical",
			"TestLocalLifecycleCrashMatrix",
			"controller death after forwarder spawn",
		},
		"setup-and-doctor.md": {
			"`camp status`",
			"`camp images list`",
			"`camp serve status`",
			"`camp provider list`",
		},
		"skills/cli-composition.md": {
			"provider mutation",
			"configuration mutation",
		},
	}
	for path, phrases := range required {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, phrase := range phrases {
			if !strings.Contains(string(body), phrase) {
				t.Errorf("%s lacks %q", path, phrase)
			}
		}
	}
}

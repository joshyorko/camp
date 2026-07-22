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

package packaging_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestHomebrewMetadataNamesTapAndInstallShape(t *testing.T) {
	type metadata struct {
		TapRepository string            `json:"tapRepository"`
		FormulaPath   string            `json:"formulaPath"`
		Dependency    string            `json:"dependency"`
		Artifacts     map[string]string `json:"artifacts"`
	}

	body, err := os.ReadFile("homebrew/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var got metadata
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.TapRepository != "joshyorko/homebrew-tap" || got.FormulaPath != "Formula/camp.rb" {
		t.Fatalf("unexpected tap destination: %+v", got)
	}
	if got.Dependency != "passt" {
		t.Fatalf("dependency = %q, want passt", got.Dependency)
	}
	for architecture, artifact := range map[string]string{
		"amd64": "camp_{{VERSION}}_linux_amd64.tar.gz",
		"arm64": "camp_{{VERSION}}_linux_arm64.tar.gz",
	} {
		if got.Artifacts[architecture] != artifact {
			t.Fatalf("artifact[%s] = %q, want %q", architecture, got.Artifacts[architecture], artifact)
		}
	}

	formula, err := os.ReadFile("homebrew/camp.rb.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{
		`depends_on "passt"`,
		`bin.install "camp"`,
		`bash_completion.install "completions/camp.bash" => "camp"`,
		`zsh_completion.install "completions/_camp"`,
		`fish_completion.install "completions/camp.fish"`,
		`system "#{bin}/camp", "--version"`,
		`system "#{bin}/camp", "--help"`,
		"{{AMD64_SHA256}}",
		"{{ARM64_SHA256}}",
	} {
		if !strings.Contains(string(formula), fragment) {
			t.Fatalf("formula template does not contain %q", fragment)
		}
	}
}

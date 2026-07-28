package releasepipeline_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRCCFactoryDeclaresPinnedTrustRootAndCanonicalTasks(t *testing.T) {
	root := filepath.Clean("..")
	lock := readRepositoryFile(t, root, "developer/rcc.lock.yaml")
	var pinned struct {
		SchemaVersion int    `yaml:"schemaVersion"`
		Version       string `yaml:"version"`
		Commit        string `yaml:"commit"`
		Host          string `yaml:"host"`
		Name          string `yaml:"name"`
		URL           string `yaml:"url"`
		SHA256        string `yaml:"sha256"`
	}
	if err := yaml.Unmarshal([]byte(lock), &pinned); err != nil {
		t.Fatalf("parse developer/rcc.lock.yaml: %v", err)
	}
	if pinned.SchemaVersion != 1 || pinned.Host != "linux/amd64" || pinned.Name == "" {
		t.Fatalf("invalid RCC lock identity: %#v", pinned)
	}
	if !regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(pinned.Version) {
		t.Fatalf("RCC version is not an exact release: %q", pinned.Version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(pinned.Commit) {
		t.Fatalf("RCC commit is not exact: %q", pinned.Commit)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(pinned.SHA256) {
		t.Fatalf("RCC SHA-256 is not exact: %q", pinned.SHA256)
	}
	if !strings.Contains(pinned.URL, "/"+pinned.Version+"/") || !strings.HasSuffix(pinned.URL, "/"+pinned.Name) {
		t.Fatalf("RCC asset URL does not match lock identity: %q", pinned.URL)
	}

	toolkit := readRepositoryFile(t, root, "developer/toolkit.yaml")
	requireContains(t, toolkit,
		"robot:",
		"robotKubernetes:",
		"local:",
		"install:",
		"test:",
		"package:",
		"environmentConfigs:",
		"- setup.yaml",
		"artifactsDir: ../build/evidence",
	)
	wrapper := readRepositoryFile(t, root, "developer/rccw")
	requireContains(t, wrapper,
		`CAMP_RCC_HOME_ROOT`,
		`${XDG_CACHE_HOME:-"$HOME/.cache"}/camp/rcc-homes`,
		`mktemp -d "$rcc_home_root/camp-rcc-home.XXXXXX"`,
		`CAMP_RCC_HOME_ROOT must remain outside the repository`,
		`RCC lock host mismatch`,
		`invalid RCC lock commit`,
	)
	if strings.Contains(wrapper, `"$repository_root/build/rcc-homes"`) {
		t.Fatal("RCC homes must remain outside the Go module")
	}
	tasks := readRepositoryFile(t, root, "tasks.py")
	requireContains(t, tasks,
		`from developer.factory import install_candidate, resolve_package_identity`,
		`[str(CANDIDATE), "--json", "setup"]`,
		`result["path"]`,
		`"CAMP_TEST_BINARY": str(CANDIDATE)`,
		`discover_go_test("./integration", test_name, tags=["kubernetes_evidence"])`,
		`test_name = "TestKubernetesLifecycleVertical"`,
		`"-tags=kubernetes_evidence"`,
		`shutil.which("gcc") or shutil.which("x86_64-conda-linux-gnu-cc")`,
		`env={"CC": compiler, "CGO_ENABLED": "1"}`,
		`@task(name="install")`,
		`"""Verify and atomically install the exact local candidate."""`,
		`verify_candidate()`,
		`installed = install_candidate(CANDIDATE, install_dir)`,
		`resolve_package_identity(ROOT, os.environ)`,
		`package COMMIT must match checked-out HEAD`,
		`"result": "gated"`,
	)
	localTask := taskSource(t, tasks, "local")
	if strings.Contains(localTask, "install_candidate(") {
		t.Fatal("local must stop after repository candidate smoke verification")
	}
	installTask := taskSource(t, tasks, "install_task")
	if !strings.Contains(installTask, "verify_candidate()") || !strings.Contains(installTask, "install_candidate(") {
		t.Fatal("install must verify and link the exact local candidate")
	}
	if strings.Contains(tasks, "if not CANDIDATE.is_file():\n        build_candidate()") {
		t.Fatal("robot task must consume the local task candidate, not rebuild it")
	}

	setup := readRepositoryFile(t, root, "developer/setup.yaml")
	requireContains(t, setup,
		"python=3.10.15",
		"invoke=2.2.0",
		"robotframework=7.4.2",
		"go=1.26.5",
		"gcc_linux-64=15.2.0",
		"docker-cli=29.6.2",
		"passt=2025_06_11.0293c6f",
	)
	robotRequirements := strings.TrimSpace(readRepositoryFile(t, root, "robot_requirements.txt"))
	if robotRequirements != "robotframework==7.4.2" {
		t.Fatalf("robot_requirements.txt must exactly match the RCC Robot runtime pin, got %q", robotRequirements)
	}
	goModule := readRepositoryFile(t, root, "go.mod")
	requireContains(t, goModule, "\ngo 1.26.5\n")
	for _, forbidden := range []string{"devpod=", "hauler=", "room-of-requirement="} {
		if strings.Contains(setup, forbidden) {
			t.Fatalf("developer/setup.yaml duplicates tools.lock.yaml authority %q", forbidden)
		}
	}
}

func taskSource(t *testing.T, source, name string) string {
	t.Helper()
	marker := "def " + name + "("
	start := strings.Index(source, marker)
	if start < 0 {
		t.Fatalf("tasks.py lacks %s", marker)
	}
	rest := source[start+len(marker):]
	if next := strings.Index(rest, "\n@task"); next >= 0 {
		return source[start : start+len(marker)+next]
	}
	return source[start:]
}

func TestDeveloperGuideUsesPATHRCCWhileCIKeepsVerifiedBootstrap(t *testing.T) {
	root := filepath.Clean("..")
	guide := readRepositoryFile(t, root, "docs/skills/testing-release-evidence.md")
	requireContains(t, guide,
		"rcc run -r developer/toolkit.yaml --dev -t local",
		"rcc run -r developer/toolkit.yaml --dev -t test",
		"rcc run -r developer/toolkit.yaml -t package",
		"`local` stops after repository-only candidate smoke verification",
		"`install` verifies that exact candidate before atomically linking it",
		"every gate ledger has that non-empty candidate SHA-256",
	)
	if strings.Contains(guide, "Always enter the factory through `./developer/rccw`") {
		t.Fatal("developer guidance must not require the CI bootstrap wrapper")
	}
}

func TestRCCWrapperRejectsUnsupportedArchitectureBeforeDownload(t *testing.T) {
	root := filepath.Clean("..")
	command := exec.Command(filepath.Join(root, "developer", "rccw"), "version")
	command.Env = append(os.Environ(),
		"CAMP_RCC_HOST_OS=linux",
		"CAMP_RCC_HOST_ARCH=arm64",
		"CAMP_RCC_CACHE_ROOT="+t.TempDir(),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("rccw accepted unsupported architecture:\n%s", output)
	}
	if !strings.Contains(string(output), "unsupported RCC factory host: linux/arm64") {
		t.Fatalf("rccw output = %q", output)
	}
}

func TestRCCWrapperRejectsDigestMismatch(t *testing.T) {
	root := filepath.Clean("..")
	lock := filepath.Join(t.TempDir(), "rcc.lock.yaml")
	if err := os.WriteFile(lock, []byte(`schemaVersion: 1
version: v18.17.7
commit: 2384c4124dadfce48a8eb46cf3fdc3ddebf30e5e
host: linux/amd64
assets:
  linux:
    amd64:
      name: rcc-linux64
      url: https://example.invalid/rcc-linux64
      sha256: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	fakeBin := filepath.Join(cache, "rcc-v18.17.7-linux-amd64")
	if err := os.WriteFile(fakeBin, []byte("#!/bin/sh\nprintf 'v18.17.7\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(filepath.Join(root, "developer", "rccw"), "version")
	command.Env = append(os.Environ(),
		"CAMP_RCC_HOST_OS=linux",
		"CAMP_RCC_HOST_ARCH=amd64",
		"CAMP_RCC_LOCK="+lock,
		"CAMP_RCC_CACHE_ROOT="+cache,
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("rccw accepted digest mismatch:\n%s", output)
	}
	if !strings.Contains(string(output), "RCC digest mismatch") {
		t.Fatalf("rccw output = %q", output)
	}
}

func TestRCCWrapperRejectsHomeInsideRepository(t *testing.T) {
	root := filepath.Clean("..")
	cache := t.TempDir()
	version := "v1.2.3"
	fakeBin := filepath.Join(cache, "rcc-"+version+"-linux-amd64")
	body := []byte("#!/bin/sh\nprintf 'v1.2.3\\n'\n")
	if err := os.WriteFile(fakeBin, body, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	lock := filepath.Join(t.TempDir(), "rcc.lock.yaml")
	lockBody := fmt.Sprintf(`schemaVersion: 1
version: %s
commit: 1111111111111111111111111111111111111111
host: linux/amd64
name: rcc-linux64
url: https://example.invalid/rcc-linux64
sha256: %x
`, version, sum)
	if err := os.WriteFile(lock, []byte(lockBody), 0o600); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(filepath.Join(root, "developer", "rccw"), "version")
	command.Env = append(os.Environ(),
		"CAMP_RCC_HOST_OS=linux",
		"CAMP_RCC_HOST_ARCH=amd64",
		"CAMP_RCC_LOCK="+lock,
		"CAMP_RCC_CACHE_ROOT="+cache,
		"CAMP_RCC_HOME_ROOT="+filepath.Join(root, "build", "forbidden-rcc-home"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("rccw accepted a home inside the repository:\n%s", output)
	}
	if !strings.Contains(string(output), "must remain outside the repository") {
		t.Fatalf("rccw output = %q", output)
	}
}

func TestRealLifecycleCleanupNeverEnumeratesAndDeletesEveryWorkspace(t *testing.T) {
	root := filepath.Clean("..")
	for _, name := range []string{
		"integration/local_lifecycle_test.go",
		"integration/forwarder_crash_test.go",
		"integration/minio_cli_reopen_test.go",
	} {
		body := readRepositoryFile(t, root, name)
		for _, forbidden := range []string{
			"assertNoDevPodWorkspaces(",
			"assertExactlyOneDevPodWorkspace(",
		} {
			if strings.Contains(body, forbidden) {
				t.Errorf("%s contains unsafe global workspace assumption %q", name, forbidden)
			}
		}
	}
}

func TestPullRequestTemplateRequiresEvidenceAndDocumentationReceipt(t *testing.T) {
	body := readRepositoryFile(t, filepath.Clean(".."), ".github/pull_request_template.md")
	requireContains(t, body,
		"## Verification",
		"Passed gates:",
		"Failed gates:",
		"Missing or skipped gates:",
		"Release-note classification:",
		"## Documentation improvement",
		"Canonical file changed or proposed:",
		"Durable learning captured:",
		"Evidence:",
		"Stale or ambiguous guidance removed:",
		"Remaining uncertainty:",
	)
}

func TestPullRequestReceiptVerifierRejectsMissingFields(t *testing.T) {
	root := filepath.Clean("..")
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, []byte(`{"pull_request":{"body":"## Verification\n- Passed gates: unit"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", filepath.Join(root, "developer", "verify_pr_receipt.py"), event)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("receipt verifier accepted incomplete body:\n%s", output)
	}
	if !strings.Contains(string(output), "missing pull request receipt field") {
		t.Fatalf("receipt verifier output = %q", output)
	}
}

func TestPullRequestReceiptVerifierRejectsInvalidReleaseNoteClassification(t *testing.T) {
	root := filepath.Clean("..")
	event := filepath.Join(t.TempDir(), "event.json")
	body := strings.Join([]string{
		"Passed gates: unit",
		"Failed gates: none",
		"Missing or skipped gates: none",
		"Release-note classification: n/a",
		"Canonical file changed or proposed: docs/skills/testing-release-evidence.md",
		"Durable learning captured: receipt validation",
		"Evidence: focused contract test",
		"Stale or ambiguous guidance removed: none",
		"Remaining uncertainty: none",
	}, "\n")
	eventBody, err := json.Marshal(map[string]any{
		"pull_request": map[string]any{"body": body},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(event, eventBody, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("python3", filepath.Join(root, "developer", "verify_pr_receipt.py"), event)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("receipt verifier accepted invalid release-note classification:\n%s", output)
	}
	if !strings.Contains(string(output), "invalid release-note classification") {
		t.Fatalf("receipt verifier output = %q", output)
	}
}

func readRepositoryFile(t *testing.T, root, name string) string {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

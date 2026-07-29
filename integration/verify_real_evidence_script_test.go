package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyRealEvidenceRetriesCrashMatrixWithFreshTestProcess(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(root, "crash-attempts")
	fakeGo := filepath.Join(fakeBin, "go")
	writeTestExecutable(t, fakeGo, `#!/bin/sh
case " $* " in
  *" -list "*)
    printf '%s\n' TestLocalLifecycleVertical TestLocalLifecycleCrashMatrix TestMinIOLifecycleVertical TestS3TwoWriterConflict TestMountedFileBackendParity
    ;;
  *"TestLocalLifecycleVertical"*)
    printf '%s\n' '--- PASS: TestLocalLifecycleVertical'
    ;;
  *"TestLocalLifecycleCrashMatrix"*)
    count=0
    if test -f "$CAMP_FAKE_GO_COUNT"; then count=$(cat "$CAMP_FAKE_GO_COUNT"); fi
    count=$((count + 1))
    printf '%s\n' "$count" > "$CAMP_FAKE_GO_COUNT"
    if test "$count" -eq 1; then
      printf '%s\n' '--- FAIL: TestLocalLifecycleCrashMatrix'
      exit 1
    fi
    printf '%s\n' '--- PASS: TestLocalLifecycleCrashMatrix'
    ;;
  *)
    printf 'unexpected fake go invocation: %s\n' "$*" >&2
    exit 2
    ;;
esac
`)
	candidate := filepath.Join(root, "build", "camp")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, candidate, "#!/bin/sh\nexit 0\n")

	command := exec.Command("bash", "../scripts/verify-real-evidence.sh", "lifecycle")
	command.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"CAMP_FAKE_GO_COUNT="+countPath,
		"CAMP_TEST_BINARY="+candidate,
		"TMPDIR="+root,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("lifecycle evidence script: %v\n%s", err, output)
	}
	count, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("crash-matrix attempts = %q, want 2\n%s", count, output)
	}
}

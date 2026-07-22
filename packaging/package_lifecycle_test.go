package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPackagedCLILifecycle(t *testing.T) {
	repositoryRoot := filepath.Clean("..")
	oldDist := filepath.Join(t.TempDir(), "old")
	newDist := filepath.Join(t.TempDir(), "new")
	buildPackageVersion(t, repositoryRoot, oldDist, "0.0.0")
	buildPackageVersion(t, repositoryRoot, newDist, "0.0.1")

	command := exec.Command("./packaging/fixtures/package-smoke.sh", oldDist, newDist)
	command.Dir = repositoryRoot
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("packaged CLI lifecycle: %v\n%s", err, output)
	}
}

func buildPackageVersion(t *testing.T, repositoryRoot, output, version string) {
	t.Helper()
	command := exec.Command("./packaging/build-packages.sh")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(),
		"VERSION="+version,
		"COMMIT="+testCommit,
		"SOURCE_DATE_EPOCH="+testEpoch,
		"OUTPUT_DIR="+output,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build package version %s: %v\n%s", version, err, output)
	}
}

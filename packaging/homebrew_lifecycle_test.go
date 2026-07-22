package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHomebrewInstallUpgradeUninstall(t *testing.T) {
	repositoryRoot := filepath.Clean("..")
	oldDist := filepath.Join(t.TempDir(), "old")
	newDist := filepath.Join(t.TempDir(), "new")
	buildPackageVersion(t, repositoryRoot, oldDist, "0.0.0")
	buildPackageVersion(t, repositoryRoot, newDist, "0.0.1")

	command := exec.Command("./packaging/fixtures/homebrew-smoke.sh", oldDist, newDist)
	command.Dir = repositoryRoot
	command.Env = os.Environ()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Homebrew lifecycle: %v\n%s", err, output)
	}
}

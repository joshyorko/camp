package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPackageBuildIsReproducible(t *testing.T) {
	repositoryRoot := filepath.Clean("..")
	first := filepath.Join(t.TempDir(), "dist")
	second := filepath.Join(t.TempDir(), "dist")

	buildPackages(t, repositoryRoot, first)
	buildPackages(t, repositoryRoot, second)

	if got, want := directoryDigests(t, first), directoryDigests(t, second); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated package builds differ:\nfirst:  %v\nsecond: %v", got, want)
	}
	for _, name := range []string{
		"camp_0.0.0_linux_amd64.tar.gz",
		"camp_0.0.0_linux_arm64.tar.gz",
		"camp_0.0.0_linux_amd64.deb",
		"camp_0.0.0_linux_arm64.deb",
		"camp_0.0.0_linux_amd64.rpm",
		"camp_0.0.0_linux_arm64.rpm",
		"camp_0.0.0_linux_amd64.apk",
		"camp_0.0.0_linux_arm64.apk",
		"checksums.txt",
	} {
		if _, err := os.Stat(filepath.Join(first, name)); err != nil {
			t.Errorf("artifact %q: %v", name, err)
		}
	}
}

func buildPackages(t *testing.T, repositoryRoot, output string) {
	t.Helper()
	command := exec.Command("./packaging/build-packages.sh")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(),
		"VERSION=0.0.0",
		"COMMIT="+testCommit,
		"SOURCE_DATE_EPOCH="+testEpoch,
		"OUTPUT_DIR="+output,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build packages: %v\n%s", err, output)
	}
}

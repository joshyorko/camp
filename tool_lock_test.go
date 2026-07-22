package camp

import (
	"bytes"
	"testing"

	tooladapter "github.com/joshyorko/camp/internal/adapters/tools"
)

func TestDistributionToolLockEmbedsAuthoritativeRootLock(t *testing.T) {
	lock, err := tooladapter.ParseLock(bytes.NewReader(DistributionToolLock()))
	if err != nil {
		t.Fatalf("ParseLock: %v", err)
	}
	for _, name := range []string{"devpod", "hauler"} {
		for _, arch := range []string{"amd64", "arm64"} {
			if _, _, err := lock.Resolve(name, "linux", arch); err != nil {
				t.Fatalf("Resolve(%s, %s): %v", name, arch, err)
			}
		}
	}
}

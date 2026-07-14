package tools

import (
	"os"
	"testing"
)

func TestCommittedDistributionToolLock(t *testing.T) {
	file, err := os.Open("../../../tools.lock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	lock, err := ParseLock(file)
	if err != nil {
		t.Fatal(err)
	}
	if lock.SchemaVersion != 1 {
		t.Fatalf("schemaVersion = %d, want 1", lock.SchemaVersion)
	}

	tests := []struct {
		name, goos, arch, repository, version, commit, sha256 string
	}{
		{"devpod", "linux", "amd64", "skevetter/devpod", "v0.26.1", "86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e", "01bbc2d88090d546e04aa435c63fc5eb95ec49ffb7ab102a67de0d6d12c82d8d"},
		{"devpod", "linux", "arm64", "skevetter/devpod", "v0.26.1", "86b6f9f5d6713fecdeff5dd240e775a8c7e8d44e", "268621da428ca6d470d1812d63c9e41a1681b681861fc984648a57c5725478ee"},
		{"hauler", "linux", "amd64", "hauler-dev/hauler", "v2.0.1", "4f47155d6f8ccec22ba6f609f2f1f4919b02fce1", "c8b65ea57f10a03f02104ebbecbf1249c41f499bc85771488824b70f6ccfc76c"},
		{"hauler", "linux", "arm64", "hauler-dev/hauler", "v2.0.1", "4f47155d6f8ccec22ba6f609f2f1f4919b02fce1", "ffe9811bbb77f844200f0644b98a08b65ee2d5ffcc0e2b49b1df31719a0a052f"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.arch, func(t *testing.T) {
			tool, asset, err := lock.Resolve(tt.name, tt.goos, tt.arch)
			if err != nil {
				t.Fatal(err)
			}
			if tool.Repository != tt.repository || tool.Version != tt.version || tool.Commit != tt.commit || asset.SHA256 != tt.sha256 {
				t.Fatalf("resolved tool mismatch: tool=%#v asset=%#v", tool, asset)
			}
			if asset.URL == "" {
				t.Fatal("asset URL is empty")
			}
		})
	}

	if lock.Fixtures.Room.Repository != "joshyorko/room-of-requirement" || lock.Fixtures.Room.Version != "v1.18.0" || lock.Fixtures.Room.Commit != "0aabf18ad291c590498bd8e904a7d09f66378b85" {
		t.Fatalf("room fixture mismatch: %#v", lock.Fixtures.Room)
	}
}

func TestDistributionLockRejectsUnsupportedPlatform(t *testing.T) {
	file, err := os.Open("../../../tools.lock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	lock, err := ParseLock(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lock.Resolve("devpod", "windows", "amd64"); err == nil {
		t.Fatal("Resolve accepted unsupported windows platform")
	}
}

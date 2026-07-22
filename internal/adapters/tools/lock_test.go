package tools

import (
	"os"
	"strings"
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
		{"hauler", "linux", "amd64", "hauler-dev/hauler", "v2.0.2", "4ece589a5c763fff15e253735263bd13a889d3cc", "d96ac67cac3c9e4fc2d24c8347fba956b2a165a2237318cc2564e44bbaabc4c3"},
		{"hauler", "linux", "arm64", "hauler-dev/hauler", "v2.0.2", "4ece589a5c763fff15e253735263bd13a889d3cc", "e77a7d2b707ba2ffbb5a69e1f6cacbf046065333cd9b1abe51ed8f9f099c2870"},
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

func TestDistributionLockRejectsMalformedManagedToolIdentitiesAndDestinations(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		repository string
		version    string
		commit     string
		assets     string
	}{
		{name: "unsafe destination name", toolName: "../devpod", repository: "example/devpod", version: "v1", commit: strings.Repeat("a", 40), assets: validAssetYAML()},
		{name: "malformed repository", toolName: "devpod", repository: "example/../devpod", version: "v1", commit: strings.Repeat("a", 40), assets: validAssetYAML()},
		{name: "unsafe version", toolName: "devpod", repository: "example/devpod", version: "../v1", commit: strings.Repeat("a", 40), assets: validAssetYAML()},
		{name: "malformed commit", toolName: "devpod", repository: "example/devpod", version: "v1", commit: "main", assets: validAssetYAML()},
		{name: "no assets", toolName: "devpod", repository: "example/devpod", version: "v1", commit: strings.Repeat("a", 40), assets: "{}"},
		{name: "empty platform", toolName: "devpod", repository: "example/devpod", version: "v1", commit: strings.Repeat("a", 40), assets: "{linux: {}}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			document := "schemaVersion: 1\ntools:\n  " + tt.toolName + ":\n    repository: " + tt.repository + "\n    version: " + tt.version + "\n    commit: " + tt.commit + "\n    assets: " + tt.assets + "\nfixtures:\n  room:\n    repository: example/room\n    version: v1\n    commit: " + strings.Repeat("b", 40) + "\n"
			if _, err := ParseLock(strings.NewReader(document)); err == nil {
				t.Fatal("ParseLock accepted malformed managed tool identity")
			}
		})
	}
}

func TestDistributionLockRejectsDuplicateArchitectureDestination(t *testing.T) {
	document := "schemaVersion: 1\ntools:\n  devpod:\n    repository: example/devpod\n    version: v1\n    commit: " + strings.Repeat("a", 40) + "\n    assets:\n      linux:\n        amd64:\n          url: https://github.com/example/one\n          sha256: " + strings.Repeat("1", 64) + "\n        amd64:\n          url: https://github.com/example/two\n          sha256: " + strings.Repeat("2", 64) + "\nfixtures:\n  room:\n    repository: example/room\n    version: v1\n    commit: " + strings.Repeat("b", 40) + "\n"
	if _, err := ParseLock(strings.NewReader(document)); err == nil {
		t.Fatal("ParseLock accepted duplicate architecture destination")
	}
}

func validAssetYAML() string {
	return "{linux: {amd64: {url: https://github.com/example/devpod, sha256: " + strings.Repeat("1", 64) + "}}}"
}

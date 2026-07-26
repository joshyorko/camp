package releasepipeline_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	releaseVersion = "0.0.0-evidence-test"
	releaseCommit  = "abcdef0123456789abcdef0123456789abcdef01"
	releaseEpoch   = "1784678400"
)

func TestReleaseEvidenceBindsFinalArchivesAndVerifiesNativeInstall(t *testing.T) {
	dist := filepath.Join(t.TempDir(), "downloaded")
	runEvidence(t, "build", dist)

	checksums := readChecksums(t, filepath.Join(dist, "checksums.txt"))
	for _, architecture := range []string{"amd64", "arm64"} {
		archive := "camp_" + releaseVersion + "_linux_" + architecture + ".tar.gz"
		digest := fileSHA256(t, filepath.Join(dist, archive))
		if checksums[archive] != digest {
			t.Fatalf("%s checksum = %q, want %q", archive, checksums[archive], digest)
		}
		sbomPath := filepath.Join(dist, archive+".spdx.json")
		var sbom struct {
			SPDXVersion string `json:"spdxVersion"`
			Packages    []struct {
				Name      string `json:"name"`
				Checksums []struct {
					Algorithm string `json:"algorithm"`
					Value     string `json:"checksumValue"`
				} `json:"checksums"`
			} `json:"packages"`
		}
		decodeJSON(t, sbomPath, &sbom)
		if sbom.SPDXVersion != "SPDX-2.3" || len(sbom.Packages) != 1 || sbom.Packages[0].Name != archive {
			t.Fatalf("unexpected SBOM identity in %s: %#v", sbomPath, sbom)
		}
		if len(sbom.Packages[0].Checksums) != 1 || sbom.Packages[0].Checksums[0].Algorithm != "SHA256" || sbom.Packages[0].Checksums[0].Value != digest {
			t.Fatalf("SBOM %s does not bind final digest %s", sbomPath, digest)
		}
	}

	var evidence struct {
		Commit    string `json:"commit"`
		Version   string `json:"version"`
		Artifacts []struct {
			Platform string `json:"platform"`
			Result   string `json:"result"`
			SHA256   string `json:"sha256"`
			SBOM     string `json:"sbom"`
		} `json:"artifacts"`
		Gates []struct {
			Name   string `json:"name"`
			Result string `json:"result"`
			Reason string `json:"reason"`
		} `json:"gates"`
	}
	decodeJSON(t, filepath.Join(dist, "evidence.json"), &evidence)
	if evidence.Commit != releaseCommit || evidence.Version != releaseVersion || len(evidence.Artifacts) != 2 || len(evidence.Gates) == 0 {
		t.Fatalf("incomplete release evidence: %#v", evidence)
	}
	for _, artifact := range evidence.Artifacts {
		if artifact.Platform == "" || artifact.Result != "built" || artifact.SHA256 == "" || artifact.SBOM == "" {
			t.Fatalf("incomplete artifact evidence: %#v", artifact)
		}
	}
	for _, gate := range evidence.Gates {
		if gate.Result == "gated" && gate.Reason == "" {
			t.Fatalf("gated lane lacks a reason: %#v", gate)
		}
	}

	runEvidence(t, "verify", dist)
	verification, err := os.ReadFile(filepath.Join(dist, "verification-"+runtime.GOARCH+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(verification), `"result":"passed"`) || !strings.Contains(string(verification), releaseCommit) {
		t.Fatalf("native verification is incomplete: %s", verification)
	}
}

func TestVerifiedArtifactManifestBindsBothNativeResultsAndRejectsMutation(t *testing.T) {
	dist := filepath.Join(t.TempDir(), "downloaded")
	runEvidence(t, "build", dist)
	evidence := struct {
		Artifacts []struct {
			Name     string `json:"name"`
			Platform string `json:"platform"`
			SHA256   string `json:"sha256"`
		} `json:"artifacts"`
	}{}
	decodeJSON(t, filepath.Join(dist, "evidence.json"), &evidence)
	for _, artifact := range evidence.Artifacts {
		architecture := strings.TrimPrefix(artifact.Platform, "linux/")
		body := map[string]string{
			"commit":   releaseCommit,
			"platform": artifact.Platform,
			"artifact": artifact.Name,
			"sha256":   artifact.SHA256,
			"result":   "passed",
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(dist, "verification-"+architecture+".json"),
			append(encoded, '\n'),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}

	runVerifiedArtifacts(t, "create", dist, true)
	var manifest struct {
		Candidate struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		} `json:"candidate"`
		Artifacts []struct {
			Architecture       string `json:"architecture"`
			Path               string `json:"path"`
			Size               int64  `json:"size"`
			SHA256             string `json:"sha256"`
			Verification       string `json:"verification"`
			VerificationSHA256 string `json:"verificationSha256"`
			Result             string `json:"result"`
		} `json:"artifacts"`
		VerificationResult string `json:"verificationResult"`
	}
	decodeJSON(t, filepath.Join(dist, "verified-artifacts.json"), &manifest)
	if manifest.Candidate.Version != releaseVersion ||
		manifest.Candidate.Commit != releaseCommit ||
		manifest.VerificationResult != "passed" ||
		len(manifest.Artifacts) != 2 {
		t.Fatalf("incomplete verified-artifact manifest: %#v", manifest)
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Architecture == "" || artifact.Path == "" || artifact.Size <= 0 ||
			artifact.SHA256 == "" || artifact.Verification == "" ||
			artifact.VerificationSHA256 == "" || artifact.Result != "passed" {
			t.Fatalf("incomplete verified artifact: %#v", artifact)
		}
	}
	runVerifiedArtifacts(t, "recheck", dist, true)

	archive := filepath.Join(dist, manifest.Artifacts[0].Path)
	file, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("mutation"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	runVerifiedArtifacts(t, "recheck", dist, false)
}

func runEvidence(t *testing.T, mode, dist string) {
	t.Helper()
	command := exec.Command("./packaging/build-release-evidence.sh", mode)
	command.Dir = ".."
	command.Env = append(os.Environ(),
		"VERSION="+releaseVersion,
		"COMMIT="+releaseCommit,
		"SOURCE_DATE_EPOCH="+releaseEpoch,
		"OUTPUT_DIR="+dist,
		"VERIFY_ARCH="+runtime.GOARCH,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release evidence %s: %v\n%s", mode, err, output)
	}
}

func runVerifiedArtifacts(t *testing.T, mode, dist string, wantSuccess bool) {
	t.Helper()
	command := exec.Command("python3", "./packaging/verified_artifacts.py", mode, dist)
	command.Dir = ".."
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("verified artifacts %s: %v\n%s", mode, err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("verified artifacts %s accepted mutated archive:\n%s", mode, output)
	}
}

func readChecksums(t *testing.T, path string) map[string]string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Fatalf("malformed checksum line %q", line)
		}
		result[fields[1]] = fields[0]
	}
	return result
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func decodeJSON(t *testing.T, path string, target any) {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(contents, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

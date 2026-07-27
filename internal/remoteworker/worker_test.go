package remoteworker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestProbeReportsTypedCapabilitiesAfterVerifyingInputs(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "manifest.json")
	kit := filepath.Join(root, "camp-hauler-kit.tar.zst")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kit, []byte("kit"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.WorkspaceRoot = root
	request.RuntimeRoot = runtimeRoot
	request.ManifestPath = manifest
	request.Expected.Architecture = "linux/" + runtime.GOARCH
	request.Expected.Helper = identityFor(t, "camp", executable)
	request.Expected.Kit = identityFor(t, filepath.Base(kit), kit)
	request.Expected.Manifest = identityFor(t, filepath.Base(manifest), manifest)
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	runErr := Run(t.Context(), bytes.NewReader(body), &output, &bytes.Buffer{})
	if runErr != nil && !errors.Is(runErr, ErrUnsupportedCapability) {
		t.Fatalf("Run() error = %v", runErr)
	}
	var result Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var receipt ProbeReceipt
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Architecture != request.Expected.Architecture || len(receipt.Capabilities) != 6 {
		t.Fatalf("probe receipt = %#v", receipt)
	}
	for _, capability := range receipt.Capabilities {
		if capability.Name == "" || (capability.Status != "supported" && capability.Status != "unsupported") {
			t.Fatalf("capability = %#v", capability)
		}
	}
}

func TestProbeRejectsChangedKitBeforeCapabilityChecks(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "manifest.json")
	kit := filepath.Join(root, "camp-hauler-kit.tar.zst")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(kit, []byte("kit"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	request := validRequest()
	request.WorkspaceRoot = root
	request.RuntimeRoot = root
	request.ManifestPath = manifest
	request.Expected.Architecture = "linux/" + runtime.GOARCH
	request.Expected.Helper = identityFor(t, "camp", executable)
	request.Expected.Kit = identityFor(t, filepath.Base(kit), kit)
	request.Expected.Manifest = identityFor(t, filepath.Base(manifest), manifest)
	request.Expected.Kit.SHA256 = string(bytes.Repeat([]byte("0"), 64))
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(t.Context(), bytes.NewReader(body), &output, &bytes.Buffer{}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Run() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q", output.String())
	}
}

func identityFor(t *testing.T, name, path string) FileIdentity {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	return FileIdentity{Name: name, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
}

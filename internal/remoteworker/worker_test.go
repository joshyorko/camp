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

	"golang.org/x/sys/unix"
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

func TestLoopbackProbeBindsWithoutListening(t *testing.T) {
	fd, err := openLoopbackProbeSocket()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	accepting, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	if err != nil {
		t.Fatal(err)
	}
	if accepting != 0 {
		t.Fatal("loopback probe socket is listening")
	}
	address, err := unix.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	inet, ok := address.(*unix.SockaddrInet4)
	if !ok || inet.Port == 0 || inet.Addr != [4]byte{127, 0, 0, 1} {
		t.Fatalf("loopback probe address = %#v", address)
	}
}

func TestPrivilegeProbeAcceptsCurrentUserWhenRequiredOperationsWork(t *testing.T) {
	if err := probeNamespaces(); err != nil {
		t.Skipf("user/network namespace operation unavailable: %v", err)
	}
	if err := probeTUN(); err != nil {
		t.Skipf("TUN operation unavailable: %v", err)
	}
	if err := probePrivilege(); err != nil {
		t.Fatalf("operation-capable user was rejected: %v", err)
	}
}

func TestPrivilegeReceiptDerivesFromOperationResultsWithoutCapabilityGate(t *testing.T) {
	if err := privilegeFromOperations(nil, nil); err != nil {
		t.Fatalf("successful operations were rejected: %v", err)
	}
	namespaceErr := errors.New("namespace operation failed")
	if err := privilegeFromOperations(namespaceErr, nil); !errors.Is(err, namespaceErr) {
		t.Fatalf("namespace error = %v", err)
	}
	tunErr := errors.New("TUN operation failed")
	if err := privilegeFromOperations(nil, tunErr); !errors.Is(err, tunErr) {
		t.Fatalf("TUN error = %v", err)
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
	var result Result
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	var receipt ErrorReceipt
	if err := json.Unmarshal(result.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if result.Operation != OperationProbe || receipt.Status != "error" || receipt.Code != "identity_mismatch" {
		t.Fatalf("result=%#v receipt=%#v", result, receipt)
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

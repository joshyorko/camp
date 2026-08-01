package remoteworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/haulkit"
	"golang.org/x/sys/unix"
)

type recordingWorkerOperations struct {
	activated    bool
	hydrated     bool
	started      bool
	observed     bool
	checkpointed bool
}

type activationFixture struct {
	order []string
	id    string
}

func (fixture *activationFixture) Verify(context.Context, Request) (verifiedRuntimeKit, error) {
	fixture.order = append(fixture.order, "verify")
	return verifiedRuntimeKit{Store: "/runtime/store"}, nil
}

func (fixture *activationFixture) StartRegistry(context.Context, Request, verifiedRuntimeKit) (temporaryRegistry, error) {
	fixture.order = append(fixture.order, "registry")
	return temporaryRegistry{Reference: "127.0.0.1:45000/camp/workspace@sha256:" + strings.Repeat("c", 64), Stop: func() error {
		fixture.order = append(fixture.order, "stop")
		return nil
	}}, nil
}

func (fixture *activationFixture) PullAndInspect(_ context.Context, reference string) (string, error) {
	fixture.order = append(fixture.order, "pull:"+reference)
	return fixture.id, nil
}

func (fixture *activationFixture) Publish(_ Request, receipt ActivationReceipt) error {
	fixture.order = append(fixture.order, "receipt:"+receipt.Status)
	return nil
}

func TestActivateImageVerifiesBeforeRegistryAndRequiresExactLocalImageID(t *testing.T) {
	request := validRequest()
	request.Operation = OperationActivateImage
	fixture := &activationFixture{id: request.Expected.Image}
	receipt, err := activateImage(t.Context(), request, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "completed" ||
		strings.Join(fixture.order, ",") != "verify,registry,pull:127.0.0.1:45000/camp/workspace@sha256:"+strings.Repeat("c", 64)+",stop,receipt:completed" {
		t.Fatalf("receipt=%#v order=%v", receipt, fixture.order)
	}

	fixture = &activationFixture{id: "sha256:" + strings.Repeat("f", 64)}
	if _, err := activateImage(t.Context(), request, fixture); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("activateImage() error = %v", err)
	}
	for _, item := range fixture.order {
		if strings.HasPrefix(item, "receipt:") {
			t.Fatalf("mismatch published success receipt: %v", fixture.order)
		}
	}
}

func TestProviderEngineFallsBackToPodman(t *testing.T) {
	directory := t.TempDir()
	podman := filepath.Join(directory, "podman")
	if err := os.WriteFile(podman, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	engine, err := providerEngine()
	if err != nil {
		t.Fatal(err)
	}
	if engine != podman {
		t.Fatalf("providerEngine() = %q, want %q", engine, podman)
	}
}

func TestPodmanActivationPullUsesLoopbackRegistryWithoutTLS(t *testing.T) {
	directory := t.TempDir()
	podman := filepath.Join(directory, "podman")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = pull ]; then [ \"$2\" = --tls-verify=false ] || exit 125; exit 0; fi\n" +
		"if [ \"$1\" = image ] && [ \"$2\" = inspect ]; then printf '" + strings.Repeat("a", 64) + "\\n'; exit 0; fi\n" +
		"exit 64\n"
	if err := os.WriteFile(podman, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	got, err := newProductionActivationRuntime().PullAndInspect(t.Context(), "127.0.0.1:46003/podman/stable@sha256:"+strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if got != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("local image ID = %q", got)
	}
}

func TestActivationUsesPlatformManifestDigestStoredByHauler(t *testing.T) {
	source := "quay.io/podman/stable@sha256:" + strings.Repeat("a", 64)
	digest, err := activationImageDigest(haulkit.StoreIdentity{Entries: []haulkit.StoreEntry{{
		Reference: source, Type: "image", Platform: "linux/amd64", Digest: strings.Repeat("b", 64),
	}}}, source, "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	if digest != "sha256:"+strings.Repeat("b", 64) {
		t.Fatalf("activation digest = %q", digest)
	}
}

func (operations *recordingWorkerOperations) ActivateImage(context.Context, Request) (any, error) {
	operations.activated = true
	return map[string]string{"status": "completed"}, nil
}

func (operations *recordingWorkerOperations) Hydrate(context.Context, Request) (any, error) {
	operations.hydrated = true
	return map[string]string{"status": "completed"}, nil
}

func (operations *recordingWorkerOperations) StartServices(context.Context, Request) (any, error) {
	operations.started = true
	return map[string]string{"status": "ready"}, nil
}

func (operations *recordingWorkerOperations) Observe(context.Context, Request) (any, error) {
	operations.observed = true
	return map[string]string{"status": "ready"}, nil
}

func (operations *recordingWorkerOperations) Checkpoint(context.Context, Request) (any, error) {
	operations.checkpointed = true
	return map[string]string{"status": "prepared"}, nil
}

func TestRunDispatchesRemoteLifecycleOperations(t *testing.T) {
	for _, operation := range []Operation{OperationActivateImage, OperationHydrate, OperationStartServices, OperationObserve, OperationCheckpoint} {
		t.Run(string(operation), func(t *testing.T) {
			request := validRequest()
			request.Operation = operation
			if operation == OperationCheckpoint {
				request.Checkpoint = checkpointRequest(false).Checkpoint
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			operations := &recordingWorkerOperations{}
			var output bytes.Buffer
			runErr := runWithOperations(t.Context(), bytes.NewReader(body), &output, operations)
			if runErr != nil {
				t.Fatal(runErr)
			}
			if operations.activated != (operation == OperationActivateImage) ||
				operations.hydrated != (operation == OperationHydrate) ||
				operations.started != (operation == OperationStartServices) ||
				operations.observed != (operation == OperationObserve) ||
				operations.checkpointed != (operation == OperationCheckpoint) {
				t.Fatalf("dispatch activate=%v hydrate=%v start=%v observe=%v checkpoint=%v", operations.activated, operations.hydrated, operations.started, operations.observed, operations.checkpointed)
			}
			var result Result
			if err := json.Unmarshal(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Operation != operation {
				t.Fatalf("result operation = %q", result.Operation)
			}
		})
	}
}

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

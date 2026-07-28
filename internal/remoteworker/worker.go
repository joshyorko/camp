package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"golang.org/x/sys/unix"
)

type CapabilityReceipt struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Diagnostic string `json:"diagnostic,omitempty"`
}

type ProbeReceipt struct {
	Status       string              `json:"status"`
	Architecture string              `json:"architecture"`
	Capabilities []CapabilityReceipt `json:"capabilities"`
}

type workerOperations interface {
	ActivateImage(context.Context, Request) (any, error)
	Hydrate(context.Context, Request) (any, error)
	StartServices(context.Context, Request) (any, error)
	Checkpoint(context.Context, Request) (any, error)
}

func Run(ctx context.Context, input io.Reader, output, _ io.Writer) error {
	return runWithOperations(ctx, input, output, productionOperations{})
}

func runWithOperations(ctx context.Context, input io.Reader, output io.Writer, operations workerOperations) error {
	request, err := DecodeRequest(input)
	if err != nil {
		if encodeErr := encodeErrorResult(output, OperationRejected, err); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
		return err
	}
	if request.Operation == OperationProbe {
		return runProbe(ctx, request, output)
	}
	var receipt any
	switch request.Operation {
	case OperationActivateImage:
		receipt, err = operations.ActivateImage(ctx, request)
	case OperationHydrate:
		receipt, err = operations.Hydrate(ctx, request)
	case OperationStartServices:
		receipt, err = operations.StartServices(ctx, request)
	case OperationCheckpoint:
		receipt, err = operations.Checkpoint(ctx, request)
	default:
		err = ErrUnsupportedOperation
	}
	if err == nil {
		return encodeResult(output, request.Operation, receipt)
	}
	if !errors.Is(err, ErrUnsupportedOperation) {
		if encodeErr := encodeErrorResult(output, request.Operation, err); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
		return err
	}
	err = ErrUnsupportedOperation
	unsupported := UnsupportedReceipt{Status: "unsupported", Diagnostic: boundedDiagnostic(err)}
	if encodeErr := encodeResult(output, request.Operation, unsupported); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runProbe(_ context.Context, request Request, output io.Writer) error {
	if err := verifyProbeInputs(request); err != nil {
		if encodeErr := encodeErrorResult(output, request.Operation, err); encodeErr != nil {
			return errors.Join(err, encodeErr)
		}
		return err
	}
	architecture := "linux/" + runtime.GOARCH
	namespaceErr := probeNamespaces()
	tunErr := probeTUN()
	capabilities := []CapabilityReceipt{
		capability("architecture", func() error {
			if runtime.GOOS != "linux" || architecture != request.Expected.Architecture {
				return fmt.Errorf("running architecture is %s/%s", runtime.GOOS, runtime.GOARCH)
			}
			return nil
		}),
		capability("filesystem", func() error { return probeFilesystem(request.RuntimeRoot) }),
		capabilityFromError("namespaces", namespaceErr),
		capabilityFromError("tun", tunErr),
		capabilityFromError("privilege", privilegeFromOperations(namespaceErr, tunErr)),
		capability("loopback-port", probeLoopbackPort),
	}
	status := "supported"
	var probeErr error
	for _, item := range capabilities {
		if item.Status != "supported" {
			status = "unsupported"
			probeErr = ErrUnsupportedCapability
		}
	}
	if err := encodeResult(output, request.Operation, ProbeReceipt{
		Status: status, Architecture: architecture, Capabilities: capabilities,
	}); err != nil {
		return err
	}
	return probeErr
}

func encodeErrorResult(output io.Writer, operation Operation, err error) error {
	code := "remote_worker_failed"
	switch {
	case errors.Is(err, ErrInvalidRequest):
		code = "invalid_request"
	case errors.Is(err, ErrIdentityMismatch):
		code = "identity_mismatch"
	case errors.Is(err, ErrUnsupportedCapability):
		code = "unsupported_capability"
	}
	return encodeResult(output, operation, ErrorReceipt{
		Status: "error", Code: code, Diagnostic: boundedDiagnostic(err),
	})
}

func verifyProbeInputs(request Request) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for path, expected := range map[string]FileIdentity{
		executable: request.Expected.Helper,
		filepath.Join(filepath.Dir(request.ManifestPath), "camp-hauler-kit.tar.zst"): request.Expected.Kit,
		request.ManifestPath: request.Expected.Manifest,
	} {
		observed, err := observeFile(expected.Name, path)
		if err != nil {
			return err
		}
		if observed != expected {
			return fmt.Errorf("%w: %s", ErrIdentityMismatch, expected.Name)
		}
	}
	for _, root := range []string{request.WorkspaceRoot, request.RuntimeRoot} {
		info, err := os.Lstat(root)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unsafe root %q", ErrInvalidRequest, root)
		}
	}
	return nil
}

func observeFile(name, path string) (FileIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return FileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() {
		return FileIdentity{}, fmt.Errorf("%w: %q is not a regular file", ErrIdentityMismatch, path)
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return FileIdentity{}, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || size != before.Size() {
		return FileIdentity{}, fmt.Errorf("%w: %q changed while observed", ErrIdentityMismatch, path)
	}
	return FileIdentity{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func capability(name string, probe func() error) CapabilityReceipt {
	if err := probe(); err != nil {
		return CapabilityReceipt{Name: name, Status: "unsupported", Diagnostic: boundedDiagnostic(err)}
	}
	return CapabilityReceipt{Name: name, Status: "supported"}
}

func capabilityFromError(name string, err error) CapabilityReceipt {
	if err != nil {
		return CapabilityReceipt{Name: name, Status: "unsupported", Diagnostic: boundedDiagnostic(err)}
	}
	return CapabilityReceipt{Name: name, Status: "supported"}
}

func probeFilesystem(root string) error {
	file, err := os.CreateTemp(root, ".camp-probe-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func probeNamespaces() error {
	command := exec.Command("/bin/true")
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 unix.CLONE_NEWUSER | unix.CLONE_NEWNET | unix.CLONE_NEWNS,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
	}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("create user, network, and mount namespaces: %w: %s", err, output)
	}
	return nil
}

func probeTUN() error {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if _, err := unix.IoctlGetInt(fd, unix.TUNGETFEATURES); err != nil {
		return err
	}
	return nil
}

func probePrivilege() error {
	return privilegeFromOperations(probeNamespaces(), probeTUN())
}

func privilegeFromOperations(namespaceErr, tunErr error) error {
	if namespaceErr != nil {
		return namespaceErr
	}
	if tunErr != nil {
		return tunErr
	}
	return nil
}

func openLoopbackProbeSocket() (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return -1, err
	}
	address := &unix.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}
	if err := unix.Bind(fd, address); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

func probeLoopbackPort() error {
	fd, err := openLoopbackProbeSocket()
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"

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

func Run(ctx context.Context, input io.Reader, output, _ io.Writer) error {
	request, err := DecodeRequest(input)
	if err != nil {
		return err
	}
	if request.Operation == OperationProbe {
		return runProbe(ctx, request, output)
	}
	err = ErrUnsupportedOperation
	receipt := UnsupportedReceipt{Status: "unsupported", Diagnostic: boundedDiagnostic(err)}
	if encodeErr := encodeResult(output, request.Operation, receipt); encodeErr != nil {
		return encodeErr
	}
	return err
}

func runProbe(_ context.Context, request Request, output io.Writer) error {
	if err := verifyProbeInputs(request); err != nil {
		return err
	}
	architecture := "linux/" + runtime.GOARCH
	capabilities := []CapabilityReceipt{
		capability("architecture", func() error {
			if runtime.GOOS != "linux" || architecture != request.Expected.Architecture {
				return fmt.Errorf("running architecture is %s/%s", runtime.GOOS, runtime.GOARCH)
			}
			return nil
		}),
		capability("filesystem", func() error { return probeFilesystem(request.RuntimeRoot) }),
		capability("namespaces", probeNamespaces),
		capability("tun", probeTUN),
		capability("privilege", probePrivilege),
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
	for _, name := range []string{"mnt", "net", "user"} {
		if _, err := os.Readlink(filepath.Join("/proc/self/ns", name)); err != nil {
			return err
		}
	}
	return nil
}

func probeTUN() error {
	info, err := os.Stat("/dev/net/tun")
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeDevice == 0 {
		return errors.New("/dev/net/tun is not a device")
	}
	return nil
}

func probePrivilege() error {
	if os.Geteuid() != 0 {
		return errors.New("effective user is not root")
	}
	return nil
}

func probeLoopbackPort() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	return listener.Close()
}

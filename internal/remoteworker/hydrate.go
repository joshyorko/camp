package remoteworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/haulkit"
	"golang.org/x/sys/unix"
)

var ErrUnsafeHydration = errors.New("unsafe remote workspace hydration")

type HydrationReceipt struct {
	Status        string `json:"status"`
	WorkspaceRoot string `json:"workspaceRoot"`
	RootSHA256    string `json:"rootSHA256"`
}

type hydrationRuntime interface {
	Verify(context.Context, Request) (verifiedRuntimeKit, error)
	ExtractRoot(context.Context, Request, verifiedRuntimeKit) (string, error)
	InstallTools(Request, verifiedRuntimeKit) error
	Promote(string, string) error
	Publish(Request, HydrationReceipt) error
}

func hydrateWorkspace(ctx context.Context, request Request, runtime hydrationRuntime) (HydrationReceipt, error) {
	kit, err := runtime.Verify(ctx, request)
	if err != nil {
		return HydrationReceipt{}, err
	}
	if observer, ok := runtime.(interface {
		Observe(Request, verifiedRuntimeKit) (HydrationReceipt, bool, error)
	}); ok {
		if receipt, complete, err := observer.Observe(request, kit); err != nil || complete {
			return receipt, err
		}
	}
	stage, err := runtime.ExtractRoot(ctx, request, kit)
	if err != nil {
		return HydrationReceipt{}, err
	}
	if err := runtime.InstallTools(request, kit); err != nil {
		return HydrationReceipt{}, err
	}
	if err := runtime.Promote(stage, request.WorkspaceRoot); err != nil {
		return HydrationReceipt{}, err
	}
	receipt := HydrationReceipt{Status: "completed", WorkspaceRoot: request.WorkspaceRoot, RootSHA256: kit.RootSHA256}
	if err := runtime.Publish(request, receipt); err != nil {
		return HydrationReceipt{}, err
	}
	return receipt, nil
}

type productionHydrationRuntime struct {
	activation *productionActivationRuntime
	manifest   haulkit.Manifest
}

func newProductionHydrationRuntime() *productionHydrationRuntime {
	return &productionHydrationRuntime{activation: newProductionActivationRuntime()}
}

func (runtimeState *productionHydrationRuntime) Verify(ctx context.Context, request Request) (verifiedRuntimeKit, error) {
	kit, err := runtimeState.activation.Verify(ctx, request)
	if err != nil {
		return verifiedRuntimeKit{}, err
	}
	body, err := os.ReadFile(request.ManifestPath)
	if err != nil {
		return verifiedRuntimeKit{}, err
	}
	runtimeState.manifest, err = haulkit.DecodeCanonical(body)
	return kit, err
}

func (*productionHydrationRuntime) Observe(request Request, kit verifiedRuntimeKit) (HydrationReceipt, bool, error) {
	path := filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hydrate.receipt.json")
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if isNotExist(err) {
		return HydrationReceipt{}, false, nil
	}
	if err != nil {
		return HydrationReceipt{}, false, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxDiagnosticBytes+1))
	if err != nil || len(body) > maxDiagnosticBytes {
		return HydrationReceipt{}, false, errors.Join(err, ErrUnsafeHydration)
	}
	var receipt HydrationReceipt
	if err := json.Unmarshal(body, &receipt); err != nil ||
		receipt.Status != "completed" || receipt.WorkspaceRoot != request.WorkspaceRoot ||
		receipt.RootSHA256 != kit.RootSHA256 {
		return HydrationReceipt{}, false, fmt.Errorf("%w: hydration receipt differs", ErrUnsafeHydration)
	}
	return receipt, true, nil
}

func (runtimeState *productionHydrationRuntime) ExtractRoot(ctx context.Context, request Request, kit verifiedRuntimeKit) (string, error) {
	ready := filepath.Dir(kit.Store)
	hauler := hauleradapter.NewClient(filepath.Join(ready, "bin", "hauler"), subprocess.NewRunner())
	rootArchive := filepath.Join(request.RuntimeRoot, "root.tar.zst")
	if _, err := os.Lstat(rootArchive); err == nil {
		observed, observeErr := observeFile(filepath.Base(rootArchive), rootArchive)
		if observeErr != nil || observed.SHA256 != runtimeState.manifest.Root.SHA256 || observed.Size != runtimeState.manifest.Root.Size {
			return "", fmt.Errorf("%w: existing root artifact", ErrIdentityMismatch)
		}
	} else if isNotExist(err) {
		result, extractErr := hauler.Extract(ctx, kit.Store, runtimeState.manifest.Root.Reference, rootArchive)
		if extractErr != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("extract root artifact: %w", extractErr)
		}
		observed, observeErr := observeFile(filepath.Base(rootArchive), rootArchive)
		if observeErr != nil || observed.SHA256 != runtimeState.manifest.Root.SHA256 || observed.Size != runtimeState.manifest.Root.Size {
			return "", fmt.Errorf("%w: extracted root artifact", ErrIdentityMismatch)
		}
	} else {
		return "", err
	}
	stage := filepath.Join(request.RuntimeRoot, "root-stage")
	if _, err := os.Lstat(stage); err == nil {
		return "", fmt.Errorf("%w: unexplained root stage", ErrUnsafeHydration)
	} else if !isNotExist(err) {
		return "", err
	}
	if err := archiveadapter.NewTarZstd().Extract(ctx, rootArchive, stage); err != nil {
		return "", err
	}
	return stage, nil
}

func (runtimeState *productionHydrationRuntime) InstallTools(request Request, kit verifiedRuntimeKit) error {
	runtimeRoot := filepath.Join(request.WorkspaceRoot, ".camp", "runtime")
	if err := secureMkdirAllOperation(runtimeRoot); err != nil {
		return err
	}
	for name, identity := range map[string]haulkit.FileIdentity{
		"camp":   runtimeState.manifest.Tools.Camp,
		"hauler": runtimeState.manifest.Tools.Hauler,
		"pasta":  runtimeState.manifest.Tools.Pasta,
	} {
		source := filepath.Join(filepath.Dir(kit.Store), "bin", name)
		file, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(file, identity.Size+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || int64(len(body)) != identity.Size {
			return errors.Join(readErr, closeErr, ErrIdentityMismatch)
		}
		observed, err := observeFile(name, source)
		if err != nil || observed.SHA256 != identity.SHA256 || observed.Size != identity.Size {
			return fmt.Errorf("%w: runtime tool %s changed", ErrIdentityMismatch, name)
		}
		expected := FileIdentity{Name: name, SHA256: identity.SHA256, Size: identity.Size}
		if err := publishStableBytes(filepath.Join(runtimeRoot, name), body, expected); err != nil {
			return err
		}
		if err := os.Chmod(filepath.Join(runtimeRoot, name), 0o500); err != nil {
			return err
		}
	}
	return syncOperationDirectory(runtimeRoot)
}

func (*productionHydrationRuntime) Promote(stage, workspace string) error {
	return promoteHydratedRoot(stage, workspace, nil)
}

func (*productionHydrationRuntime) Publish(request Request, receipt HydrationReceipt) error {
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return publishReceipt(filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hydrate.receipt.json"), append(body, '\n'))
}

func promoteHydratedRoot(stage, workspace string, boundary func(string) error) error {
	stageFD, stageStat, err := openOperationDirectory(stage)
	if err != nil {
		return fmt.Errorf("%w: open root stage: %v", ErrUnsafeHydration, err)
	}
	defer unix.Close(stageFD)
	workspaceFD, workspaceStat, err := openOperationDirectory(workspace)
	if err != nil {
		return fmt.Errorf("%w: open workspace: %v", ErrUnsafeHydration, err)
	}
	defer unix.Close(workspaceFD)
	if stageStat.Dev != workspaceStat.Dev {
		return fmt.Errorf("%w: stage and workspace cross devices", ErrUnsafeHydration)
	}
	if err := validateInitialWorkspace(workspaceFD); err != nil {
		return err
	}
	names, err := readDirectoryNames(stageFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == ".camp-bootstrap" {
			return fmt.Errorf("%w: root archive contains reserved top-level entry %q", ErrUnsafeHydration, name)
		}
		if name == ".camp" {
			if err := promoteCampDirectory(stageFD, workspaceFD, boundary); err != nil {
				return err
			}
			continue
		}
		if boundary != nil {
			if err := boundary("promotion-before-rename"); err != nil {
				return err
			}
		}
		if err := unix.Renameat2(stageFD, name, workspaceFD, name, unix.RENAME_NOREPLACE); err != nil {
			return fmt.Errorf("%w: promote %q: %v", ErrUnsafeHydration, name, err)
		}
		if err := unix.Fsync(workspaceFD); err != nil {
			return fmt.Errorf("sync promoted workspace entry %q: %w", name, err)
		}
		if err := unix.Fsync(stageFD); err != nil {
			return fmt.Errorf("sync consumed root stage entry %q: %w", name, err)
		}
	}
	return nil
}

func promoteCampDirectory(stageFD, workspaceFD int, boundary func(string) error) error {
	stageCampFD, err := unix.Openat(stageFD, ".camp", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: staged .camp is unsafe: %v", ErrUnsafeHydration, err)
	}
	defer unix.Close(stageCampFD)
	workspaceCampFD, err := unix.Openat(workspaceFD, ".camp", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, syscall.ENOENT) {
		if err := unix.Mkdirat(workspaceFD, ".camp", 0o700); err != nil {
			return err
		}
		if err := unix.Fsync(workspaceFD); err != nil {
			return err
		}
		workspaceCampFD, err = unix.Openat(workspaceFD, ".camp", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	}
	if err != nil {
		return fmt.Errorf("%w: workspace .camp is unsafe: %v", ErrUnsafeHydration, err)
	}
	defer unix.Close(workspaceCampFD)
	names, err := readDirectoryNames(stageCampFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "runtime" {
			return fmt.Errorf("%w: root archive contains reserved .camp/runtime", ErrUnsafeHydration)
		}
		if boundary != nil {
			if err := boundary("promotion-before-camp-rename"); err != nil {
				return err
			}
		}
		if err := unix.Renameat2(stageCampFD, name, workspaceCampFD, name, unix.RENAME_NOREPLACE); err != nil {
			return fmt.Errorf("%w: promote .camp/%s: %v", ErrUnsafeHydration, name, err)
		}
		if err := unix.Fsync(workspaceCampFD); err != nil {
			return err
		}
		if err := unix.Fsync(stageCampFD); err != nil {
			return err
		}
	}
	if err := unix.Unlinkat(stageFD, ".camp", unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(stageFD)
}

func openOperationDirectory(path string) (int, unix.Stat_t, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, unix.Stat_t{}, err
	}
	var named unix.Stat_t
	if err := unix.Lstat(path, &named); err != nil || named.Mode&unix.S_IFMT != unix.S_IFDIR ||
		named.Dev != stat.Dev || named.Ino != stat.Ino {
		unix.Close(fd)
		return -1, unix.Stat_t{}, fmt.Errorf("directory path identity changed")
	}
	return fd, stat, nil
}

func validateInitialWorkspace(workspaceFD int) error {
	names, err := readDirectoryNames(workspaceFD)
	if err != nil {
		return err
	}
	for _, name := range names {
		switch name {
		case ".camp-bootstrap":
			if err := requireDirectoryAt(workspaceFD, name); err != nil {
				return err
			}
		case ".camp":
			campFD, err := unix.Openat(workspaceFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				return fmt.Errorf("%w: reserved .camp is unsafe: %v", ErrUnsafeHydration, err)
			}
			children, readErr := readDirectoryNames(campFD)
			if readErr == nil && (len(children) != 1 || children[0] != "runtime") {
				readErr = fmt.Errorf("%w: .camp contains entries other than runtime", ErrUnsafeHydration)
			}
			if readErr == nil {
				readErr = requireDirectoryAt(campFD, "runtime")
			}
			closeErr := unix.Close(campFD)
			if readErr != nil || closeErr != nil {
				return errors.Join(readErr, closeErr)
			}
		default:
			return fmt.Errorf("%w: unexpected initial workspace entry %q", ErrUnsafeHydration, name)
		}
	}
	return nil
}

func requireDirectoryAt(parentFD int, name string) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: reserved directory %q is unsafe: %v", ErrUnsafeHydration, name, err)
	}
	return unix.Close(fd)
}

func readDirectoryNames(fd int) ([]string, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	directory := os.NewFile(uintptr(dup), "remote-worker-directory")
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || name == "." || name == ".." {
			return nil, fmt.Errorf("%w: invalid directory entry", ErrUnsafeHydration)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func writeAllAt(file *os.File, body []byte) error {
	for len(body) > 0 {
		n, err := file.Write(body)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		body = body[n:]
	}
	return nil
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT)
}

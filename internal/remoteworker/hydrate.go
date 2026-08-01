package remoteworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/jsonstrict"
	"golang.org/x/sys/unix"
)

var ErrUnsafeHydration = errors.New("unsafe remote workspace hydration")

type HydrationReceipt struct {
	Status        string           `json:"status"`
	SessionID     string           `json:"sessionId"`
	WorkspaceRoot string           `json:"workspaceRoot"`
	RuntimeRoot   string           `json:"runtimeRoot"`
	ManifestPath  string           `json:"manifestPath"`
	Expected      ExpectedIdentity `json:"expected"`
	RootSHA256    string           `json:"rootSHA256"`
}

type hydrationRuntime interface {
	Verify(context.Context, Request) (verifiedRuntimeKit, error)
	AdmitWorkspace(Request) error
	ExtractRoot(context.Context, Request, verifiedRuntimeKit) (string, error)
	InstallTools(Request, verifiedRuntimeKit) error
	Promote(string, string) error
	Publish(Request, HydrationReceipt) error
}

func hydrateWorkspace(ctx context.Context, request Request, runtime hydrationRuntime) (HydrationReceipt, error) {
	if observer, ok := runtime.(interface {
		ObserveCompleted(Request) (HydrationReceipt, bool, error)
	}); ok {
		if receipt, complete, err := observer.ObserveCompleted(request); err != nil || complete {
			return receipt, err
		}
	}
	if err := runtime.AdmitWorkspace(request); err != nil {
		return HydrationReceipt{}, err
	}
	kit, err := runtime.Verify(ctx, request)
	if err != nil {
		return HydrationReceipt{}, err
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
	receipt := newHydrationReceipt(request, kit.RootSHA256)
	if err := runtime.Publish(request, receipt); err != nil {
		return HydrationReceipt{}, err
	}
	return receipt, nil
}

type productionHydrationRuntime struct {
	activation *productionActivationRuntime
	manifest   haulkit.Manifest
	kit        FileIdentity
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
	runtimeState.kit = request.Expected.Kit
	return kit, err
}

func (*productionHydrationRuntime) ObserveCompleted(request Request) (HydrationReceipt, bool, error) {
	body, err := readHydrationReceipt(request.WorkspaceRoot)
	if err != nil {
		return HydrationReceipt{}, false, nil
	}
	if err := jsonstrict.RejectDuplicateKeys(body); err != nil {
		return HydrationReceipt{}, false, nil
	}
	var receipt HydrationReceipt
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return HydrationReceipt{}, false, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return HydrationReceipt{}, false, nil
	}
	trustedRootSHA256, err := readTrustedManifestRoot(request)
	if err != nil || !hydrationReceiptMatches(request, receipt, trustedRootSHA256) {
		return HydrationReceipt{}, false, nil
	}
	return receipt, true, nil
}

func newHydrationReceipt(request Request, rootSHA256 string) HydrationReceipt {
	return HydrationReceipt{
		Status: "completed", SessionID: request.SessionID, WorkspaceRoot: request.WorkspaceRoot,
		RuntimeRoot: request.RuntimeRoot, ManifestPath: request.ManifestPath, Expected: request.Expected,
		RootSHA256: rootSHA256,
	}
}

func hydrationReceiptMatches(request Request, receipt HydrationReceipt, trustedRootSHA256 string) bool {
	return receipt.Status == "completed" && receipt.SessionID == request.SessionID &&
		receipt.WorkspaceRoot == request.WorkspaceRoot && receipt.RuntimeRoot == request.RuntimeRoot &&
		receipt.ManifestPath == request.ManifestPath && receipt.Expected == request.Expected &&
		validDigest(trustedRootSHA256) && receipt.RootSHA256 == trustedRootSHA256
}

func readTrustedManifestRoot(request Request) (string, error) {
	const maxHydrationManifestBytes = 4 << 20
	expected := request.Expected.Manifest
	if expected.Size <= 0 || expected.Size > maxHydrationManifestBytes ||
		expected.Name != filepath.Base(request.ManifestPath) {
		return "", ErrIdentityMismatch
	}
	parentFD, _, err := openOperationDirectory(filepath.Dir(request.ManifestPath))
	if err != nil {
		return "", err
	}
	defer unix.Close(parentFD)
	fd, err := unix.Openat(parentFD, expected.Name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), expected.Name)
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() != expected.Size {
		return "", ErrIdentityMismatch
	}
	body, err := io.ReadAll(io.LimitReader(file, expected.Size+1))
	if err != nil || int64(len(body)) != expected.Size {
		return "", errors.Join(err, ErrIdentityMismatch)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() {
		return "", ErrIdentityMismatch
	}
	digest := sha256.Sum256(body)
	if fmt.Sprintf("%x", digest) != expected.SHA256 {
		return "", ErrIdentityMismatch
	}
	manifest, err := haulkit.DecodeCanonical(body)
	if err != nil {
		return "", err
	}
	return manifest.Root.SHA256, nil
}

func readHydrationReceipt(workspace string) ([]byte, error) {
	workspaceFD, _, err := openOperationDirectory(workspace)
	if err != nil {
		return nil, err
	}
	defer unix.Close(workspaceFD)
	campFD, err := unix.Openat(workspaceFD, ".camp", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(campFD)
	runtimeFD, err := unix.Openat(campFD, "runtime", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(runtimeFD)
	fd, err := unix.Openat(runtimeFD, "hydrate.receipt.json", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "hydrate.receipt.json")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxDiagnosticBytes {
		return nil, ErrUnsafeHydration
	}
	body, err := io.ReadAll(io.LimitReader(file, maxDiagnosticBytes+1))
	if err != nil || len(body) > maxDiagnosticBytes {
		return nil, errors.Join(err, ErrUnsafeHydration)
	}
	return body, nil
}

func (runtimeState *productionHydrationRuntime) AdmitWorkspace(request Request) error {
	body, err := readStableIdentityFile(request.ManifestPath, request.Expected.Manifest)
	if err != nil {
		return fmt.Errorf("%w: read activation manifest: %v", ErrUnsafeHydration, err)
	}
	manifest, err := haulkit.DecodeCanonical(body)
	if err != nil || manifest.Archive.SHA256 != request.Expected.Kit.SHA256 || manifest.Archive.Size != request.Expected.Kit.Size {
		return fmt.Errorf("%w: activation manifest archive identity", ErrUnsafeHydration)
	}
	runtimeState.manifest = manifest
	workspaceFD, _, err := openOperationDirectory(request.WorkspaceRoot)
	if err != nil {
		return fmt.Errorf("%w: open workspace: %v", ErrUnsafeHydration, err)
	}
	defer unix.Close(workspaceFD)
	return validateInitialWorkspace(workspaceFD, manifest.Chunks)
}

func (runtimeState *productionHydrationRuntime) ExtractRoot(ctx context.Context, request Request, kit verifiedRuntimeKit) (string, error) {
	ready := filepath.Dir(kit.Store)
	hauler := hauleradapter.NewClient(filepath.Join(ready, "bin", "hauler"), subprocess.NewRunner())
	rootOutput, rootArchive, err := rootExtractionPaths(request.RuntimeRoot, runtimeState.manifest.Root.Reference)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(rootOutput); err == nil {
		if err := validateExtractedRoot(rootOutput, rootArchive, runtimeState.manifest.Root); err != nil {
			return "", fmt.Errorf("%w: existing root artifact", err)
		}
	} else if isNotExist(err) {
		result, extractErr := hauler.Extract(ctx, kit.Store, runtimeState.manifest.Root.Reference, rootOutput)
		if extractErr != nil || result.ExitCode != 0 {
			return "", fmt.Errorf("extract root artifact: %w", extractErr)
		}
		if err := validateExtractedRoot(rootOutput, rootArchive, runtimeState.manifest.Root); err != nil {
			return "", fmt.Errorf("%w: extracted root artifact", err)
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

func rootExtractionPaths(runtimeRoot, reference string) (string, string, error) {
	canonical, err := haulkit.NormalizeRootReference(reference)
	if err != nil {
		return "", "", err
	}
	name := strings.TrimSuffix(strings.TrimPrefix(canonical, "hauler/"), ":latest")
	output := filepath.Join(runtimeRoot, "root.tar.zst")
	return output, filepath.Join(output, name), nil
}

func validateExtractedRoot(output, archive string, expected haulkit.RootIdentity) error {
	outputFD, _, err := openOperationDirectory(output)
	if err != nil {
		return fmt.Errorf("%w: extracted root output: %v", ErrIdentityMismatch, err)
	}
	defer unix.Close(outputFD)
	names, err := readDirectoryNames(outputFD)
	if err != nil || len(names) != 1 || names[0] != filepath.Base(archive) {
		return fmt.Errorf("%w: extracted root output shape", ErrIdentityMismatch)
	}
	observed, err := observeFile(filepath.Base(archive), archive)
	if err != nil || observed.SHA256 != expected.SHA256 || observed.Size != expected.Size {
		return fmt.Errorf("%w: extracted root artifact", ErrIdentityMismatch)
	}
	return nil
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

func (runtimeState *productionHydrationRuntime) Promote(stage, workspace string) error {
	return promoteHydratedRoot(stage, workspace, runtimeState.manifest.Chunks, nil)
}

func (*productionHydrationRuntime) Publish(request Request, receipt HydrationReceipt) error {
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return publishReceipt(filepath.Join(request.WorkspaceRoot, ".camp", "runtime", "hydrate.receipt.json"), append(body, '\n'))
}

func promoteHydratedRoot(stage, workspace string, chunks []haulkit.ChunkIdentity, boundary func(string) error) error {
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
	if err := validateInitialWorkspace(workspaceFD, chunks); err != nil {
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

func validateInitialWorkspace(workspaceFD int, chunks []haulkit.ChunkIdentity) error {
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
		case "chunks":
			if len(chunks) == 0 {
				return fmt.Errorf("%w: unexpected initial workspace entry %q", ErrUnsafeHydration, name)
			}
			if err := requireInitialChunksAt(workspaceFD, chunks); err != nil {
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
	if len(chunks) > 0 && !containsName(names, "chunks") {
		return fmt.Errorf("%w: bootstrap chunks are missing", ErrUnsafeHydration)
	}
	return nil
}

func requireInitialChunksAt(parentFD int, chunks []haulkit.ChunkIdentity) error {
	chunksFD, err := unix.Openat(parentFD, "chunks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: bootstrap chunks are unsafe: %v", ErrUnsafeHydration, err)
	}
	defer unix.Close(chunksFD)
	names, err := readDirectoryNames(chunksFD)
	if err != nil || len(names) != len(chunks) {
		return fmt.Errorf("%w: bootstrap chunk allowlist", ErrUnsafeHydration)
	}
	for index, chunk := range chunks {
		if index >= len(names) || names[index] != chunk.Name {
			return fmt.Errorf("%w: bootstrap chunk allowlist", ErrUnsafeHydration)
		}
		if err := requireInitialFileAt(chunksFD, FileIdentity{Name: chunk.Name, SHA256: chunk.SHA256, Size: chunk.Size}); err != nil {
			return err
		}
	}
	return nil
}

func requireInitialFileAt(parentFD int, expected FileIdentity) error {
	fileDescriptor, err := unix.Openat(parentFD, expected.Name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("%w: bootstrap file is unsafe: %v", ErrUnsafeHydration, err)
	}
	file := os.NewFile(uintptr(fileDescriptor), expected.Name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != expected.Size {
		return fmt.Errorf("%w: bootstrap file identity", ErrUnsafeHydration)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil || fmt.Sprintf("%x", digest.Sum(nil)) != expected.SHA256 {
		return fmt.Errorf("%w: bootstrap file identity", ErrUnsafeHydration)
	}
	return nil
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
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

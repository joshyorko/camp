package remoteworker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	hauleradapter "github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/ports"
	"golang.org/x/sys/unix"
)

type productionOperations struct{}

func (productionOperations) StartServices(ctx context.Context, request Request) (any, error) {
	return launchServiceSupervisor(ctx, request)
}

type productionActivationRuntime struct {
	kit verifiedRuntimeKit
}

type runtimeStoreValidator struct {
	version string
}

func (validator runtimeStoreValidator) client(store string) *hauleradapter.Client {
	return hauleradapter.NewClientWithVersion(
		filepath.Join(filepath.Dir(store), "bin", "hauler"),
		validator.version,
		subprocess.NewRunner(),
	)
}

func (validator runtimeStoreValidator) ValidateStore(ctx context.Context, store string) (haulkit.StoreIdentity, error) {
	return validator.client(store).ValidateStore(ctx, store)
}

func (validator runtimeStoreValidator) ObserveRoot(ctx context.Context, store, reference string) (haulkit.RootIdentity, error) {
	return validator.client(store).ObserveRoot(ctx, store, reference)
}

func newProductionActivationRuntime() *productionActivationRuntime {
	return &productionActivationRuntime{}
}

func (runtimeState *productionActivationRuntime) Verify(ctx context.Context, request Request) (verifiedRuntimeKit, error) {
	executable, err := os.Executable()
	if err != nil {
		return verifiedRuntimeKit{}, err
	}
	private := filepath.Dir(executable)
	sourceManifest := filepath.Join(private, request.Expected.Manifest.Name)
	sourceKit := filepath.Join(filepath.Dir(private), request.Expected.Kit.Name)
	for path, expected := range map[string]FileIdentity{
		executable: request.Expected.Helper, sourceManifest: request.Expected.Manifest, sourceKit: request.Expected.Kit,
	} {
		observed, err := observeFile(expected.Name, path)
		if err != nil || observed != expected {
			return verifiedRuntimeKit{}, fmt.Errorf("%w: activation input %q", ErrIdentityMismatch, expected.Name)
		}
	}
	body, err := os.ReadFile(sourceManifest)
	if err != nil {
		return verifiedRuntimeKit{}, err
	}
	manifest, err := haulkit.DecodeCanonical(body)
	if err != nil {
		return verifiedRuntimeKit{}, err
	}
	if manifest.SessionID != request.SessionID || manifest.Architecture != request.Expected.Architecture ||
		request.Expected.Architecture != "linux/"+runtime.GOARCH {
		return verifiedRuntimeKit{}, fmt.Errorf("%w: activation manifest scope", ErrIdentityMismatch)
	}
	if err := secureMkdirAllOperation(request.RuntimeRoot); err != nil {
		return verifiedRuntimeKit{}, err
	}
	ready := filepath.Join(request.RuntimeRoot, "kit")
	validator := runtimeStoreValidator{version: manifest.Tools.Hauler.Version}
	if info, statErr := os.Lstat(ready); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return verifiedRuntimeKit{}, fmt.Errorf("%w: existing runtime kit", ErrIdentityMismatch)
		}
		for name, identity := range map[string]haulkit.FileIdentity{
			"camp": manifest.Tools.Camp, "hauler": manifest.Tools.Hauler, "pasta": manifest.Tools.Pasta,
		} {
			observed, err := observeFile(name, filepath.Join(ready, "bin", name))
			if err != nil || observed.SHA256 != identity.SHA256 || observed.Size != identity.Size {
				return verifiedRuntimeKit{}, fmt.Errorf("%w: existing runtime tool %s", ErrIdentityMismatch, name)
			}
		}
		store := filepath.Join(ready, "store")
		observedStore, err := validator.ValidateStore(ctx, store)
		if err != nil || !reflect.DeepEqual(observedStore, manifest.Store) {
			return verifiedRuntimeKit{}, fmt.Errorf("%w: existing runtime store", ErrIdentityMismatch)
		}
		root, err := validator.ObserveRoot(ctx, store, manifest.Root.Reference)
		if err != nil || root != manifest.Root {
			return verifiedRuntimeKit{}, fmt.Errorf("%w: existing runtime root", ErrIdentityMismatch)
		}
		if err := publishStableBytes(request.ManifestPath, body, request.Expected.Manifest); err != nil {
			return verifiedRuntimeKit{}, err
		}
		runtimeState.kit = verifiedRuntimeKit{Store: store, RootSHA256: manifest.Root.SHA256}
		return runtimeState.kit, nil
	} else if !isNotExist(statErr) {
		return verifiedRuntimeKit{}, statErr
	}
	verified, err := haulkit.NewVerifier(validator).Verify(ctx, haulkit.VerifyRequest{
		ManifestPath: sourceManifest, ExpectedManifestSHA256: request.Expected.Manifest.SHA256, ArchivePath: sourceKit, Architecture: request.Expected.Architecture,
		Tools: manifest.Tools, Destination: ready,
	})
	if err != nil {
		return verifiedRuntimeKit{}, err
	}
	if verified.Manifest.Root != manifest.Root || verified.ReadyDirectory != ready {
		return verifiedRuntimeKit{}, fmt.Errorf("%w: verified runtime kit", ErrIdentityMismatch)
	}
	if err := publishStableBytes(request.ManifestPath, body, request.Expected.Manifest); err != nil {
		return verifiedRuntimeKit{}, err
	}
	runtimeState.kit = verifiedRuntimeKit{Store: filepath.Join(ready, "store"), RootSHA256: manifest.Root.SHA256}
	return runtimeState.kit, nil
}

func (runtimeState *productionActivationRuntime) StartRegistry(ctx context.Context, request Request, kit verifiedRuntimeKit) (temporaryRegistry, error) {
	if kit.Store != runtimeState.kit.Store {
		return temporaryRegistry{}, ErrIdentityMismatch
	}
	ready := filepath.Dir(kit.Store)
	haulerPath := filepath.Join(ready, "bin", "hauler")
	pastaPath := filepath.Join(ready, "bin", "pasta")
	port := 46000 + int(sha256.Sum256([]byte(request.SessionID))[0])*20/256
	logPath := filepath.Join(request.RuntimeRoot, "activation-registry.log")
	pidPath := filepath.Join(request.RuntimeRoot, "activation-registry.pid")
	overlay := filepath.Join(request.RuntimeRoot, "activation-registry")
	if err := os.Mkdir(overlay, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return temporaryRegistry{}, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return temporaryRegistry{}, err
	}
	mapping := "127.0.0.1/" + strconv.Itoa(port) + ":5000"
	argv := []string{
		"--foreground", "--quiet", "--log-file", logPath, "--pid", pidPath,
		"--ipv4-only", "--host-lo-to-ns-lo", "--tcp-ports", mapping,
		"--udp-ports", "none", "--tcp-ns", "none", "--udp-ns", "none", "--",
		haulerPath, "store", "--store", kit.Store, "serve", "registry",
		"--directory", overlay, "--port", "5000", "--readonly=true",
	}
	command := exec.Command(pastaPath, argv...)
	command.Stdout, command.Stderr = logFile, logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return temporaryRegistry{}, err
	}
	stop := func() error {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		waitErr := command.Wait()
		closeErr := logFile.Close()
		if exit, ok := waitErr.(*exec.ExitError); ok && exit.ProcessState.ExitCode() == -1 {
			waitErr = nil
		}
		return errors.Join(waitErr, closeErr)
	}
	endpoint := "http://127.0.0.1:" + strconv.Itoa(port) + "/v2/"
	deadline := time.Now().Add(15 * time.Second)
	for {
		probe, probeErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if probeErr == nil {
			response, doErr := http.DefaultClient.Do(probe)
			if doErr == nil {
				_ = response.Body.Close()
				if response.StatusCode >= 200 && response.StatusCode < 500 {
					break
				}
			}
		}
		if time.Now().After(deadline) {
			return temporaryRegistry{}, errors.Join(errors.New("temporary activation registry did not become ready"), stop())
		}
		select {
		case <-ctx.Done():
			return temporaryRegistry{}, errors.Join(ctx.Err(), stop())
		case <-time.After(50 * time.Millisecond):
		}
	}
	repository, digest, err := splitImmutableImage(request.Expected.SourceImage)
	if err != nil {
		return temporaryRegistry{}, errors.Join(err, stop())
	}
	return temporaryRegistry{
		Reference: "127.0.0.1:" + strconv.Itoa(port) + "/" + repository + "@" + digest,
		Stop:      stop,
	}, nil
}

func (*productionActivationRuntime) PullAndInspect(ctx context.Context, reference string) (string, error) {
	docker, err := exec.LookPath("docker")
	if err != nil {
		return "", fmt.Errorf("%w: Docker-compatible provider engine is unavailable", ErrUnsupportedCapability)
	}
	runner := subprocess.NewRunner()
	pulled, err := runner.Run(ctx, ports.Command{Executable: docker, Argv: []string{"pull", reference}})
	if err != nil || pulled.ExitCode != 0 {
		return "", fmt.Errorf("pull activation image: %w", err)
	}
	return inspectDockerImage(ctx, docker, reference)
}

func inspectDockerImage(ctx context.Context, docker, reference string) (string, error) {
	runner := subprocess.NewRunner()
	inspected, err := runner.Run(ctx, ports.Command{Executable: docker, Argv: []string{"image", "inspect", "--format", "{{.Id}}", reference}})
	if err != nil || inspected.ExitCode != 0 {
		return "", fmt.Errorf("inspect activation image: %w", err)
	}
	value := strings.TrimSpace(string(inspected.Stdout))
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return "", fmt.Errorf("%w: Docker returned invalid local image ID", ErrIdentityMismatch)
	}
	return value, nil
}

func (*productionActivationRuntime) Observe(ctx context.Context, request Request, _ verifiedRuntimeKit) (ActivationReceipt, bool, error) {
	path := filepath.Join(request.RuntimeRoot, "activate-image.receipt.json")
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if isNotExist(err) {
		return ActivationReceipt{}, false, nil
	}
	if err != nil {
		return ActivationReceipt{}, false, err
	}
	body, readErr := io.ReadAll(io.LimitReader(file, maxDiagnosticBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) > maxDiagnosticBytes {
		return ActivationReceipt{}, false, errors.Join(readErr, closeErr, ErrUnsafeHydration)
	}
	var receipt ActivationReceipt
	if err := json.Unmarshal(body, &receipt); err != nil || receipt.Status != "completed" ||
		receipt.SourceImage != request.Expected.SourceImage || receipt.LocalImage != request.Expected.Image {
		return ActivationReceipt{}, false, fmt.Errorf("%w: activation receipt differs", ErrUnsafeHydration)
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return ActivationReceipt{}, false, fmt.Errorf("%w: Docker-compatible provider engine is unavailable", ErrUnsupportedCapability)
	}
	observed, err := inspectDockerImage(ctx, docker, request.Expected.Image)
	if err != nil || observed != request.Expected.Image {
		return ActivationReceipt{}, false, fmt.Errorf("%w: activated provider image changed", ErrIdentityMismatch)
	}
	return receipt, true, nil
}

func (*productionActivationRuntime) Publish(request Request, receipt ActivationReceipt) error {
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return publishReceipt(filepath.Join(request.RuntimeRoot, "activate-image.receipt.json"), append(body, '\n'))
}

type verifiedRuntimeKit struct {
	Store      string
	RootSHA256 string
}

type temporaryRegistry struct {
	Reference string
	Stop      func() error
}

type ActivationReceipt struct {
	Status      string `json:"status"`
	SourceImage string `json:"sourceImage"`
	LocalImage  string `json:"localImage"`
}

type activationRuntime interface {
	Verify(context.Context, Request) (verifiedRuntimeKit, error)
	StartRegistry(context.Context, Request, verifiedRuntimeKit) (temporaryRegistry, error)
	PullAndInspect(context.Context, string) (string, error)
	Publish(Request, ActivationReceipt) error
}

func activateImage(ctx context.Context, request Request, runtime activationRuntime) (ActivationReceipt, error) {
	kit, err := runtime.Verify(ctx, request)
	if err != nil {
		return ActivationReceipt{}, err
	}
	if observer, ok := runtime.(interface {
		Observe(context.Context, Request, verifiedRuntimeKit) (ActivationReceipt, bool, error)
	}); ok {
		if receipt, complete, err := observer.Observe(ctx, request, kit); err != nil || complete {
			return receipt, err
		}
	}
	registry, err := runtime.StartRegistry(ctx, request, kit)
	if err != nil {
		return ActivationReceipt{}, err
	}
	if registry.Stop == nil || registry.Reference == "" {
		return ActivationReceipt{}, errors.New("temporary activation registry is incomplete")
	}
	localID, operationErr := runtime.PullAndInspect(ctx, registry.Reference)
	stopErr := registry.Stop()
	if operationErr != nil || stopErr != nil {
		return ActivationReceipt{}, errors.Join(operationErr, stopErr)
	}
	if localID != request.Expected.Image {
		return ActivationReceipt{}, fmt.Errorf("%w: provider engine image ID", ErrIdentityMismatch)
	}
	receipt := ActivationReceipt{Status: "completed", SourceImage: request.Expected.SourceImage, LocalImage: localID}
	if err := runtime.Publish(request, receipt); err != nil {
		return ActivationReceipt{}, err
	}
	return receipt, nil
}

func (productionOperations) ActivateImage(ctx context.Context, request Request) (any, error) {
	return activateImage(ctx, request, newProductionActivationRuntime())
}

func secureMkdirAllOperation(path string) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean != path {
		return fmt.Errorf("%w: runtime root is not absolute and clean", ErrInvalidRequest)
	}
	current := string(filepath.Separator)
	for _, segment := range strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator)) {
		if segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: unsafe runtime path %q", ErrInvalidRequest, current)
		}
	}
	return nil
}

func publishStableBytes(path string, body []byte, expected FileIdentity) error {
	if observed, err := observeFile(expected.Name, path); err == nil {
		if observed == expected {
			return nil
		}
		return fmt.Errorf("%w: stable file %q differs", ErrIdentityMismatch, path)
	} else if !isNotExist(err) {
		return err
	}
	if err := secureMkdirAllOperation(filepath.Dir(path)); err != nil {
		return err
	}
	partial := path + ".part"
	file, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	writeErr := writeAllAt(file, body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		_ = os.Remove(partial)
		return errors.Join(writeErr, syncErr, closeErr)
	}
	observed, err := observeFile(expected.Name, partial)
	if err != nil || observed != expected {
		_ = os.Remove(partial)
		return fmt.Errorf("%w: staged stable file", ErrIdentityMismatch)
	}
	if err := unix.Renameat2(unix.AT_FDCWD, partial, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE); err != nil {
		_ = os.Remove(partial)
		return err
	}
	return syncOperationDirectory(filepath.Dir(path))
}

func publishReceipt(path string, body []byte) error {
	if existing, err := os.ReadFile(path); err == nil {
		if string(existing) == string(body) {
			return nil
		}
		return fmt.Errorf("%w: receipt differs", ErrUnsafeHydration)
	} else if !isNotExist(err) {
		return err
	}
	digest := sha256.Sum256(body)
	expected := FileIdentity{Name: filepath.Base(path), SHA256: fmt.Sprintf("%x", digest[:]), Size: int64(len(body))}
	return publishStableBytes(path, body, expected)
}

func syncOperationDirectory(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return unix.Fsync(fd)
}

func splitImmutableImage(reference string) (string, string, error) {
	const marker = "@sha256:"
	index := strings.LastIndex(reference, marker)
	if index <= 0 {
		return "", "", ErrInvalidRequest
	}
	name, digest := reference[:index], reference[index+1:]
	firstSlash := strings.IndexByte(name, '/')
	if firstSlash >= 0 {
		first := name[:firstSlash]
		if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
			name = name[firstSlash+1:]
		}
	}
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "\\\x00") {
		return "", "", ErrInvalidRequest
	}
	return name, digest, nil
}

func (productionOperations) Hydrate(ctx context.Context, request Request) (any, error) {
	return hydrateWorkspace(ctx, request, newProductionHydrationRuntime())
}

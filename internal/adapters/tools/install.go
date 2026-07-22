//go:build linux

package tools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	defaultMaxDownloadBytes   = int64(256 << 20)
	defaultMaxExecutableBytes = int64(512 << 20)
	maxInstallIdentityBytes   = 64 << 10
	ExternalHostCapability    = "external-host-capability"
)

type Resolution struct {
	Path         string `json:"path"`
	Managed      bool   `json:"managed"`
	Repository   string `json:"repository"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	GOOS         string `json:"goos"`
	Architecture string `json:"architecture"`
	AssetSHA256  string `json:"assetSha256"`
	BinarySHA256 string `json:"binarySha256"`
}

type installIdentity struct {
	Repository   string `json:"repository"`
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	GOOS         string `json:"goos"`
	Architecture string `json:"architecture"`
	AssetSHA256  string `json:"assetSha256"`
	BinarySHA256 string `json:"binarySha256"`
}

type Installer struct {
	lock               Lock
	root               string
	client             *http.Client
	allowedHosts       map[string]struct{}
	lookPath           func(string) (string, error)
	maxDownloadBytes   int64
	maxExecutableBytes int64
	hooks              map[InstallStage]func() error
}

type InstallerOption func(*Installer) error

type InstallStage string

const (
	StageDownload InstallStage = "download"
	StageVerify   InstallStage = "verify"
	StageChmod    InstallStage = "chmod"
	StageFsync    InstallStage = "fsync"
	StageRename   InstallStage = "rename"
)

func WithHTTPClient(client *http.Client) InstallerOption {
	return func(installer *Installer) error {
		if client == nil {
			return errors.New("tool installer HTTP client is required")
		}
		installer.client = client
		return nil
	}
}

func WithAllowedHosts(hosts ...string) InstallerOption {
	return func(installer *Installer) error {
		if len(hosts) == 0 {
			return errors.New("at least one approved download host is required")
		}
		installer.allowedHosts = make(map[string]struct{}, len(hosts))
		for _, host := range hosts {
			if host == "" || strings.ContainsAny(host, "/?#@") {
				return errors.New("invalid approved download host")
			}
			installer.allowedHosts[strings.ToLower(host)] = struct{}{}
		}
		return nil
	}
}

func WithLookPath(lookPath func(string) (string, error)) InstallerOption {
	return func(installer *Installer) error {
		if lookPath == nil {
			return errors.New("tool path resolver is required")
		}
		installer.lookPath = lookPath
		return nil
	}
}

func WithDownloadLimits(downloadBytes, executableBytes int64) InstallerOption {
	return func(installer *Installer) error {
		if downloadBytes <= 0 || executableBytes <= 0 {
			return errors.New("tool download limits must be positive")
		}
		installer.maxDownloadBytes = downloadBytes
		installer.maxExecutableBytes = executableBytes
		return nil
	}
}

func WithInstallHook(stage InstallStage, hook func() error) InstallerOption {
	return func(installer *Installer) error {
		if hook == nil {
			return errors.New("tool install hook is required")
		}
		switch stage {
		case StageDownload, StageVerify, StageChmod, StageFsync, StageRename:
		default:
			return errors.New("unknown tool install hook stage")
		}
		installer.hooks[stage] = hook
		return nil
	}
}

func NewInstaller(lock Lock, root string, options ...InstallerOption) (*Installer, error) {
	if err := lock.validate(); err != nil {
		return nil, err
	}
	if root == "" {
		return nil, errors.New("tool install root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve tool install root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create tool install root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("canonicalize tool install root: %w", err)
	}
	installer := &Installer{
		lock:   lock,
		root:   canonical,
		client: &http.Client{Timeout: 5 * time.Minute},
		allowedHosts: map[string]struct{}{
			"github.com":                            {},
			"objects.githubusercontent.com":         {},
			"github-releases.githubusercontent.com": {},
			"release-assets.githubusercontent.com":  {},
		},
		lookPath:           exec.LookPath,
		maxDownloadBytes:   defaultMaxDownloadBytes,
		maxExecutableBytes: defaultMaxExecutableBytes,
		hooks:              make(map[InstallStage]func() error),
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("nil tool installer option")
		}
		if err := option(installer); err != nil {
			return nil, err
		}
	}
	return installer, nil
}

func (i *Installer) Ensure(ctx context.Context, name, goos, arch string) (Resolution, error) {
	tool, asset, err := i.lock.Resolve(name, goos, arch)
	if err != nil {
		return Resolution{}, err
	}
	if goos != "linux" || (arch != "amd64" && arch != "arm64") {
		return Resolution{}, fmt.Errorf("managed tool %q does not support %s/%s", name, goos, arch)
	}
	identity := installIdentity{
		Repository: tool.Repository, Version: tool.Version, Commit: tool.Commit,
		GOOS: goos, Architecture: arch, AssetSHA256: asset.SHA256,
	}
	var candidatePath, candidateDigest string
	if candidate, pathErr := i.lookPath(name); pathErr == nil {
		if digest, verifyErr := verifyExecutable(candidate, i.maxExecutableBytes); verifyErr == nil {
			candidatePath, candidateDigest = candidate, digest
			if candidateDigest == asset.SHA256 && !isArchiveAsset(name, asset) {
				return resolution(candidate, false, identity, candidateDigest), nil
			}
		}
	}

	finalDirectory, finalPath, markerPath := i.managedPaths(name, identity)
	guard, err := i.acquire(ctx, name, identity)
	if err != nil {
		return Resolution{}, err
	}
	defer unlockInstallGuard(guard)

	if binaryDigest, verifyErr := verifyManaged(name, asset, finalPath, markerPath, identity, i.maxDownloadBytes, i.maxExecutableBytes); verifyErr == nil {
		if candidatePath != "" && candidateDigest == binaryDigest {
			return resolution(candidatePath, false, identity, candidateDigest), nil
		}
		return resolution(finalPath, true, identity, binaryDigest), nil
	}
	if err := os.RemoveAll(finalDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Resolution{}, fmt.Errorf("remove unverified managed tool: %w", err)
	}
	parent := filepath.Dir(finalDirectory)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Resolution{}, fmt.Errorf("create managed tool identity directory: %w", err)
	}
	if err := cleanAbandonedStages(parent); err != nil {
		return Resolution{}, err
	}
	stage, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return Resolution{}, fmt.Errorf("create private tool staging directory: %w", err)
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		_ = os.RemoveAll(stage)
		return Resolution{}, fmt.Errorf("protect tool staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stage)
		}
	}()

	downloadPath := filepath.Join(stage, "download")
	if err := i.download(ctx, asset, downloadPath); err != nil {
		return Resolution{}, err
	}
	if err := i.runHook(StageDownload); err != nil {
		return Resolution{}, err
	}
	assetDigest, err := hashRegularFile(downloadPath, i.maxDownloadBytes, false)
	if err != nil {
		return Resolution{}, fmt.Errorf("verify downloaded tool asset: %w", err)
	}
	if assetDigest != asset.SHA256 {
		return Resolution{}, errors.New("downloaded tool asset checksum mismatch")
	}
	if err := i.runHook(StageVerify); err != nil {
		return Resolution{}, err
	}
	stagedBinary := filepath.Join(stage, name)
	if isArchiveAsset(name, asset) {
		if err := extractReleaseTarGzip(downloadPath, stagedBinary, name, i.maxExecutableBytes); err != nil {
			return Resolution{}, err
		}
		if err := os.Rename(downloadPath, filepath.Join(stage, "source.tar.gz")); err != nil {
			return Resolution{}, fmt.Errorf("retain verified tool archive: %w", err)
		}
	} else if err := os.Rename(downloadPath, stagedBinary); err != nil {
		return Resolution{}, fmt.Errorf("stage raw tool binary: %w", err)
	}
	if err := os.Chmod(stagedBinary, 0o755); err != nil {
		return Resolution{}, fmt.Errorf("make managed tool executable: %w", err)
	}
	if err := i.runHook(StageChmod); err != nil {
		return Resolution{}, err
	}
	if err := syncFile(stagedBinary); err != nil {
		return Resolution{}, fmt.Errorf("sync managed tool executable: %w", err)
	}
	binaryDigest, err := verifyExecutable(stagedBinary, i.maxExecutableBytes)
	if err != nil {
		return Resolution{}, fmt.Errorf("verify managed tool executable: %w", err)
	}
	identity.BinarySHA256 = binaryDigest
	if isArchiveAsset(name, asset) && candidatePath != "" && candidateDigest == binaryDigest {
		return resolution(candidatePath, false, identity, candidateDigest), nil
	}
	if err := writeIdentity(filepath.Join(stage, "identity.json"), identity); err != nil {
		return Resolution{}, err
	}
	if err := syncDirectory(stage); err != nil {
		return Resolution{}, fmt.Errorf("sync tool staging directory: %w", err)
	}
	if err := i.runHook(StageFsync); err != nil {
		return Resolution{}, err
	}
	if err := os.Rename(stage, finalDirectory); err != nil {
		return Resolution{}, fmt.Errorf("publish managed tool atomically: %w", err)
	}
	committed = true
	if err := i.runHook(StageRename); err != nil {
		return Resolution{}, err
	}
	if err := syncDirectory(parent); err != nil {
		return Resolution{}, fmt.Errorf("sync managed tool parent: %w", err)
	}
	verifiedDigest, err := verifyManaged(name, asset, finalPath, markerPath, identity, i.maxDownloadBytes, i.maxExecutableBytes)
	if err != nil {
		return Resolution{}, fmt.Errorf("verify published managed tool: %w", err)
	}
	return resolution(finalPath, true, identity, verifiedDigest), nil
}

// Inspect resolves an already-present executable against the distribution
// lock without downloading, repairing, or deleting any tool state.
func (i *Installer) Inspect(ctx context.Context, name, goos, arch string) (Resolution, error) {
	if err := ctx.Err(); err != nil {
		return Resolution{}, err
	}
	tool, asset, err := i.lock.Resolve(name, goos, arch)
	if err != nil {
		return Resolution{}, err
	}
	identity := installIdentity{
		Repository: tool.Repository, Version: tool.Version, Commit: tool.Commit,
		GOOS: goos, Architecture: arch, AssetSHA256: asset.SHA256,
	}
	var candidatePath, candidateDigest string
	if candidate, pathErr := i.lookPath(name); pathErr == nil {
		if digest, verifyErr := verifyExecutable(candidate, i.maxExecutableBytes); verifyErr == nil {
			candidatePath, candidateDigest = candidate, digest
			if candidateDigest == asset.SHA256 && !isArchiveAsset(name, asset) {
				return resolution(candidate, false, identity, candidateDigest), nil
			}
		}
	}
	_, finalPath, markerPath := i.managedPaths(name, identity)
	binaryDigest, err := verifyManaged(name, asset, finalPath, markerPath, identity, i.maxDownloadBytes, i.maxExecutableBytes)
	if err != nil {
		return Resolution{}, errors.New("no installed executable matches the locked tool identity")
	}
	if candidatePath != "" && candidateDigest == binaryDigest {
		return resolution(candidatePath, false, identity, candidateDigest), nil
	}
	return resolution(finalPath, true, identity, binaryDigest), nil
}

func (i *Installer) runHook(stage InstallStage) error {
	if hook := i.hooks[stage]; hook != nil {
		if err := hook(); err != nil {
			return fmt.Errorf("tool install interrupted after %s: %w", stage, err)
		}
	}
	return nil
}

func (i *Installer) managedPaths(name string, identity installIdentity) (string, string, string) {
	key := sha256.Sum256([]byte(identity.Repository + "\x00" + identity.Version + "\x00" + identity.Commit))
	directory := filepath.Join(i.root, "tools", name, hex.EncodeToString(key[:]), identity.GOOS+"-"+identity.Architecture, identity.AssetSHA256)
	return directory, filepath.Join(directory, name), filepath.Join(directory, "identity.json")
}

func (i *Installer) acquire(ctx context.Context, name string, identity installIdentity) (*os.File, error) {
	directory := filepath.Join(i.root, "locks")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create tool install lock directory: %w", err)
	}
	key := sha256.Sum256([]byte(name + "\x00" + identity.Repository + "\x00" + identity.Version + "\x00" + identity.GOOS + "\x00" + identity.Architecture))
	guard, err := os.OpenFile(filepath.Join(directory, hex.EncodeToString(key[:])+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open tool install lock: %w", err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := unix.Flock(int(guard.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return guard, nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = guard.Close()
			return nil, fmt.Errorf("lock managed tool install: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = guard.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func unlockInstallGuard(guard *os.File) {
	_ = unix.Flock(int(guard.Fd()), unix.LOCK_UN)
	_ = guard.Close()
}

func (i *Installer) download(ctx context.Context, asset Asset, destination string) error {
	parsed, err := i.validateAssetURL(asset.URL, false)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return errors.New("create tool download request")
	}
	client := *i.client
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("tool download exceeded redirect limit")
		}
		_, redirectErr := i.validateAssetURL(request.URL.String(), true)
		return redirectErr
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("download request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("tool download returned HTTP status %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create tool download staging file: %w", err)
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, i.maxDownloadBytes+1))
	if copyErr == nil && written > i.maxDownloadBytes {
		copyErr = errors.New("tool download exceeds size limit")
	}
	if copyErr == nil {
		copyErr = file.Sync()
	}
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return fmt.Errorf("close tool download staging file: %w", closeErr)
	}
	return nil
}

func (i *Installer) validateAssetURL(raw string, allowRedirectQuery bool) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (!allowRedirectQuery && parsed.RawQuery != "") || parsed.Fragment != "" {
		return nil, errors.New("tool asset URL must be credential-free HTTPS without query or fragment")
	}
	if _, ok := i.allowedHosts[strings.ToLower(parsed.Host)]; !ok {
		return nil, errors.New("tool asset URL host is not approved")
	}
	return parsed, nil
}

func isArchiveAsset(name string, asset Asset) bool {
	return name == "hauler" || strings.HasSuffix(strings.ToLower(asset.URL), ".tar.gz") || strings.HasSuffix(strings.ToLower(asset.URL), ".tgz")
}

func extractReleaseTarGzip(source, destination, executableName string, limit int64) error {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create staged archive executable: %w", err)
	}
	_, inspectErr := inspectReleaseTarGzip(source, executableName, limit, output)
	if inspectErr == nil {
		inspectErr = output.Sync()
	}
	closeErr := output.Close()
	if inspectErr != nil {
		return inspectErr
	}
	if closeErr != nil {
		return fmt.Errorf("close staged archive executable: %w", closeErr)
	}
	return nil
}

func inspectReleaseTarGzip(source, executableName string, limit int64, output io.Writer) (string, error) {
	file, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("open verified tool archive: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", errors.New("tool archive is not valid gzip")
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	hash := sha256.New()
	seen := make(map[string]struct{}, 3)
	var totalSize int64
	for {
		header, nextErr := tarReader.Next()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return "", errors.New("tool archive contains malformed entries")
		}
		cleanName := filepath.ToSlash(filepath.Clean(header.Name))
		if header.Name == "" || filepath.IsAbs(header.Name) || cleanName != header.Name || strings.Contains(header.Name, "\\") || header.Typeflag != tar.TypeReg {
			return "", errors.New("tool archive contains an unsafe or unexpected entry")
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return "", errors.New("tool archive contains duplicate entries")
		}
		seen[header.Name] = struct{}{}
		if header.Size < 0 || header.Size > limit || totalSize > limit-header.Size {
			return "", errors.New("tool archive exceeds decompression limit")
		}
		totalSize += header.Size

		writer := io.Discard
		switch header.Name {
		case executableName:
			if header.Mode&0o111 == 0 {
				return "", errors.New("tool archive executable has unsafe mode")
			}
			writer = hash
			if output != nil {
				writer = io.MultiWriter(output, hash)
			}
		case "LICENSE", "README.md":
			if header.Mode&0o111 != 0 {
				return "", errors.New("tool archive metadata has unsafe mode")
			}
		default:
			return "", errors.New("tool archive contains an unsafe or unexpected entry")
		}
		written, copyErr := io.Copy(writer, tarReader)
		if copyErr != nil || written != header.Size {
			return "", errors.New("tool archive entry size is invalid")
		}
	}
	if _, ok := seen[executableName]; !ok {
		return "", errors.New("tool archive must contain exactly one executable")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func verifyManaged(name string, asset Asset, binaryPath, markerPath string, want installIdentity, downloadLimit, executableLimit int64) (string, error) {
	body, err := readInstallIdentity(markerPath)
	if err != nil {
		return "", err
	}
	var got installIdentity
	if err := json.Unmarshal(body, &got); err != nil {
		return "", err
	}
	want.BinarySHA256 = got.BinarySHA256
	if got != want || got.BinarySHA256 == "" {
		return "", errors.New("managed tool identity mismatch")
	}
	digest, err := verifyExecutable(binaryPath, executableLimit)
	if err != nil {
		return "", err
	}
	if digest != got.BinarySHA256 {
		return "", errors.New("managed tool binary checksum mismatch")
	}
	if isArchiveAsset(name, asset) {
		source := filepath.Join(filepath.Dir(binaryPath), "source.tar.gz")
		sourceDigest, sourceErr := verifyDataFile(source, downloadLimit)
		if sourceErr != nil || sourceDigest != asset.SHA256 {
			return "", errors.New("managed tool source archive does not match locked asset checksum")
		}
		derivedDigest, sourceErr := inspectReleaseTarGzip(source, name, executableLimit, nil)
		if sourceErr != nil || derivedDigest != digest {
			return "", errors.New("managed archive executable does not match locked source asset")
		}
	} else if digest != asset.SHA256 {
		return "", errors.New("managed raw tool does not match locked asset checksum")
	}
	return digest, nil
}

func readInstallIdentity(path string) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open managed tool identity")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > maxInstallIdentityBytes {
		return nil, errors.New("managed tool identity file shape, link count, or size is invalid")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxInstallIdentityBytes+1))
	if err != nil || int64(len(body)) != before.Size {
		return nil, errors.New("read managed tool identity within size limit")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return nil, errors.New("managed tool identity changed while reading")
	}
	return body, nil
}

func verifyDataFile(path string, limit int64) (string, error) {
	return hashNoFollowRegular(path, limit, false)
}

func verifyExecutable(path string, limit int64) (string, error) {
	return hashNoFollowRegular(path, limit, true)
}

func hashNoFollowRegular(path string, limit int64, requireExecutable bool) (string, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return "", errors.New("open tool file")
	}
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return "", err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > limit || (requireExecutable && before.Mode&0o111 == 0) {
		return "", errors.New("tool file shape, link count, permissions, or size is invalid")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil || written > limit {
		return "", errors.New("read tool file within size limit")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return "", err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim {
		return "", errors.New("tool file changed while verifying identity")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func hashRegularFile(path string, limit int64, requireExecutable bool) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > limit || (requireExecutable && info.Mode().Perm()&0o111 == 0) {
		return "", errors.New("tool file shape or size is invalid")
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, limit+1))
	if err != nil {
		return "", err
	}
	if written > limit {
		return "", errors.New("tool file exceeds size limit")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeIdentity(path string, identity installIdentity) error {
	body, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create managed tool identity: %w", err)
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write managed tool identity: %w", err)
	}
	if closeErr != nil {
		return fmt.Errorf("close managed tool identity: %w", closeErr)
	}
	return nil
}

func cleanAbandonedStages(parent string) error {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".stage-") {
			if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
				return fmt.Errorf("remove abandoned tool staging path: %w", err)
			}
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func syncFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func resolution(path string, managed bool, identity installIdentity, binaryDigest string) Resolution {
	return Resolution{Path: path, Managed: managed, Repository: identity.Repository, Version: identity.Version,
		Commit: identity.Commit, GOOS: identity.GOOS, Architecture: identity.Architecture,
		AssetSHA256: identity.AssetSHA256, BinarySHA256: binaryDigest}
}

type PastaCapability struct {
	Kind string
	Path string
}

type PastaProbe struct {
	LookPath func(string) (string, error)
	Run      func(context.Context, string, ...string) ([]byte, error)
}

func (p PastaProbe) Probe(ctx context.Context) (PastaCapability, error) {
	lookPath := p.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	run := p.Run
	if run == nil {
		run = func(ctx context.Context, path string, arguments ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, arguments...).CombinedOutput()
		}
	}
	path, err := lookPath("pasta")
	if err != nil {
		return PastaCapability{}, errors.New("external pasta capability was not found on PATH")
	}
	if _, err := verifyExecutable(path, defaultMaxExecutableBytes); err != nil {
		return PastaCapability{}, fmt.Errorf("validate external pasta capability: %w", err)
	}
	output, err := run(ctx, path, "--help")
	if err != nil {
		return PastaCapability{}, errors.New("external pasta functional probe failed")
	}
	for _, option := range []string{"--config-net", "--map-guest-addr", "--tcp-ports", "--udp-ports"} {
		if !strings.Contains(string(output), option) {
			return PastaCapability{}, fmt.Errorf("external pasta capability lacks required option %s", option)
		}
	}
	return PastaCapability{Kind: ExternalHostCapability, Path: path}, nil
}

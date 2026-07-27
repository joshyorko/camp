package capsule

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshyorko/camp/internal/remoteworker"
	"golang.org/x/sys/unix"
)

const (
	BootstrapRegularFileLimit = 16
	BootstrapMetadataLimit    = 1 << 20
)

type BootstrapVerificationRequest struct {
	Root     string
	Expected remoteworker.ExpectedIdentity
	Scope    BootstrapScope
	Config   remoteworker.FileIdentity
}

type BootstrapScope struct {
	SchemaVersion uint32
	SessionID     string
	WorkspaceRoot string
	RuntimeRoot   string
	ManifestPath  string
	Architecture  string
}

type BootstrapVerification struct {
	RegularFiles  int
	MetadataBytes int64
	Helper        remoteworker.FileIdentity
	Kit           remoteworker.FileIdentity
	Config        remoteworker.FileIdentity
	Initialize    remoteworker.Request
	Hydrate       remoteworker.Request
	Services      remoteworker.Request
}

func VerifyBootstrap(request BootstrapVerificationRequest) (BootstrapVerification, error) {
	if !filepath.IsAbs(request.Root) || filepath.Clean(request.Root) != request.Root {
		return BootstrapVerification{}, fmt.Errorf("%w: bootstrap root is unsafe", ErrInvalidBootstrap)
	}
	if request.Scope.SchemaVersion != remoteworker.ProtocolSchemaVersion || request.Scope.SessionID == "" ||
		!filepath.IsAbs(request.Scope.WorkspaceRoot) || !filepath.IsAbs(request.Scope.RuntimeRoot) ||
		!filepath.IsAbs(request.Scope.ManifestPath) || request.Scope.Architecture != request.Expected.Architecture ||
		request.Config.Name != "devcontainer.json" || request.Config.Size <= 0 {
		return BootstrapVerification{}, fmt.Errorf("%w: persisted bootstrap scope is incomplete", ErrInvalidBootstrap)
	}
	root, err := openDirectoryBootstrap(request.Root)
	if err != nil {
		return BootstrapVerification{}, err
	}
	defer root.Close()
	if err := verifyDirectoryEntries(root, map[string]bool{
		".camp-bootstrap": true, "camp-hauler-kit.tar.zst": false,
	}); err != nil {
		return BootstrapVerification{}, err
	}
	private, err := openRelativeDirectory(root, ".camp-bootstrap")
	if err != nil {
		return BootstrapVerification{}, err
	}
	defer private.Close()
	if err := verifyDirectoryEntries(private, map[string]bool{
		"devcontainer.json": false, "camp-bootstrap": false,
		"initialize-request.json": false, "hydrate-request.json": false, "services-request.json": false,
	}); err != nil {
		return BootstrapVerification{}, err
	}
	result := BootstrapVerification{RegularFiles: 6}
	if result.RegularFiles > BootstrapRegularFileLimit {
		return BootstrapVerification{}, fmt.Errorf("%w: bootstrap file limit", ErrInvalidBootstrap)
	}
	kit, err := observeRelativeFile(root, "camp-hauler-kit.tar.zst", request.Expected.Kit.Name)
	if err != nil || kit != request.Expected.Kit {
		return BootstrapVerification{}, fmt.Errorf("%w: kit identity", ErrInvalidBootstrap)
	}
	helper, helperMode, err := observeRelativeFileWithMode(private, "camp-bootstrap", request.Expected.Helper.Name)
	if err != nil || helper != request.Expected.Helper || helperMode&0o111 == 0 {
		return BootstrapVerification{}, fmt.Errorf("%w: helper identity", ErrInvalidBootstrap)
	}
	result.Kit, result.Helper = kit, helper
	config, err := observeRelativeFile(private, "devcontainer.json", request.Config.Name)
	if err != nil || config != request.Config {
		return BootstrapVerification{}, fmt.Errorf("%w: devcontainer config identity", ErrInvalidBootstrap)
	}
	result.Config = config
	metadata := make(map[string][]byte, 4)
	for _, name := range []string{"devcontainer.json", "initialize-request.json", "hydrate-request.json", "services-request.json"} {
		body, err := readRelativeFile(private, name, BootstrapMetadataLimit-result.MetadataBytes)
		if err != nil {
			return BootstrapVerification{}, err
		}
		result.MetadataBytes += int64(len(body))
		if result.MetadataBytes > BootstrapMetadataLimit {
			return BootstrapVerification{}, fmt.Errorf("%w: bootstrap metadata exceeds %d bytes", ErrInvalidBootstrap, BootstrapMetadataLimit)
		}
		metadata[name] = body
	}
	requestFiles := []struct {
		name      string
		operation remoteworker.Operation
		target    *remoteworker.Request
	}{
		{"initialize-request.json", remoteworker.OperationActivateImage, &result.Initialize},
		{"hydrate-request.json", remoteworker.OperationHydrate, &result.Hydrate},
		{"services-request.json", remoteworker.OperationStartServices, &result.Services},
	}
	for _, item := range requestFiles {
		decoded, err := remoteworker.DecodeRequest(bytes.NewReader(metadata[item.name]))
		if err != nil || decoded.Operation != item.operation || decoded.Expected != request.Expected ||
			!requestMatchesScope(decoded, request.Scope) {
			return BootstrapVerification{}, fmt.Errorf("%w: %s identity", ErrInvalidBootstrap, item.name)
		}
		*item.target = decoded
	}
	if !sameRequestScope(result.Initialize, result.Hydrate) || !sameRequestScope(result.Initialize, result.Services) {
		return BootstrapVerification{}, fmt.Errorf("%w: request scopes differ", ErrInvalidBootstrap)
	}
	document, err := decodeDevcontainer(metadata["devcontainer.json"])
	if err != nil {
		return BootstrapVerification{}, err
	}
	var image string
	if err := decodeRawString(document["image"], &image); err != nil || image != request.Expected.Image {
		return BootstrapVerification{}, fmt.Errorf("%w: devcontainer image identity", ErrInvalidBootstrap)
	}
	for _, hook := range []struct {
		field, request string
	}{
		{"initializeCommand", "initialize-request.json"},
		{"onCreateCommand", "hydrate-request.json"},
		{"postStartCommand", "services-request.json"},
	} {
		raw := string(document[hook.field])
		if strings.Count(raw, ".camp-bootstrap/camp-bootstrap __remote-worker") != 1 ||
			strings.Count(raw, ".camp-bootstrap/"+hook.request) != 1 {
			return BootstrapVerification{}, fmt.Errorf("%w: devcontainer %s helper boundary", ErrInvalidBootstrap, hook.field)
		}
	}
	return result, nil
}

func requestMatchesScope(request remoteworker.Request, scope BootstrapScope) bool {
	return request.SchemaVersion == scope.SchemaVersion && request.SessionID == scope.SessionID &&
		request.WorkspaceRoot == scope.WorkspaceRoot && request.RuntimeRoot == scope.RuntimeRoot &&
		request.ManifestPath == scope.ManifestPath && request.Expected.Architecture == scope.Architecture
}

func verifyDirectoryEntries(directory *os.File, expected map[string]bool) error {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	if len(entries) != len(expected) {
		return fmt.Errorf("%w: bootstrap contains unexpected entries", ErrInvalidBootstrap)
	}
	for _, entry := range entries {
		directoryExpected, ok := expected[entry.Name()]
		if !ok || entry.Type()&os.ModeSymlink != 0 || entry.IsDir() != directoryExpected ||
			(!directoryExpected && !entry.Type().IsRegular()) {
			return fmt.Errorf("%w: bootstrap entry %q has an invalid type", ErrInvalidBootstrap, entry.Name())
		}
	}
	return nil
}

func readRelativeFile(parent *os.File, name string, remaining int64) ([]byte, error) {
	if remaining < 0 {
		return nil, fmt.Errorf("%w: bootstrap metadata budget", ErrInvalidBootstrap)
	}
	file, err := openRelativeRegular(parent, name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, remaining+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > remaining {
		return nil, fmt.Errorf("%w: bootstrap metadata budget", ErrInvalidBootstrap)
	}
	return body, nil
}

func observeRelativeFile(parent *os.File, path, name string) (remoteworker.FileIdentity, error) {
	identity, _, err := observeRelativeFileWithMode(parent, path, name)
	return identity, err
}

func observeRelativeFileWithMode(parent *os.File, path, name string) (remoteworker.FileIdentity, os.FileMode, error) {
	file, err := openRelativeRegular(parent, path)
	if err != nil {
		return remoteworker.FileIdentity{}, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return remoteworker.FileIdentity{}, 0, err
	}
	identity, err := observeOpenFile(file, name)
	return identity, info.Mode(), err
}

func openRelativeRegular(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("bootstrap entry is not regular")
	}
	return file, nil
}

func sameRequestScope(left, right remoteworker.Request) bool {
	return left.SchemaVersion == right.SchemaVersion && left.SessionID == right.SessionID &&
		left.WorkspaceRoot == right.WorkspaceRoot && left.RuntimeRoot == right.RuntimeRoot &&
		left.ManifestPath == right.ManifestPath && left.Expected == right.Expected
}

func decodeRawString(raw []byte, value *string) error {
	if len(raw) == 0 {
		return errors.New("missing string")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

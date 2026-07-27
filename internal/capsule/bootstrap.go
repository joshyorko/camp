package capsule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/joshyorko/camp/internal/remoteworker"
	"golang.org/x/sys/unix"
)

var ErrInvalidBootstrap = errors.New("invalid remote bootstrap source")

type BootstrapRequest struct {
	Root              string
	DevcontainerPath  string
	KitArchivePath    string
	ManifestPath      string
	OuterImage        string
	InitializeRequest remoteworker.Request
	HydrateRequest    remoteworker.Request
	ServicesRequest   remoteworker.Request
}

type Bootstrap struct {
	Root             string
	DevcontainerPath string
}

func RenderBootstrap(request BootstrapRequest) (Bootstrap, error) {
	return renderBootstrap(request, func() (*os.File, error) {
		fd, err := unix.Open("/proc/self/exe", unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(fd), "/proc/self/exe"), nil
	})
}

func renderBootstrap(request BootstrapRequest, openHelper func() (*os.File, error)) (Bootstrap, error) {
	if !filepath.IsAbs(request.Root) || filepath.Clean(request.Root) != request.Root ||
		!filepath.IsAbs(request.DevcontainerPath) || !filepath.IsAbs(request.KitArchivePath) ||
		!filepath.IsAbs(request.ManifestPath) || openHelper == nil {
		return Bootstrap{}, fmt.Errorf("%w: paths must be absolute", ErrInvalidBootstrap)
	}
	if _, err := os.Lstat(request.Root); err == nil {
		return Bootstrap{}, fmt.Errorf("%w: bootstrap root already exists", ErrInvalidBootstrap)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Bootstrap{}, err
	}
	parent := filepath.Dir(request.Root)
	if err := requireRealDirectory(parent); err != nil {
		return Bootstrap{}, err
	}
	original, err := readRegular(request.DevcontainerPath)
	if err != nil {
		return Bootstrap{}, err
	}
	document, err := decodeDevcontainer(original)
	if err != nil {
		return Bootstrap{}, err
	}
	if _, hasImage := document["image"]; !hasImage {
		if _, buildOnly := document["build"]; buildOnly {
			return Bootstrap{}, fmt.Errorf("%w: build-only devcontainer", ErrInvalidBootstrap)
		}
		return Bootstrap{}, fmt.Errorf("%w: devcontainer lacks image", ErrInvalidBootstrap)
	}
	requests := []struct {
		name      string
		operation remoteworker.Operation
		request   remoteworker.Request
	}{
		{"initialize-request.json", remoteworker.OperationActivateImage, request.InitializeRequest},
		{"hydrate-request.json", remoteworker.OperationHydrate, request.HydrateRequest},
		{"services-request.json", remoteworker.OperationStartServices, request.ServicesRequest},
	}
	for _, item := range requests {
		if item.request.Operation != item.operation || item.request.Expected.Image != request.OuterImage {
			return Bootstrap{}, fmt.Errorf("%w: mismatched %s", ErrInvalidBootstrap, item.name)
		}
		body, err := json.Marshal(item.request)
		if err != nil {
			return Bootstrap{}, err
		}
		if _, err := remoteworker.DecodeRequest(bytes.NewReader(body)); err != nil {
			return Bootstrap{}, err
		}
	}
	expected := request.InitializeRequest.Expected
	for _, item := range requests[1:] {
		if item.request.Expected != expected {
			return Bootstrap{}, fmt.Errorf("%w: request identities differ", ErrInvalidBootstrap)
		}
	}
	if observed, err := observeRegular(request.KitArchivePath, expected.Kit.Name); err != nil {
		return Bootstrap{}, err
	} else if observed != expected.Kit {
		return Bootstrap{}, fmt.Errorf("%w: kit identity", ErrInvalidBootstrap)
	}
	if observed, err := observeRegular(request.ManifestPath, expected.Manifest.Name); err != nil {
		return Bootstrap{}, err
	} else if observed != expected.Manifest {
		return Bootstrap{}, fmt.Errorf("%w: manifest identity", ErrInvalidBootstrap)
	}
	helper, err := openHelper()
	if err != nil {
		return Bootstrap{}, err
	}
	defer helper.Close()
	helperInfo, err := helper.Stat()
	if err != nil || !helperInfo.Mode().IsRegular() {
		return Bootstrap{}, fmt.Errorf("%w: helper is not regular", ErrInvalidBootstrap)
	}
	helperIdentity, err := observeOpenFile(helper, expected.Helper.Name)
	if err != nil || helperIdentity != expected.Helper {
		return Bootstrap{}, fmt.Errorf("%w: helper identity", ErrInvalidBootstrap)
	}
	if _, err := helper.Seek(0, io.SeekStart); err != nil {
		return Bootstrap{}, err
	}

	document["image"] = json.RawMessage(mustJSON(request.OuterImage))
	hooks := []struct {
		field   string
		request string
	}{
		{"initializeCommand", "initialize-request.json"},
		{"onCreateCommand", "hydrate-request.json"},
		{"postStartCommand", "services-request.json"},
	}
	for _, hook := range hooks {
		composed, err := composeLifecycle(document[hook.field], hook.request)
		if err != nil {
			return Bootstrap{}, fmt.Errorf("%w: %s: %v", ErrInvalidBootstrap, hook.field, err)
		}
		document[hook.field] = composed
	}
	generated, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return Bootstrap{}, err
	}
	generated = append(generated, '\n')

	stage, err := os.MkdirTemp(parent, ".camp-bootstrap-stage-*")
	if err != nil {
		return Bootstrap{}, err
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	private := filepath.Join(stage, ".camp-bootstrap")
	if err := os.Mkdir(private, 0o700); err != nil {
		return Bootstrap{}, err
	}
	if err := writePrivateFile(filepath.Join(private, "devcontainer.json"), generated, 0o600); err != nil {
		return Bootstrap{}, err
	}
	for _, item := range requests {
		body, err := json.Marshal(item.request)
		if err != nil {
			return Bootstrap{}, err
		}
		body = append(body, '\n')
		if err := writePrivateFile(filepath.Join(private, item.name), body, 0o600); err != nil {
			return Bootstrap{}, err
		}
	}
	if err := copyOpenFile(helper, filepath.Join(private, "camp-bootstrap"), 0o700); err != nil {
		return Bootstrap{}, err
	}
	if err := copyRegular(request.KitArchivePath, filepath.Join(stage, "camp-hauler-kit.tar.zst"), 0o600); err != nil {
		return Bootstrap{}, err
	}
	if err := syncBootstrapDirectory(private); err != nil {
		return Bootstrap{}, err
	}
	if err := syncBootstrapDirectory(stage); err != nil {
		return Bootstrap{}, err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stage, unix.AT_FDCWD, request.Root, unix.RENAME_NOREPLACE); err != nil {
		return Bootstrap{}, err
	}
	if err := syncBootstrapDirectory(parent); err != nil {
		return Bootstrap{}, err
	}
	published = true
	return Bootstrap{
		Root:             request.Root,
		DevcontainerPath: filepath.Join(request.Root, ".camp-bootstrap", "devcontainer.json"),
	}, nil
}

func decodeDevcontainer(body []byte) (map[string]json.RawMessage, error) {
	document, err := decodeRawObject(body)
	if err != nil {
		return nil, fmt.Errorf("%w: decode devcontainer: %v", ErrInvalidBootstrap, err)
	}
	return document, nil
}

func decodeRawObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errors.New("expected JSON object")
	}
	document := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || key == "" {
			return nil, errors.New("invalid object key")
		}
		if _, exists := document[key]; exists {
			return nil, fmt.Errorf("duplicate object key %q", key)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		document[key] = value
	}
	if closeToken, err := decoder.Token(); err != nil || closeToken != json.Delim('}') {
		return nil, errors.New("unterminated JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing JSON")
	}
	return document, nil
}

func composeLifecycle(original json.RawMessage, requestName string) (json.RawMessage, error) {
	helper := mustJSON(".camp-bootstrap/camp-bootstrap __remote-worker < .camp-bootstrap/" + requestName)
	if len(original) == 0 {
		return json.RawMessage(`{"00-camp-bootstrap":` + helper + `}`), nil
	}
	trimmed := bytes.TrimSpace(original)
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, errors.New("null lifecycle command")
	}
	if trimmed[0] == '{' {
		named, err := decodeRawObject(trimmed)
		if err != nil || len(named) == 0 {
			return nil, errors.New("malformed named lifecycle command")
		}
		if _, conflict := named["00-camp-bootstrap"]; conflict {
			return nil, errors.New("reserved lifecycle command name")
		}
		var buffer bytes.Buffer
		buffer.WriteString(`{"00-camp-bootstrap":`)
		buffer.WriteString(helper)
		for _, key := range sortedKeys(named) {
			if err := validateLifecycleLeaf(named[key]); err != nil {
				return nil, err
			}
			buffer.WriteByte(',')
			buffer.WriteString(mustJSON(key))
			buffer.WriteByte(':')
			buffer.Write(bytes.TrimSpace(named[key]))
		}
		buffer.WriteByte('}')
		return buffer.Bytes(), nil
	}
	if err := validateLifecycleLeaf(trimmed); err != nil {
		return nil, err
	}
	return json.RawMessage(`{"00-camp-bootstrap":` + helper + `,"10-user":` + string(trimmed) + `}`), nil
}

func validateLifecycleLeaf(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("malformed lifecycle command")
	}
	switch command := value.(type) {
	case string:
		if command == "" {
			return errors.New("empty lifecycle command")
		}
	case []any:
		if len(command) == 0 {
			return errors.New("empty lifecycle argv")
		}
		for _, argument := range command {
			if _, ok := argument.(string); !ok {
				return errors.New("mixed lifecycle argv")
			}
		}
	default:
		return errors.New("unsupported lifecycle command form")
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mustJSON(value string) string {
	body, _ := json.Marshal(value)
	return string(body)
}

func requireRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %q is not a real directory", ErrInvalidBootstrap, path)
	}
	return nil
}

func readRegular(path string) ([]byte, error) {
	file, err := openRegularBootstrap(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func observeRegular(path, name string) (remoteworker.FileIdentity, error) {
	file, err := openRegularBootstrap(path)
	if err != nil {
		return remoteworker.FileIdentity{}, err
	}
	defer file.Close()
	return observeOpenFile(file, name)
}

func observeOpenFile(file *os.File, name string) (remoteworker.FileIdentity, error) {
	before, err := file.Stat()
	if err != nil {
		return remoteworker.FileIdentity{}, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return remoteworker.FileIdentity{}, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Size() != size || after.Size() != size {
		return remoteworker.FileIdentity{}, errors.New("file identity changed during observation")
	}
	return remoteworker.FileIdentity{Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: size}, nil
}

func openRegularBootstrap(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: %q is not regular", ErrInvalidBootstrap, path)
	}
	return file, nil
}

func writePrivateFile(path string, body []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func copyRegular(source, destination string, mode os.FileMode) error {
	file, err := openRegularBootstrap(source)
	if err != nil {
		return err
	}
	defer file.Close()
	return copyOpenFile(file, destination, mode)
}

func copyOpenFile(source *os.File, destination string, mode os.FileMode) error {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, source)
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

func syncBootstrapDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

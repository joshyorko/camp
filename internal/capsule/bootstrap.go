package capsule

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/joshyorko/camp/internal/haulkit"
	"github.com/joshyorko/camp/internal/jsonstrict"
	"github.com/joshyorko/camp/internal/remoteworker"
	"golang.org/x/sys/unix"
)

var ErrInvalidBootstrap = errors.New("invalid remote bootstrap source")

type BootstrapRequest struct {
	Root              string
	DevcontainerPath  string
	ChunkDirectory    string
	ManifestPath      string
	OuterImage        string
	InitializeRequest remoteworker.Request
	HydrateRequest    remoteworker.Request
	ServicesRequest   remoteworker.Request
}

type Bootstrap struct {
	Root             string
	DevcontainerPath string
	LifecycleUser    string
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
		!filepath.IsAbs(request.DevcontainerPath) || !filepath.IsAbs(request.ChunkDirectory) ||
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
	parentDirectory, err := openDirectoryBootstrap(parent)
	if err != nil {
		return Bootstrap{}, err
	}
	defer parentDirectory.Close()
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
		if item.request.Operation != item.operation || item.request.Expected.SourceImage != request.OuterImage {
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
		if item.request.SchemaVersion != request.InitializeRequest.SchemaVersion ||
			item.request.SessionID != request.InitializeRequest.SessionID ||
			item.request.WorkspaceRoot != request.InitializeRequest.WorkspaceRoot ||
			item.request.RuntimeRoot != request.InitializeRequest.RuntimeRoot ||
			item.request.ManifestPath != request.InitializeRequest.ManifestPath {
			return Bootstrap{}, fmt.Errorf("%w: request scopes differ", ErrInvalidBootstrap)
		}
	}
	manifest, err := openRegularBootstrap(request.ManifestPath)
	if err != nil {
		return Bootstrap{}, err
	}
	defer manifest.Close()
	if observed, err := observeOpenFile(manifest, expected.Manifest.Name); err != nil {
		return Bootstrap{}, err
	} else if observed != expected.Manifest {
		return Bootstrap{}, fmt.Errorf("%w: manifest identity", ErrInvalidBootstrap)
	}
	if _, err := manifest.Seek(0, io.SeekStart); err != nil {
		return Bootstrap{}, err
	}
	manifestBody, err := io.ReadAll(manifest)
	if err != nil {
		return Bootstrap{}, err
	}
	decodedManifest, err := haulkit.DecodeCanonical(manifestBody)
	if err != nil || decodedManifest.Archive.SHA256 != expected.Kit.SHA256 || decodedManifest.Archive.Size != expected.Kit.Size {
		return Bootstrap{}, fmt.Errorf("%w: kit manifest identity", ErrInvalidBootstrap)
	}
	chunkDirectory, err := openDirectoryBootstrap(request.ChunkDirectory)
	if err != nil {
		return Bootstrap{}, err
	}
	defer chunkDirectory.Close()
	if err := verifyChunkDirectory(chunkDirectory, decodedManifest.Chunks); err != nil {
		return Bootstrap{}, err
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
	document["containerUser"] = json.RawMessage(mustJSON("root"))
	removeControllerLocalCompatibilityMounts(document)
	lifecycleUser, err := lifecycleRemoteUser(document)
	if err != nil {
		return Bootstrap{}, err
	}
	hooks := []struct {
		field    string
		requests []string
	}{
		{"onCreateCommand", []string{"initialize-request.json", "hydrate-request.json"}},
		{"postStartCommand", []string{"services-request.json"}},
	}
	for _, hook := range hooks {
		composed, err := composeLifecycle(document[hook.field], hook.requests...)
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

	stageName, stage, stageDirectory, err := createBootstrapStage(parentDirectory)
	if err != nil {
		return Bootstrap{}, err
	}
	defer stageDirectory.Close()
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	if err := unix.Mkdirat(int(stageDirectory.Fd()), ".camp-bootstrap", 0o700); err != nil {
		return Bootstrap{}, err
	}
	privateDirectory, err := openRelativeDirectory(stageDirectory, ".camp-bootstrap")
	if err != nil {
		return Bootstrap{}, err
	}
	defer privateDirectory.Close()
	if err := writePrivateFileAt(privateDirectory, "devcontainer.json", generated, 0o600); err != nil {
		return Bootstrap{}, err
	}
	for _, item := range requests {
		body, err := json.Marshal(item.request)
		if err != nil {
			return Bootstrap{}, err
		}
		body = append(body, '\n')
		if err := writePrivateFileAt(privateDirectory, item.name, body, 0o600); err != nil {
			return Bootstrap{}, err
		}
	}
	if err := copyOpenFileAt(helper, privateDirectory, "camp-bootstrap", 0o700); err != nil {
		return Bootstrap{}, err
	}
	if err := verifyRelativeIdentity(privateDirectory, "camp-bootstrap", expected.Helper); err != nil {
		return Bootstrap{}, err
	}
	if err := copyOpenFileAt(manifest, privateDirectory, expected.Manifest.Name, 0o600); err != nil {
		return Bootstrap{}, err
	}
	if err := verifyRelativeIdentity(privateDirectory, expected.Manifest.Name, expected.Manifest); err != nil {
		return Bootstrap{}, err
	}
	if err := unix.Mkdirat(int(stageDirectory.Fd()), "chunks", 0o700); err != nil {
		return Bootstrap{}, err
	}
	stagedChunks, err := openRelativeDirectory(stageDirectory, "chunks")
	if err != nil {
		return Bootstrap{}, err
	}
	defer stagedChunks.Close()
	for _, chunk := range decodedManifest.Chunks {
		source, err := openRelativeRegular(chunkDirectory, chunk.Name)
		if err != nil {
			return Bootstrap{}, err
		}
		expectedChunk := remoteworker.FileIdentity{Name: chunk.Name, SHA256: chunk.SHA256, Size: chunk.Size}
		if err := copyOpenFileAt(source, stagedChunks, chunk.Name, 0o600); err != nil {
			_ = source.Close()
			return Bootstrap{}, err
		}
		if err := source.Close(); err != nil {
			return Bootstrap{}, err
		}
		if err := verifyRelativeIdentity(stagedChunks, chunk.Name, expectedChunk); err != nil {
			return Bootstrap{}, err
		}
	}
	if err := stagedChunks.Sync(); err != nil {
		return Bootstrap{}, err
	}
	if err := privateDirectory.Sync(); err != nil {
		return Bootstrap{}, err
	}
	if err := stageDirectory.Sync(); err != nil {
		return Bootstrap{}, err
	}
	if err := requireSameDirectory(parentDirectory, parent); err != nil {
		return Bootstrap{}, err
	}
	targetName := filepath.Base(request.Root)
	if err := unix.Renameat2(int(parentDirectory.Fd()), stageName, int(parentDirectory.Fd()), targetName, unix.RENAME_NOREPLACE); err != nil {
		return Bootstrap{}, err
	}
	if err := requireSameDirectory(parentDirectory, parent); err != nil {
		_ = unix.Renameat2(int(parentDirectory.Fd()), targetName, int(parentDirectory.Fd()), stageName, unix.RENAME_NOREPLACE)
		_ = parentDirectory.Sync()
		return Bootstrap{}, err
	}
	if err := bootstrapDirectorySync(parentDirectory); err != nil {
		if rollbackErr := rollbackPublishedBootstrap(parentDirectory, targetName, stageDirectory, &stageName, &stage); rollbackErr != nil {
			return Bootstrap{}, fmt.Errorf("%w: parent sync failed and rollback was incomplete: %v", ErrInvalidBootstrap, rollbackErr)
		}
		return Bootstrap{}, err
	}
	published = true
	return Bootstrap{
		Root:             request.Root,
		DevcontainerPath: filepath.Join(request.Root, ".camp-bootstrap", "devcontainer.json"),
		LifecycleUser:    lifecycleUser,
	}, nil
}

func removeControllerLocalCompatibilityMounts(document map[string]json.RawMessage) {
	raw, exists := document["mounts"]
	if !exists {
		return
	}
	var observed []string
	if err := json.Unmarshal(raw, &observed); err != nil || len(observed) != 6 {
		return
	}
	executables := []string{"iptables", "iptables-save", "iptables-restore", "ip6tables", "ip6tables-save", "ip6tables-restore"}
	for index, executable := range executables {
		expected := "source=${localWorkspaceFolder}/.camp/runtime/iptables-compat,target=/usr/local/sbin/" + executable + ",type=bind,readonly"
		if observed[index] != expected {
			return
		}
	}
	delete(document, "mounts")
}

func lifecycleRemoteUser(document map[string]json.RawMessage) (string, error) {
	user := "root"
	if raw, exists := document["remoteUser"]; exists {
		if err := decodeRawString(raw, &user); err != nil {
			return "", fmt.Errorf("%w: remoteUser", ErrInvalidBootstrap)
		}
	}
	if user == "" || strings.ContainsAny(user, `/\\`) || strings.IndexFunc(user, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: remoteUser", ErrInvalidBootstrap)
	}
	return user, nil
}

func rollbackPublishedBootstrap(parent *os.File, target string, published *os.File, stageName, stagePath *string) error {
	placeholderName, placeholderPath, placeholder, err := createBootstrapStage(parent)
	if err != nil {
		return err
	}
	defer placeholder.Close()
	placeholderOwned := true
	defer func() {
		if placeholderOwned {
			_ = os.RemoveAll(placeholderPath)
		}
	}()
	if err := unix.Renameat2(int(parent.Fd()), target, int(parent.Fd()), placeholderName, unix.RENAME_EXCHANGE); err != nil {
		return err
	}
	placeholderOwned = false
	exchanged, err := openRelativeDirectory(parent, placeholderName)
	if err != nil {
		return err
	}
	exchangedInfo, infoErr := exchanged.Stat()
	publishedInfo, publishedErr := published.Stat()
	_ = exchanged.Close()
	if infoErr != nil || publishedErr != nil {
		return errors.Join(infoErr, publishedErr)
	}
	if !os.SameFile(exchangedInfo, publishedInfo) {
		if err := unix.Renameat2(int(parent.Fd()), target, int(parent.Fd()), placeholderName, unix.RENAME_EXCHANGE); err != nil {
			return fmt.Errorf("restore replacement after inode mismatch: %w", err)
		}
		if err := removeOwnedEmptyDirectory(parent, placeholderName, placeholder); err != nil {
			return err
		}
		if err := parent.Sync(); err != nil {
			return err
		}
		return errors.New("bootstrap target was replaced before rollback")
	}
	if err := removeOwnedEmptyDirectory(parent, target, placeholder); err != nil {
		return err
	}
	*stageName = placeholderName
	*stagePath = placeholderPath
	return parent.Sync()
}

func removeOwnedEmptyDirectory(parent *os.File, name string, expected *os.File) error {
	current, err := openRelativeDirectory(parent, name)
	if err != nil {
		return err
	}
	currentInfo, currentErr := current.Stat()
	expectedInfo, expectedErr := expected.Stat()
	_ = current.Close()
	if currentErr != nil || expectedErr != nil {
		return errors.Join(currentErr, expectedErr)
	}
	if !os.SameFile(currentInfo, expectedInfo) {
		return errors.New("rollback placeholder identity changed")
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}

var bootstrapDirectorySync = func(directory *os.File) error {
	return directory.Sync()
}

func createBootstrapStage(parent *os.File) (string, string, *os.File, error) {
	for range 100 {
		var random [8]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", "", nil, err
		}
		name := ".camp-bootstrap-stage-" + hex.EncodeToString(random[:])
		if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err == nil {
			directory, openErr := openRelativeDirectory(parent, name)
			if openErr != nil {
				_ = unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
				return "", "", nil, openErr
			}
			return name, filepath.Join("/proc/self/fd", fmt.Sprint(parent.Fd()), name), directory, nil
		} else if !errors.Is(err, unix.EEXIST) {
			return "", "", nil, err
		}
	}
	return "", "", nil, errors.New("could not allocate bootstrap stage")
}

func openRelativeDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func verifyRelativeIdentity(parent *os.File, name string, expected remoteworker.FileIdentity) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	observed, err := observeOpenFile(file, expected.Name)
	if err != nil {
		return err
	}
	if observed != expected {
		return fmt.Errorf("%w: staged %s identity", ErrInvalidBootstrap, expected.Name)
	}
	return nil
}

func verifyChunkDirectory(directory *os.File, chunks []haulkit.ChunkIdentity) error {
	expected := make(map[string]bool, len(chunks))
	for _, chunk := range chunks {
		expected[chunk.Name] = false
	}
	if err := verifyDirectoryEntries(directory, expected); err != nil {
		return err
	}
	for _, chunk := range chunks {
		identity, err := observeRelativeFile(directory, chunk.Name, chunk.Name)
		if err != nil || identity.SHA256 != chunk.SHA256 || identity.Size != chunk.Size {
			return fmt.Errorf("%w: chunk identity", ErrInvalidBootstrap)
		}
	}
	return nil
}

func openDirectoryBootstrap(path string) (*os.File, error) {
	return openPathBootstrap(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW)
}

func openPathBootstrap(path string, finalFlags int) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: unsafe path %q", ErrInvalidBootstrap, path)
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	if path == "/" {
		return os.NewFile(uintptr(current), path), nil
	}
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for index, segment := range segments {
		flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_DIRECTORY | unix.O_NOFOLLOW
		if index == len(segments)-1 {
			flags = finalFlags
		}
		next, openErr := unix.Openat(current, segment, flags, 0)
		_ = unix.Close(current)
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	return os.NewFile(uintptr(current), path), nil
}

func requireSameDirectory(directory *os.File, path string) error {
	pinned, err := directory.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() || !os.SameFile(pinned, current) {
		return fmt.Errorf("%w: bootstrap parent changed", ErrInvalidBootstrap)
	}
	return nil
}

func decodeDevcontainer(body []byte) (map[string]json.RawMessage, error) {
	if err := jsonstrict.RejectDuplicateKeys(body); err != nil {
		return nil, fmt.Errorf("%w: decode devcontainer: %v", ErrInvalidBootstrap, err)
	}
	document, err := decodeRawObject(body)
	if err != nil {
		return nil, fmt.Errorf("%w: decode devcontainer: %v", ErrInvalidBootstrap, err)
	}
	return document, nil
}

func decodeRawObject(body []byte) (map[string]json.RawMessage, error) {
	if err := jsonstrict.RejectDuplicateKeys(body); err != nil {
		return nil, err
	}
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

func composeLifecycle(original json.RawMessage, requestNames ...string) (json.RawMessage, error) {
	helpers := make([]string, len(requestNames))
	for index, requestName := range requestNames {
		helpers[index] = ".camp-bootstrap/camp-bootstrap __remote-worker < .camp-bootstrap/" + requestName
	}
	helper := strings.Join(helpers, " && ")
	if len(original) == 0 {
		return json.RawMessage(mustJSON(helper)), nil
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
		const helperKey = "00-camp-bootstrap"
		if _, exists := named[helperKey]; exists {
			return nil, errors.New("reserved lifecycle command name")
		}
		var lifecycle strings.Builder
		lifecycle.WriteString(helper)
		lifecycle.WriteString(" || exit $?\n")
		pids := make([]string, 0, len(named))
		for _, key := range sortedKeys(named) {
			if err := validateLifecycleLeaf(named[key]); err != nil {
				return nil, err
			}
			command, err := namedLifecycleShellCommand(named[key])
			if err != nil {
				return nil, err
			}
			pid := "_camp_pid_" + fmt.Sprint(len(pids))
			lifecycle.WriteString("(\n")
			lifecycle.WriteString(command)
			lifecycle.WriteString("\n) &\n")
			lifecycle.WriteString(pid)
			lifecycle.WriteString("=$!\n")
			pids = append(pids, pid)
		}
		lifecycle.WriteString("_camp_status=0\n")
		for _, pid := range pids {
			lifecycle.WriteString("wait \"$")
			lifecycle.WriteString(pid)
			lifecycle.WriteString("\" || { _camp_code=$?; [ \"$_camp_status\" -ne 0 ] || _camp_status=$_camp_code; }\n")
		}
		lifecycle.WriteString("exit \"$_camp_status\"\n")
		composed := map[string]json.RawMessage{
			helperKey: json.RawMessage(mustJSON(lifecycle.String())),
		}
		body, err := json.Marshal(composed)
		return body, err
	}
	if err := validateLifecycleLeaf(trimmed); err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return nil, err
	}
	if command, ok := value.(string); ok {
		return json.RawMessage(mustJSON(helper + " || exit $?\n" + command)), nil
	}
	return composeLifecycleLeaf(trimmed, helper)
}

func namedLifecycleShellCommand(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("malformed lifecycle command")
	}
	switch command := value.(type) {
	case string:
		return "/bin/sh -c " + shellQuote(command), nil
	case []any:
		arguments := make([]string, len(command))
		for index, argument := range command {
			value, ok := argument.(string)
			if !ok {
				return "", errors.New("mixed lifecycle argv")
			}
			arguments[index] = shellQuote(value)
		}
		return strings.Join(arguments, " "), nil
	default:
		return "", errors.New("unsupported lifecycle command form")
	}
}

func composeLifecycleLeaf(raw json.RawMessage, helper string) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("malformed lifecycle command")
	}
	switch command := value.(type) {
	case string:
		return json.RawMessage(mustJSON(helper + " && /bin/sh -c " + shellQuote(command))), nil
	case []any:
		arguments := make([]string, 0, len(command)+4)
		arguments = append(arguments, "/bin/sh", "-c", helper+` && exec "$@"`, "camp-user")
		for _, argument := range command {
			value, ok := argument.(string)
			if !ok {
				return nil, errors.New("mixed lifecycle argv")
			}
			arguments = append(arguments, value)
		}
		body, err := json.Marshal(arguments)
		return body, err
	default:
		return nil, errors.New("unsupported lifecycle command form")
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
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
	file, err := openPathBootstrap(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: %q is not regular", ErrInvalidBootstrap, path)
	}
	return file, nil
}

func writePrivateFileAt(parent *os.File, name string, body []byte, mode os.FileMode) error {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func copyOpenFileAt(source, parent *os.File, name string, mode os.FileMode) error {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	output := os.NewFile(uintptr(fd), name)
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

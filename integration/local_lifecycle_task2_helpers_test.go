package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/filebackend"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
)

type fileGenerationEvidence struct {
	Metadata      domain.GenerationMetadata
	ArchiveSHA256 string
	ArchiveSize   int64
	SidecarSHA256 string
}

type filePublicationEvidence struct {
	Pointer       domain.LatestPointer
	PointerSHA256 string
	Generation    fileGenerationEvidence
}

func TestParsePlatformManifestDigestRequiresCompleteDigestBoundManifest(t *testing.T) {
	t.Parallel()

	body := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:` + strings.Repeat("a", 64) + `","size":123},"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:` + strings.Repeat("b", 64) + `","size":456}]}`)
	sum := sha256.Sum256(body)
	headerDigest := "sha256:" + hex.EncodeToString(sum[:])

	got, err := parsePlatformManifestDigest(body, headerDigest)
	if err != nil || got != headerDigest {
		t.Fatalf("parsePlatformManifestDigest() = %q, %v; want %q", got, err, headerDigest)
	}

	for _, test := range []struct {
		name   string
		body   string
		header string
	}{
		{"header mismatch", string(body), "sha256:" + strings.Repeat("c", 64)},
		{"index is not a platform manifest", strings.Replace(string(body), "application/vnd.oci.image.manifest.v1+json", "application/vnd.oci.image.index.v1+json", 1), headerDigest},
		{"missing layers", strings.Replace(string(body), `"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":"sha256:`+strings.Repeat("b", 64)+`","size":456}]`, `"layers":[]`, 1), ""},
		{"incomplete descriptor", strings.Replace(string(body), `"size":456`, `"size":0`, 1), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := test.header
			if header == "" {
				digest := sha256.Sum256([]byte(test.body))
				header = "sha256:" + hex.EncodeToString(digest[:])
			}
			if got, err := parsePlatformManifestDigest([]byte(test.body), header); err == nil {
				t.Fatalf("parsePlatformManifestDigest() = %q, want error", got)
			}
		})
	}
}

func TestRunBoundedPTYCommandForwardsInputAndStopsAtDeadline(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := runBoundedPTYCommand(ctx, nil, "", "sh", []string{"-c", `read value; test "$value" = exit; printf pty-ok`}, "exit\n")
	if err != nil || !strings.Contains(string(output), "pty-ok") {
		t.Fatalf("bounded PTY output = %q, %v", output, err)
	}

	expired, cancelExpired := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelExpired()
	started := time.Now()
	_, err = runBoundedPTYCommand(expired, nil, "", "sh", []string{"-c", "sleep 30"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded PTY deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("bounded PTY took %s to stop after deadline", elapsed)
	}
}

func TestReadFilePublicationEvidenceBindsPointerSidecarAndArchiveBytes(t *testing.T) {
	root := t.TempDir()
	store, err := filebackend.New(root)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("immutable generation")
	sum := sha256.Sum256(body)
	ref := domain.GenerationRef{Generation: 1, ArchiveSHA256: hex.EncodeToString(sum[:])}
	objectKey, err := coordination.GenerationObjectKey("local-lifecycle", domain.Lineage{Branch: "main"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	metadataKey, err := coordination.GenerationMetadataKey("local-lifecycle", domain.Lineage{Branch: "main"}, ref)
	if err != nil {
		t.Fatal(err)
	}
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion,
		Capsule:       "local-lifecycle",
		Lineage:       domain.Lineage{Branch: "main"},
		Generation:    ref,
		ObjectKey:     objectKey,
		MetadataKey:   metadataKey,
		Size:          int64(len(body)),
		CreatedAt:     time.Unix(1, 0).UTC(),
		SessionID:     "session-a",
		Verified:      domain.Verification{LocalHaulLoadable: true},
	}
	if _, err := coordination.NewGenerationRepository(store).PutAndVerify(context.Background(), metadata, bytesSource(body)); err != nil {
		t.Fatal(err)
	}
	pointer := domain.LatestPointer{
		SchemaVersion: domain.SchemaVersion,
		Capsule:       metadata.Capsule,
		Lineage:       metadata.Lineage,
		Generation:    metadata.Generation,
		ObjectKey:     metadata.ObjectKey,
		Size:          metadata.Size,
		CreatedAt:     metadata.CreatedAt,
		SessionID:     metadata.SessionID,
	}
	if _, err := coordination.NewPointerRepository(store).Create(context.Background(), pointer); err != nil {
		t.Fatal(err)
	}

	evidence, err := readFilePublicationEvidence(context.Background(), root, "local-lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Pointer.Generation != ref || evidence.Generation.Metadata.Generation != ref ||
		evidence.Generation.ArchiveSHA256 != ref.ArchiveSHA256 || evidence.Generation.SidecarSHA256 == "" ||
		evidence.PointerSHA256 == "" {
		t.Fatalf("file publication evidence = %#v", evidence)
	}

	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(objectKey)), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileGenerationEvidence(context.Background(), root, "local-lifecycle", ref); err == nil {
		t.Fatal("tampered immutable generation was accepted")
	}
}

func parsePlatformManifestDigest(body []byte, headerDigest string) (string, error) {
	digest, err := parseWorkspaceImageDigest([]byte(strings.TrimSpace(headerDigest) + "\n"))
	if err != nil {
		return "", fmt.Errorf("parse registry platform-manifest digest: %w", err)
	}
	sum := sha256.Sum256(body)
	if actual := "sha256:" + hex.EncodeToString(sum[:]); digest != actual {
		return "", fmt.Errorf("registry platform-manifest digest %q does not match response body %q", digest, actual)
	}
	var manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
		} `json:"config"`
		Layers []struct {
			MediaType string `json:"mediaType"`
			Digest    string `json:"digest"`
			Size      int64  `json:"size"`
		} `json:"layers"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", fmt.Errorf("decode registry platform manifest: %w", err)
	}
	if manifest.SchemaVersion != 2 ||
		(manifest.MediaType != "application/vnd.oci.image.manifest.v1+json" &&
			manifest.MediaType != "application/vnd.docker.distribution.manifest.v2+json") {
		return "", fmt.Errorf("registry response is not one supported platform manifest")
	}
	if err := validateManifestDescriptor(manifest.Config.MediaType, manifest.Config.Digest, manifest.Config.Size); err != nil {
		return "", fmt.Errorf("platform-manifest config: %w", err)
	}
	if len(manifest.Layers) == 0 {
		return "", errors.New("platform manifest has no layers")
	}
	for index, layer := range manifest.Layers {
		if err := validateManifestDescriptor(layer.MediaType, layer.Digest, layer.Size); err != nil {
			return "", fmt.Errorf("platform-manifest layer %d: %w", index, err)
		}
	}
	return digest, nil
}

func readFilePublicationEvidence(ctx context.Context, root, capsule string) (filePublicationEvidence, error) {
	store, err := filebackend.New(root)
	if err != nil {
		return filePublicationEvidence{}, err
	}
	record, err := coordination.NewPointerRepository(store).Read(ctx, capsule, domain.Lineage{Branch: "main"})
	if err != nil {
		return filePublicationEvidence{}, fmt.Errorf("read file-backend pointer: %w", err)
	}
	generation, err := readFileGenerationEvidence(ctx, root, capsule, record.Pointer.Generation)
	if err != nil {
		return filePublicationEvidence{}, err
	}
	metadata := generation.Metadata
	pointer := record.Pointer
	if pointer.Capsule != metadata.Capsule || pointer.Lineage != metadata.Lineage ||
		pointer.Generation != metadata.Generation || !sameGenerationRef(pointer.Parent, metadata.Parent) ||
		pointer.ObjectKey != metadata.ObjectKey || pointer.Size != metadata.Size ||
		!pointer.CreatedAt.Equal(metadata.CreatedAt) || pointer.Tools != metadata.Tools ||
		pointer.SessionID != metadata.SessionID {
		return filePublicationEvidence{}, errors.New("file-backend pointer does not exactly match immutable generation metadata")
	}
	pointerKey, err := pointer.Lineage.PointerKey(capsule)
	if err != nil {
		return filePublicationEvidence{}, err
	}
	pointerSHA256, _, err := digestFile(filepath.Join(root, filepath.FromSlash(pointerKey)))
	if err != nil {
		return filePublicationEvidence{}, fmt.Errorf("digest file-backend pointer: %w", err)
	}
	return filePublicationEvidence{Pointer: pointer, PointerSHA256: pointerSHA256, Generation: generation}, nil
}

func readFileGenerationEvidence(ctx context.Context, root, capsule string, ref domain.GenerationRef) (fileGenerationEvidence, error) {
	store, err := filebackend.New(root)
	if err != nil {
		return fileGenerationEvidence{}, err
	}
	metadata, _, err := coordination.NewGenerationRepository(store).ReadMetadata(ctx, capsule, domain.Lineage{Branch: "main"}, ref)
	if err != nil {
		return fileGenerationEvidence{}, fmt.Errorf("read file-backend generation metadata: %w", err)
	}
	archiveSHA256, archiveSize, err := digestFile(filepath.Join(root, filepath.FromSlash(metadata.ObjectKey)))
	if err != nil {
		return fileGenerationEvidence{}, fmt.Errorf("digest file-backend generation archive: %w", err)
	}
	if archiveSHA256 != metadata.Generation.ArchiveSHA256 || archiveSize != metadata.Size {
		return fileGenerationEvidence{}, fmt.Errorf("file-backend generation archive has size %d and sha256 %s, want %d and %s", archiveSize, archiveSHA256, metadata.Size, metadata.Generation.ArchiveSHA256)
	}
	sidecarSHA256, _, err := digestFile(filepath.Join(root, filepath.FromSlash(metadata.MetadataKey)))
	if err != nil {
		return fileGenerationEvidence{}, fmt.Errorf("digest file-backend generation sidecar: %w", err)
	}
	return fileGenerationEvidence{Metadata: metadata, ArchiveSHA256: archiveSHA256, ArchiveSize: archiveSize, SidecarSHA256: sidecarSHA256}, nil
}

func sameGenerationRef(left, right *domain.GenerationRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func digestFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func validateManifestDescriptor(mediaType, digest string, size int64) error {
	if strings.TrimSpace(mediaType) == "" || size <= 0 {
		return errors.New("descriptor media type or positive size is missing")
	}
	if _, err := parseWorkspaceImageDigest([]byte(strings.TrimSpace(digest) + "\n")); err != nil {
		return fmt.Errorf("descriptor digest: %w", err)
	}
	return nil
}

func runBoundedPTYCommand(ctx context.Context, environment []string, directory, executable string, argv []string, input string) ([]byte, error) {
	if ctx == nil {
		return nil, errors.New("bounded PTY context is nil")
	}
	commandLine := shellQuote(executable)
	for _, argument := range argv {
		commandLine += " " + shellQuote(argument)
	}
	command := exec.Command("script", "--quiet", "--return", "--command", commandLine, "/dev/null")
	command.Env = mergeCommandEnvironment(os.Environ(), environment)
	command.Dir = directory
	command.Stdin = strings.NewReader(input)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		return output.Bytes(), fmt.Errorf("start bounded PTY command: %w", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	select {
	case err := <-waited:
		return output.Bytes(), err
	case <-ctx.Done():
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		select {
		case <-waited:
		case <-time.After(time.Second):
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			<-waited
		}
		return output.Bytes(), ctx.Err()
	}
}

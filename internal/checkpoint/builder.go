package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	archiveadapter "github.com/joshyorko/camp/internal/adapters/archive"
	"github.com/joshyorko/camp/internal/adapters/hauler"
	"github.com/joshyorko/camp/internal/coordination"
	"github.com/joshyorko/camp/internal/domain"
	"golang.org/x/sys/unix"
)

type RootArchiver interface {
	Create(context.Context, string, string) (archiveadapter.ArchiveInfo, error)
}

type GenerationAssembler interface {
	Assemble(context.Context, string, string, string) (hauler.GenerationArtifact, error)
}

type Builder struct {
	archive   RootArchiver
	assembler GenerationAssembler
}

type BuildRequest struct {
	Capsule    string
	Root       string
	Inventory  domain.ImageInventory
	Lineage    domain.Lineage
	Generation uint64
	Parent     *domain.GenerationRef
	SessionID  string
	CreatedAt  time.Time
	Tools      domain.ToolVersions
}

type BuildResult struct {
	InnerArchive archiveadapter.ArchiveInfo
	Artifact     hauler.GenerationArtifact
	Metadata     domain.GenerationMetadata
}

func NewBuilder(archive RootArchiver, assembler GenerationAssembler) *Builder {
	return &Builder{archive: archive, assembler: assembler}
}

func (b *Builder) Build(ctx context.Context, request BuildRequest) (BuildResult, error) {
	if b == nil || b.archive == nil || b.assembler == nil || request.Capsule == "" || request.SessionID == "" || request.Generation == 0 || request.CreatedAt.IsZero() {
		return BuildResult{}, errors.New("checkpoint build request is incomplete")
	}
	rootDirectory, err := openCheckpointRoot(request.Root)
	if err != nil {
		return BuildResult{}, err
	}
	defer rootDirectory.close()
	root := rootDirectory.path
	inventory := request.Inventory
	inventory.SchemaVersion = domain.SchemaVersion
	if inventory.Images == nil {
		inventory.Images = []domain.Image{}
	}
	sort.Slice(inventory.Images, func(i, j int) bool {
		if inventory.Images[i].CapturedReference == inventory.Images[j].CapturedReference {
			return inventory.Images[i].Platform.Architecture < inventory.Images[j].Platform.Architecture
		}
		return inventory.Images[i].CapturedReference < inventory.Images[j].CapturedReference
	})
	manifest, err := hauler.RenderManifest(request.Capsule, inventory)
	if err != nil {
		return BuildResult{}, err
	}
	inventoryBody, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return BuildResult{}, err
	}
	inventoryBody = append(inventoryBody, '\n')
	campDirectory, err := rootDirectory.openOrCreateChild(".camp")
	if err != nil {
		return BuildResult{}, err
	}
	defer campDirectory.close()
	if err := commitDocuments(campDirectory.accessPath(), inventoryBody, manifest); err != nil {
		return BuildResult{}, err
	}
	buildDirectory, err := campDirectory.openOrCreateChild("build")
	if err != nil {
		return BuildResult{}, err
	}
	defer buildDirectory.close()
	innerName := request.Capsule + ".tar.zst"
	haulName := fmt.Sprintf("%s-haul-%d.tar.zst", request.Capsule, request.Generation)
	innerPath := filepath.Join(buildDirectory.accessPath(), innerName)
	haulPath := filepath.Join(buildDirectory.accessPath(), haulName)
	for _, path := range []string{innerPath, haulPath} {
		if err := removeKnownTransient(path, buildDirectory.accessPath()); err != nil {
			return BuildResult{}, err
		}
	}
	inner, err := b.archive.Create(ctx, root, innerPath)
	if err != nil {
		return BuildResult{}, err
	}
	campPath, err := campDirectory.verifiedPath()
	if err != nil {
		return BuildResult{}, err
	}
	buildPath, err := buildDirectory.verifiedPath()
	if err != nil {
		return BuildResult{}, err
	}
	artifact, err := b.assembler.Assemble(
		ctx,
		filepath.Join(campPath, "hauler-manifest.yaml"),
		buildPath,
		filepath.Join(buildPath, haulName),
	)
	if err != nil {
		return BuildResult{}, err
	}
	inner.Path = filepath.Join(buildDirectory.path, innerName)
	artifact.Path = filepath.Join(buildDirectory.path, haulName)
	if !artifact.Validated || artifact.SHA256 == "" || artifact.Size < 0 {
		return BuildResult{}, errors.New("Hauler generation was not locally validated")
	}
	ref := domain.GenerationRef{Generation: request.Generation, ArchiveSHA256: artifact.SHA256}
	objectKey, err := coordination.GenerationObjectKey(request.Capsule, request.Lineage, ref)
	if err != nil {
		return BuildResult{}, err
	}
	metadataKey, err := coordination.GenerationMetadataKey(request.Capsule, request.Lineage, ref)
	if err != nil {
		return BuildResult{}, err
	}
	metadata := domain.GenerationMetadata{
		SchemaVersion: domain.SchemaVersion, Capsule: request.Capsule, Lineage: request.Lineage,
		Generation: ref, Parent: cloneRef(request.Parent), ObjectKey: objectKey, MetadataKey: metadataKey,
		Size: artifact.Size, CreatedAt: request.CreatedAt.UTC(), Tools: request.Tools, SessionID: request.SessionID,
		Verified: domain.Verification{LocalHaulLoadable: true},
	}
	return BuildResult{InnerArchive: inner, Artifact: artifact, Metadata: metadata}, nil
}

func commitDocuments(campDirectory string, inventory, manifest []byte) error {
	return commitDocumentsWithFault(campDirectory, inventory, manifest, nil)
}

type contentTransaction struct {
	SchemaVersion int                          `json:"schemaVersion"`
	ID            string                       `json:"id"`
	Documents     []contentTransactionDocument `json:"documents"`
}

type contentTransactionDocument struct {
	Final  string `json:"final"`
	Staged string `json:"staged"`
	SHA256 string `json:"sha256"`
}

func commitDocumentsWithFault(campDirectory string, inventory, manifest []byte, fault func(string) error) error {
	if err := os.MkdirAll(campDirectory, 0o700); err != nil {
		return err
	}
	if err := recoverContentTransaction(campDirectory); err != nil {
		return err
	}
	for _, name := range []string{".images.json.partial", ".hauler-manifest.yaml.partial"} {
		if err := removeLegacyPartial(filepath.Join(campDirectory, name)); err != nil {
			return err
		}
	}
	documents := []struct {
		name string
		body []byte
	}{{"images.json", inventory}, {"hauler-manifest.yaml", manifest}}
	if documentsMatch(campDirectory, documents) {
		return nil
	}
	pairHash := sha256.New()
	for _, document := range documents {
		_, _ = pairHash.Write([]byte(document.name))
		_, _ = pairHash.Write([]byte{0})
		_, _ = pairHash.Write(document.body)
		_, _ = pairHash.Write([]byte{0})
	}
	id := hex.EncodeToString(pairHash.Sum(nil))
	transaction := contentTransaction{SchemaVersion: domain.SchemaVersion, ID: id}
	for _, document := range documents {
		digest := sha256.Sum256(document.body)
		stagedName := "." + document.name + "." + id + ".new"
		if err := writeDurableExclusive(filepath.Join(campDirectory, stagedName), document.body); err != nil {
			return err
		}
		transaction.Documents = append(transaction.Documents, contentTransactionDocument{
			Final: document.name, Staged: stagedName, SHA256: hex.EncodeToString(digest[:]),
		})
	}
	markerBody, err := json.Marshal(transaction)
	if err != nil {
		return err
	}
	marker := filepath.Join(campDirectory, ".content-transaction.json")
	markerTemporary := marker + ".new"
	if err := writeDurableExclusive(markerTemporary, markerBody); err != nil {
		return err
	}
	if err := os.Rename(markerTemporary, marker); err != nil {
		return err
	}
	if err := syncDir(campDirectory); err != nil {
		return err
	}
	if fault != nil {
		if err := fault("after-marker"); err != nil {
			return err
		}
	}
	for index, document := range transaction.Documents {
		if err := os.Rename(filepath.Join(campDirectory, document.Staged), filepath.Join(campDirectory, document.Final)); err != nil {
			return err
		}
		if index == 0 && fault != nil {
			if err := fault("after-first-rename"); err != nil {
				return err
			}
		}
	}
	if err := syncDir(campDirectory); err != nil {
		return err
	}
	if err := verifyContentTransaction(campDirectory, transaction); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	return syncDir(campDirectory)
}

func recoverContentTransaction(campDirectory string) error {
	marker := filepath.Join(campDirectory, ".content-transaction.json")
	body, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var transaction contentTransaction
	if err := json.Unmarshal(body, &transaction); err != nil {
		return fmt.Errorf("decode pending content transaction: %w", err)
	}
	if err := validateContentTransaction(transaction); err != nil {
		return err
	}
	for _, document := range transaction.Documents {
		final := filepath.Join(campDirectory, document.Final)
		if fileMatches(final, document.SHA256) {
			_ = removeRegularIfPresent(filepath.Join(campDirectory, document.Staged))
			continue
		}
		staged := filepath.Join(campDirectory, document.Staged)
		if !fileMatches(staged, document.SHA256) {
			return fmt.Errorf("pending content transaction %s has no verified bytes for %s", transaction.ID, document.Final)
		}
		if err := os.Rename(staged, final); err != nil {
			return err
		}
	}
	if err := syncDir(campDirectory); err != nil {
		return err
	}
	if err := verifyContentTransaction(campDirectory, transaction); err != nil {
		return err
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	return syncDir(campDirectory)
}

func validateContentTransaction(transaction contentTransaction) error {
	if transaction.SchemaVersion != domain.SchemaVersion || len(transaction.ID) != 64 || len(transaction.Documents) != 2 {
		return errors.New("pending content transaction is invalid")
	}
	if _, err := hex.DecodeString(transaction.ID); err != nil {
		return errors.New("pending content transaction has invalid identity")
	}
	want := map[string]bool{"images.json": false, "hauler-manifest.yaml": false}
	for _, document := range transaction.Documents {
		if _, ok := want[document.Final]; !ok || want[document.Final] || document.Staged != "."+document.Final+"."+transaction.ID+".new" || len(document.SHA256) != 64 {
			return errors.New("pending content transaction document is invalid")
		}
		if _, err := hex.DecodeString(document.SHA256); err != nil {
			return errors.New("pending content transaction digest is invalid")
		}
		want[document.Final] = true
	}
	return nil
}

func verifyContentTransaction(campDirectory string, transaction contentTransaction) error {
	if err := validateContentTransaction(transaction); err != nil {
		return err
	}
	for _, document := range transaction.Documents {
		if !fileMatches(filepath.Join(campDirectory, document.Final), document.SHA256) {
			return fmt.Errorf("content transaction %s did not commit %s", transaction.ID, document.Final)
		}
	}
	return nil
}

func writeDurableExclusive(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(body)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func documentsMatch(campDirectory string, documents []struct {
	name string
	body []byte
}) bool {
	for _, document := range documents {
		digest := sha256.Sum256(document.body)
		if !fileMatches(filepath.Join(campDirectory, document.name), hex.EncodeToString(digest[:])) {
			return false
		}
	}
	return true
}

func fileMatches(path, digest string) bool {
	body, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	actual := sha256.Sum256(body)
	return hex.EncodeToString(actual[:]) == digest
}

func removeLegacyPartial(path string) error {
	return removeRegularIfPresent(path)
}

func removeRegularIfPresent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unexplained checkpoint content partial")
	}
	return os.Remove(path)
}

func removeKnownTransient(path, buildDirectory string) error {
	if filepath.Dir(path) != buildDirectory {
		return errors.New("transient build path escaped build directory")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unexplained checkpoint transient")
	}
	return os.Remove(path)
}

func cloneRef(reference *domain.GenerationRef) *domain.GenerationRef {
	if reference == nil {
		return nil
	}
	copy := *reference
	return &copy
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

type checkpointDirectory struct {
	fd   int
	path string
}

func openCheckpointRoot(path string) (checkpointDirectory, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return checkpointDirectory{}, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return checkpointDirectory{}, errors.New("checkpoint root must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return checkpointDirectory{}, err
	}
	fd, err := unix.Open(canonical, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return checkpointDirectory{}, err
	}
	return checkpointDirectory{fd: fd, path: canonical}, nil
}

func (d checkpointDirectory) openOrCreateChild(name string) (checkpointDirectory, error) {
	if err := unix.Mkdirat(d.fd, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return checkpointDirectory{}, err
	}
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	fd, err := unix.Openat2(d.fd, name, how)
	if err != nil {
		return checkpointDirectory{}, fmt.Errorf("checkpoint directory %q is unsafe: %w", filepath.Join(d.path, name), err)
	}
	return checkpointDirectory{fd: fd, path: filepath.Join(d.path, name)}, nil
}

func (d checkpointDirectory) accessPath() string {
	return fmt.Sprintf("/proc/self/fd/%d", d.fd)
}

func (d checkpointDirectory) verifiedPath() (string, error) {
	parentHow := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_NO_SYMLINKS,
	}
	parentFD, err := unix.Openat2(unix.AT_FDCWD, filepath.Dir(d.path), parentHow)
	if err != nil {
		return "", fmt.Errorf("reopen checkpoint parent for %q: %w", d.path, err)
	}
	defer unix.Close(parentFD)
	how := &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV,
	}
	currentFD, err := unix.Openat2(parentFD, filepath.Base(d.path), how)
	if err != nil {
		return "", fmt.Errorf("reopen checkpoint directory %q: %w", d.path, err)
	}
	defer unix.Close(currentFD)
	var held, current unix.Stat_t
	if err := unix.Fstat(d.fd, &held); err != nil {
		return "", fmt.Errorf("inspect held checkpoint directory %q: %w", d.path, err)
	}
	if err := unix.Fstat(currentFD, &current); err != nil {
		return "", fmt.Errorf("inspect reopened checkpoint directory %q: %w", d.path, err)
	}
	if held.Dev != current.Dev || held.Ino != current.Ino {
		return "", fmt.Errorf("checkpoint directory %q changed identity", d.path)
	}
	return d.path, nil
}

func (d checkpointDirectory) close() {
	_ = unix.Close(d.fd)
}

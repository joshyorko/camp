package registry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type Barrier interface {
	WithCut(context.Context, SnapshotRequest, func() error) error
}

type SnapshotRequest struct {
	OverlayRoot     string
	SnapshotRoot    string
	CatalogEndpoint string
	SessionID       string
}

type Snapshot struct {
	Root       string                    `json:"root"`
	References []ports.RegistryReference `json:"references"`
}

type Snapshotter struct {
	catalog ports.RegistryCatalog
	barrier Barrier
}

func NewSnapshotter(catalog ports.RegistryCatalog, barrier Barrier) *Snapshotter {
	return &Snapshotter{catalog: catalog, barrier: barrier}
}

func (s *Snapshotter) Seal(ctx context.Context, request SnapshotRequest) (Snapshot, error) {
	if s == nil || s.catalog == nil || s.barrier == nil || request.CatalogEndpoint == "" {
		return Snapshot{}, errors.New("registry snapshot dependencies or request are incomplete")
	}
	overlay, err := secureDirectory(request.OverlayRoot)
	if err != nil {
		return Snapshot{}, fmt.Errorf("registry overlay: %w", err)
	}
	destination, err := filepath.Abs(request.SnapshotRoot)
	if err != nil || destination == overlay {
		return Snapshot{}, errors.New("invalid registry snapshot destination")
	}
	if relative, relErr := filepath.Rel(overlay, destination); relErr == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Snapshot{}, errors.New("registry snapshot destination is inside mutable overlay")
	}
	if _, err := os.Lstat(destination); err == nil {
		return Snapshot{}, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Snapshot{}, err
	}
	temporary, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+".partial-")
	if err != nil {
		return Snapshot{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	var references []ports.RegistryReference
	references, err = s.catalog.List(ctx, request.CatalogEndpoint)
	if err != nil {
		return Snapshot{}, err
	}
	err = s.barrier.WithCut(ctx, request, func() error {
		if err := copyRegistryTree(overlay, temporary); err != nil {
			return err
		}
		return syncDirectory(temporary)
	})
	if err != nil {
		return Snapshot{}, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return Snapshot{}, err
	}
	committed = true
	if err := syncDirectory(parent); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Root: destination, References: references}, nil
}

func MergeCatalog(inventory domain.ImageInventory, authority string, references []ports.RegistryReference, generatedAt time.Time) (domain.ImageInventory, error) {
	if inventory.SchemaVersion != 0 && inventory.SchemaVersion != domain.SchemaVersion {
		return domain.ImageInventory{}, errors.New("image inventory schema is unsupported")
	}
	if generatedAt.IsZero() {
		return domain.ImageInventory{}, errors.New("catalog merge timestamp is empty")
	}
	if err := validateAuthority(authority); err != nil {
		return domain.ImageInventory{}, err
	}
	result := domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: generatedAt.UTC(), Images: append([]domain.Image(nil), inventory.Images...)}
	seen := make(map[string]string, len(result.Images))
	for _, image := range result.Images {
		if prior, exists := seen[image.CapturedReference]; exists && prior != image.CapturedManifestDigest {
			return domain.ImageInventory{}, fmt.Errorf("inventory contains digest drift for %q: %w", image.CapturedReference, ErrRegistryDigestMismatch)
		}
		seen[image.CapturedReference] = image.CapturedManifestDigest
	}
	for _, reference := range references {
		if !repositoryPattern.MatchString(reference.Repository) || !tagPattern.MatchString(reference.Tag) || !digestPattern.MatchString(reference.ManifestDigest) {
			return domain.ImageInventory{}, errors.New("registry catalog contains an invalid reference")
		}
		captured := authority + "/" + reference.Repository + ":" + reference.Tag
		if prior, exists := seen[captured]; exists {
			if prior != reference.ManifestDigest {
				return domain.ImageInventory{}, fmt.Errorf("catalog tag %q moved from %s to %s: %w", captured, prior, reference.ManifestDigest, ErrRegistryDigestMismatch)
			}
			continue
		}
		seen[captured] = reference.ManifestDigest
		result.Images = append(result.Images, domain.Image{
			CapturedReference: captured, CapturedManifestDigest: reference.ManifestDigest,
			Source: domain.ImageSourceRegistry, CreatedAt: generatedAt.UTC(),
		})
	}
	sort.Slice(result.Images, func(i, j int) bool {
		if result.Images[i].CapturedReference == result.Images[j].CapturedReference {
			return result.Images[i].CapturedManifestDigest < result.Images[j].CapturedManifestDigest
		}
		return result.Images[i].CapturedReference < result.Images[j].CapturedReference
	})
	return result, nil
}

func validateAuthority(authority string) error {
	if strings.Contains(authority, "://") || strings.ContainsAny(authority, "/@?#%\\ \t\r\n\x00") {
		return errors.New("invalid registry authority")
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" || port == "" {
		return errors.New("invalid registry authority")
	}
	return nil
}

func secureDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("path is not a real directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != absolute {
		return "", errors.New("path has unexplained symlink resolution")
	}
	return canonical, nil
}

func copyRegistryTree(source, destination string) error {
	inodes := make(map[[2]uint64]string)
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("registry snapshot path escaped overlay")
		}
		if relative == "." {
			return os.Chmod(destination, info.Mode().Perm())
		}
		target := filepath.Join(destination, relative)
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() && !info.IsDir() {
			return fmt.Errorf("registry snapshot rejects special entry %q", relative)
		}
		if info.IsDir() {
			return os.Mkdir(target, info.Mode().Perm())
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
			key := [2]uint64{uint64(stat.Dev), stat.Ino}
			if prior, exists := inodes[key]; exists {
				return os.Link(prior, target)
			}
			inodes[key] = target
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.Copy(output, input)
		if copyErr == nil {
			copyErr = output.Sync()
		}
		closeOut := output.Close()
		closeIn := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeOut != nil {
			return closeOut
		}
		if closeIn != nil {
			return closeIn
		}
		return os.Chtimes(target, info.ModTime(), info.ModTime())
	})
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

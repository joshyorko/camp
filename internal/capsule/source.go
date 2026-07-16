package capsule

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var (
	ErrUnsafeSource   = errors.New("unsafe capsule source")
	ErrSourceConflict = errors.New("local and remote capsule sources conflict")
	ErrNoSource       = errors.New("no capsule source is configured")
)

type SourceKind string

const (
	SourceAdopted SourceKind = "adopted"
	SourceRemote  SourceKind = "remote"
)

type SourceRequest struct {
	Capsule         string
	ExplicitPath    string
	ConfiguredPath  string
	RemoteAvailable bool
}

type Source struct {
	Kind        SourceKind
	Root        string
	Initialized bool
}

func ResolveSource(request SourceRequest) (Source, error) {
	if request.ExplicitPath != "" {
		root, initialized, err := validateSourceRoot(request.ExplicitPath)
		if err != nil {
			return Source{}, err
		}
		return Source{Kind: SourceAdopted, Root: root, Initialized: initialized}, nil
	}
	if request.ConfiguredPath != "" {
		root, initialized, err := validateSourceRoot(request.ConfiguredPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && request.RemoteAvailable {
				return Source{Kind: SourceRemote}, nil
			}
			return Source{}, err
		}
		if request.RemoteAvailable && !initialized {
			return Source{}, fmt.Errorf("configured local root %q is uninitialized while remote %q exists: %w", root, request.Capsule, ErrSourceConflict)
		}
		return Source{Kind: SourceAdopted, Root: root, Initialized: initialized}, nil
	}
	if request.RemoteAvailable {
		return Source{Kind: SourceRemote}, nil
	}
	return Source{}, fmt.Errorf("run `camp init /path/to/root` or configure a backend containing capsule %q: %w", request.Capsule, ErrNoSource)
}

func validateSourceRoot(path string) (string, bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("resolve source root: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", false, fmt.Errorf("source root %q must be a real directory: %w", absolute, ErrUnsafeSource)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != absolute {
		return "", false, fmt.Errorf("source root %q has unexplained symlink resolution: %w", absolute, ErrUnsafeSource)
	}
	metadata := filepath.Join(canonical, ".camp", "capsule.yaml")
	metadataInfo, err := os.Lstat(metadata)
	if err == nil {
		if metadataInfo.Mode().IsRegular() && metadataInfo.Mode()&os.ModeSymlink == 0 {
			return canonical, true, nil
		}
		return "", false, fmt.Errorf("capsule metadata is not a regular file: %w", ErrUnsafeSource)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	return canonical, false, nil
}

package capsule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrOwnershipMismatch = errors.New("materialization ownership mismatch")

type Ownership struct {
	materializationRoot string
}

func (o *Ownership) MaterializationRoot() string {
	if o == nil {
		return ""
	}
	return o.materializationRoot
}

type ownershipMarker struct {
	Token         string `json:"token"`
	CanonicalPath string `json:"canonicalPath"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
}

func NewOwnership(dataHome string) (*Ownership, error) {
	if dataHome == "" {
		return nil, errors.New("XDG data home is empty")
	}
	root, err := filepath.Abs(filepath.Join(dataHome, "camp", "materializations"))
	if err != nil {
		return nil, fmt.Errorf("resolve materialization root: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create materialization root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("canonicalize materialization root: %w", err)
	}
	return &Ownership{materializationRoot: canonical}, nil
}

func (o *Ownership) Adopt(path string) (domain.Materialization, error) {
	canonical, original, device, inode, err := inspectRoot(path)
	if err != nil {
		return domain.Materialization{}, err
	}
	return domain.Materialization{
		SchemaVersion:    domain.SchemaVersion,
		CanonicalPath:    canonical,
		OriginalPath:     original,
		Mode:             domain.MaterializationAdopted,
		Device:           device,
		Inode:            inode,
		CleanupPermitted: false,
	}, nil
}

func (o *Ownership) MarkCreated(path string) (domain.Materialization, error) {
	token, err := NewOwnershipToken()
	if err != nil {
		return domain.Materialization{}, err
	}
	return o.MarkCreatedWithToken(path, token)
}

func NewOwnershipToken() (string, error) {
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate ownership token: %w", err)
	}
	return hex.EncodeToString(tokenBytes), nil
}

func (o *Ownership) MarkCreatedWithToken(path, token string) (domain.Materialization, error) {
	if o == nil || o.materializationRoot == "" {
		return domain.Materialization{}, errors.New("ownership root is unavailable")
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return domain.Materialization{}, fmt.Errorf("ownership token is invalid: %w", ErrOwnershipMismatch)
	}
	canonical, original, device, inode, err := inspectRoot(path)
	if err != nil {
		return domain.Materialization{}, err
	}
	if !contained(o.materializationRoot, canonical) {
		return domain.Materialization{}, fmt.Errorf("created root %q is outside %q: %w", canonical, o.materializationRoot, ErrOwnershipMismatch)
	}
	materialization := domain.Materialization{
		SchemaVersion:    domain.SchemaVersion,
		CanonicalPath:    canonical,
		OriginalPath:     original,
		OwnershipMarker:  token,
		Mode:             domain.MaterializationCreated,
		Device:           device,
		Inode:            inode,
		CleanupPermitted: true,
	}
	marker := ownershipMarker{Token: token, CanonicalPath: canonical, Device: device, Inode: inode}
	body, err := json.Marshal(marker)
	if err != nil {
		return domain.Materialization{}, err
	}
	runtimeDirectory := filepath.Join(canonical, ".camp", "runtime")
	if err := ensureMarkerDirectory(canonical); err != nil {
		return domain.Materialization{}, fmt.Errorf("create ownership marker directory: %w", err)
	}
	markerPath := filepath.Join(runtimeDirectory, "ownership.json")
	if existing, readErr := os.Lstat(markerPath); readErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || !existing.Mode().IsRegular() {
			return domain.Materialization{}, fmt.Errorf("ownership marker is not a regular file: %w", ErrOwnershipMismatch)
		}
		existingBody, readErr := os.ReadFile(markerPath)
		if readErr != nil || len(existingBody) > 4096 {
			return domain.Materialization{}, fmt.Errorf("read ownership marker: %w", ErrOwnershipMismatch)
		}
		var existingMarker ownershipMarker
		if json.Unmarshal(existingBody, &existingMarker) != nil || existingMarker != marker {
			return domain.Materialization{}, ErrOwnershipMismatch
		}
		return materialization, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return domain.Materialization{}, readErr
	}
	file, err := os.OpenFile(markerPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.Materialization{}, fmt.Errorf("create ownership marker: %w", err)
	}
	if _, err := file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return domain.Materialization{}, fmt.Errorf("write ownership marker: %w", err)
	}
	if closeErr != nil {
		return domain.Materialization{}, fmt.Errorf("close ownership marker: %w", closeErr)
	}
	if err := syncDir(runtimeDirectory); err != nil {
		return domain.Materialization{}, fmt.Errorf("sync ownership marker: %w", err)
	}
	return materialization, nil
}

func ensureMarkerDirectory(root string) error {
	current := root
	for _, component := range []string{".camp", "runtime"} {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("ownership marker parent %q is not a real directory: %w", current, ErrOwnershipMismatch)
		}
	}
	return nil
}

func validateMarkerDirectory(root string) error {
	current := root
	for _, component := range []string{".camp", "runtime"} {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("ownership marker parent %q is not a real directory", current)
		}
	}
	return nil
}

func (o *Ownership) Revalidate(materialization domain.Materialization) error {
	if o == nil || o.materializationRoot == "" || materialization.SchemaVersion != domain.SchemaVersion {
		return ErrOwnershipMismatch
	}
	if materialization.Mode == domain.MaterializationCreated {
		_, err := o.revalidateCreated(materialization)
		return err
	}
	if materialization.Mode != domain.MaterializationAdopted || materialization.CleanupPermitted || materialization.OwnershipMarker != "" {
		return ErrOwnershipMismatch
	}
	canonical, original, device, inode, err := inspectRoot(materialization.OriginalPath)
	if err != nil {
		return fmt.Errorf("revalidate adopted materialization: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	if canonical != materialization.CanonicalPath || original != materialization.OriginalPath || device != materialization.Device || inode != materialization.Inode {
		return ErrOwnershipMismatch
	}
	return nil
}

func (o *Ownership) revalidateCreated(materialization domain.Materialization) (string, error) {
	if materialization.Mode != domain.MaterializationCreated || !materialization.CleanupPermitted || materialization.OwnershipMarker == "" {
		return "", ErrOwnershipMismatch
	}
	decoded, err := hex.DecodeString(materialization.OwnershipMarker)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != materialization.OwnershipMarker {
		return "", ErrOwnershipMismatch
	}
	canonical, original, device, inode, err := inspectRoot(materialization.CanonicalPath)
	if err != nil {
		return "", fmt.Errorf("revalidate materialization: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	if canonical != materialization.CanonicalPath || original != materialization.OriginalPath || !contained(o.materializationRoot, canonical) || device != materialization.Device || inode != materialization.Inode {
		return "", ErrOwnershipMismatch
	}
	if err := validateMarkerDirectory(canonical); err != nil {
		return "", fmt.Errorf("validate ownership marker directory: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	markerPath := filepath.Join(canonical, ".camp", "runtime", "ownership.json")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil {
		return "", fmt.Errorf("inspect ownership marker: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	if markerInfo.Mode()&os.ModeSymlink != 0 || !markerInfo.Mode().IsRegular() {
		return "", fmt.Errorf("ownership marker is not a regular file: %w", ErrOwnershipMismatch)
	}
	file, err := os.Open(markerPath)
	if err != nil {
		return "", fmt.Errorf("open ownership marker: %w", ErrOwnershipMismatch)
	}
	body, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) > 4096 {
		return "", ErrOwnershipMismatch
	}
	var marker ownershipMarker
	if err := json.Unmarshal(body, &marker); err != nil || marker.Token != materialization.OwnershipMarker || marker.CanonicalPath != canonical || marker.Device != device || marker.Inode != inode {
		return "", ErrOwnershipMismatch
	}
	return canonical, nil
}

func (o *Ownership) RemoveOwned(ctx context.Context, materialization domain.Materialization) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if materialization.Mode == domain.MaterializationAdopted {
		return false, nil
	}
	canonical, err := o.revalidateCreated(materialization)
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(canonical)
	if err := os.RemoveAll(canonical); err != nil {
		return false, fmt.Errorf("remove owned materialization: %w", err)
	}
	if err := syncDir(parent); err != nil {
		return false, fmt.Errorf("sync materialization removal: %w", err)
	}
	return true, nil
}

func inspectRoot(path string) (canonical, original string, device, inode uint64, err error) {
	if path == "" {
		return "", "", 0, 0, errors.New("materialization path is empty")
	}
	original, err = filepath.Abs(path)
	if err != nil {
		return "", "", 0, 0, err
	}
	info, err := os.Lstat(original)
	if err != nil {
		return "", "", 0, 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", "", 0, 0, errors.New("materialization root must be a real directory")
	}
	canonical, err = filepath.EvalSymlinks(original)
	if err != nil {
		return "", "", 0, 0, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", "", 0, 0, errors.New("materialization identity unavailable")
	}
	return canonical, original, uint64(stat.Dev), stat.Ino, nil
}

func contained(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) && relative[:min(len(relative), 3)] != "../"
}

func syncDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

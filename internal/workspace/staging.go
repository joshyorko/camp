package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

var mirrorAttemptPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

type MirrorStaging struct{ root string }

func NewMirrorStaging(root string) *MirrorStaging {
	return &MirrorStaging{root: filepath.Clean(root)}
}

func (s *MirrorStaging) Fresh(ctx context.Context, requestedRoot, attemptID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s == nil || !filepath.IsAbs(s.root) || !filepath.IsAbs(requestedRoot) || !mirrorAttemptPattern.MatchString(attemptID) {
		return "", errors.New("remote mirror staging identity is invalid")
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", err
	}
	destination := filepath.Join(s.root, attemptID)
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", err
	}
	return destination, nil
}

func (s *MirrorStaging) Discard(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || filepath.Dir(root) != s.root || !mirrorAttemptPattern.MatchString(filepath.Base(root)) {
		return errors.New("remote mirror discard identity is invalid")
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("remote mirror staging root is not an owned directory")
	}
	return os.RemoveAll(root)
}

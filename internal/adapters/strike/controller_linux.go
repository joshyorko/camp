//go:build linux

package strike

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joshyorko/camp/internal/app"
	"golang.org/x/sys/unix"
)

type Controller struct{ now func() time.Time }

func NewController(now func() time.Time) *Controller { return &Controller{now: now} }

func (c *Controller) Archive(ctx context.Context, plan app.StrikePlan) (string, error) {
	unlock, err := lock(plan.DataRoot)
	if err != nil {
		return "", err
	}
	defer unlock()
	targets, err := verifiedTargets(plan)
	if err != nil {
		return "", err
	}
	archive := plan.DataRoot + ".strike-" + c.now().UTC().Format("20060102T150405Z")
	if err := os.Mkdir(archive, 0o700); err != nil {
		return "", fmt.Errorf("create strike archive: %w", err)
	}
	moved := make([]string, 0, len(targets))
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return archive, err
		}
		if err := verifyTarget(plan.DataRoot, target); err != nil {
			return archive, err
		}
		dst := filepath.Join(archive, filepath.Base(target))
		if err := os.Rename(target, dst); err != nil {
			return archive, fmt.Errorf("archive %s: %w", filepath.Base(target), err)
		}
		moved = append(moved, filepath.Base(target))
	}
	body, _ := json.MarshalIndent(struct {
		CreatedAt time.Time `json:"createdAt"`
		Moved     []string  `json:"moved"`
	}{c.now().UTC(), moved}, "", "  ")
	if err := os.WriteFile(filepath.Join(archive, "manifest.json"), append(body, '\n'), 0o600); err != nil {
		return archive, err
	}
	return archive, nil
}

func (c *Controller) Purge(ctx context.Context, plan app.StrikePlan) error {
	unlock, err := lock(plan.DataRoot)
	if err != nil {
		return err
	}
	defer unlock()
	targets, err := verifiedTargets(plan)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := verifyTarget(plan.DataRoot, target); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("purge %s: %w", filepath.Base(target), err)
		}
	}
	return nil
}

func verifiedTargets(plan app.StrikePlan) ([]string, error) {
	root := filepath.Clean(plan.DataRoot)
	if !filepath.IsAbs(root) || root == "/" {
		return nil, errors.New("strike data root must be an absolute non-root path")
	}
	targets := make([]string, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if err := verifyTarget(root, target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func verifyTarget(root, target string) error {
	target = filepath.Clean(target)
	if filepath.Dir(target) != root {
		return fmt.Errorf("strike target is not a direct child of data root: %s", target)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("strike target must be a real directory: %s", target)
	}
	return nil
}

func lock(root string) (func(), error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(root, ".strike.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("another Camp controller operation is active")
	}
	return func() {
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		_ = file.Close()
	}, nil
}

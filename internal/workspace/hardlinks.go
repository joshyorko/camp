package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"syscall"

	"github.com/joshyorko/camp/internal/ports"
)

type HardlinkRestoreRequest struct {
	WorkspaceID string `json:"workspaceId"`
	Context     string `json:"context"`
	LocalRoot   string `json:"localRoot"`
	RemoteRoot  string `json:"remoteRoot"`
}

type workspaceCommandExecutor interface {
	Execute(context.Context, ports.WorkspaceCommand) (ports.Result, error)
}

type HardlinkRestorer struct{ executor workspaceCommandExecutor }

func NewHardlinkRestorer(executor workspaceCommandExecutor) *HardlinkRestorer {
	return &HardlinkRestorer{executor: executor}
}

func (r *HardlinkRestorer) Restore(ctx context.Context, request HardlinkRestoreRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.executor == nil || request.WorkspaceID == "" {
		return errors.New("incomplete hardlink restore request")
	}
	if !safeHardlinkRoot(request.LocalRoot) || filepath.Clean(request.LocalRoot) == string(filepath.Separator) {
		return errors.New("invalid local root")
	}
	if !safeHardlinkRoot(request.RemoteRoot) || filepath.Clean(request.RemoteRoot) == string(filepath.Separator) {
		return errors.New("invalid remote root")
	}
	info, err := os.Lstat(request.LocalRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("invalid local root")
	}
	type identity struct{ device, inode uint64 }
	groups := make(map[identity][]string)
	err = filepath.WalkDir(request.LocalRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		stat, ok := fileInfo.Sys().(*syscall.Stat_t)
		if !ok || !fileInfo.Mode().IsRegular() || stat.Nlink < 2 {
			return nil
		}
		relative, err := filepath.Rel(request.LocalRoot, path)
		if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) {
			return errors.New("unsafe hardlink path")
		}
		groups[identity{device: uint64(stat.Dev), inode: stat.Ino}] = append(groups[identity{device: uint64(stat.Dev), inode: stat.Ino}], filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		return err
	}
	ordered := make([][]string, 0, len(groups))
	for _, paths := range groups {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths)
		ordered = append(ordered, paths)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i][0] < ordered[j][0] })
	for _, paths := range ordered {
		for _, target := range paths[1:] {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := r.executor.Execute(ctx, ports.WorkspaceCommand{WorkspaceID: request.WorkspaceID, Context: request.Context, Workdir: request.RemoteRoot, Argv: []string{"ln", "--force", "--", paths[0], target}})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func safeHardlinkRoot(root string) bool {
	return root != "" && filepath.IsAbs(root) && filepath.Clean(root) == root
}

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrLogOwnership = errors.New("service log is outside the owned log root")

type LogChunk struct {
	Bytes     []byte
	Truncated bool
}

type ServiceLogReader struct {
	root     string
	maxBytes int64
}

func NewServiceLogReader(root string, maxBytes int64) (*ServiceLogReader, error) {
	if root == "" || maxBytes < 1 {
		return nil, errors.New("service log reader root or bound is invalid")
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve service log root: %w", err)
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.Join(err, ErrLogOwnership)
	}
	return &ServiceLogReader{root: canonical, maxBytes: maxBytes}, nil
}

func (r *ServiceLogReader) ReadTail(ctx context.Context, record domain.ServiceUnitRecord, limit int64) (LogChunk, error) {
	if r == nil || record.Name == "" || record.LogPath == "" || limit < 1 || limit > r.maxBytes {
		return LogChunk{}, ErrLogOwnership
	}
	if err := ctx.Err(); err != nil {
		return LogChunk{}, err
	}
	path, err := filepath.Abs(record.LogPath)
	if err != nil {
		return LogChunk{}, errors.Join(err, ErrLogOwnership)
	}
	relative, err := filepath.Rel(r.root, path)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return LogChunk{}, ErrLogOwnership
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return LogChunk{}, errors.Join(err, ErrLogOwnership)
	}
	named, err := os.Lstat(path)
	if err != nil || !named.Mode().IsRegular() || named.Mode().Perm()&0o077 != 0 {
		return LogChunk{}, errors.Join(err, ErrLogOwnership)
	}
	file, err := os.Open(path)
	if err != nil {
		return LogChunk{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(named, opened) {
		return LogChunk{}, errors.Join(err, ErrLogOwnership)
	}
	if stat, ok := opened.Sys().(*syscall.Stat_t); !ok || stat.Nlink != 1 {
		return LogChunk{}, ErrLogOwnership
	}
	offset := opened.Size() - limit
	truncated := offset > 0
	if offset < 0 {
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return LogChunk{}, err
	}
	body, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return LogChunk{}, err
	}
	if err := ctx.Err(); err != nil {
		return LogChunk{}, err
	}
	return LogChunk{Bytes: body, Truncated: truncated}, nil
}

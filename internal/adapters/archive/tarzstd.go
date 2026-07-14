package archive

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/klauspost/compress/zstd"
)

var (
	ErrUnsafeArchive      = errors.New("unsafe archive entry")
	ErrRootSnapshotStable = errors.New("root changed while snapshotting")
)

type ArchiveInfo struct {
	Path   string
	SHA256 string
	Size   int64
}

type TarZstd struct {
	afterInventory func() error
}

type Option func(*TarZstd)

func WithAfterInventory(hook func() error) Option {
	return func(adapter *TarZstd) { adapter.afterInventory = hook }
}

func NewTarZstd(options ...Option) *TarZstd {
	adapter := &TarZstd{}
	for _, option := range options {
		option(adapter)
	}
	return adapter
}

type sourceEntry struct {
	path       string
	name       string
	mode       os.FileMode
	size       int64
	mtime      int64
	device     uint64
	inode      uint64
	links      uint64
	linkTarget string
	xattrs     map[string][]byte
}

func (a *TarZstd) Create(ctx context.Context, root, destination string) (ArchiveInfo, error) {
	canonicalRoot, err := canonicalRoot(root)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if !filepath.IsAbs(destination) {
		return ArchiveInfo{}, errors.New("archive destination must be absolute")
	}
	if _, err := os.Lstat(destination); err == nil {
		return ArchiveInfo{}, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return ArchiveInfo{}, err
	}
	entries, err := inventory(ctx, canonicalRoot)
	if err != nil {
		return ArchiveInfo{}, err
	}
	if a.afterInventory != nil {
		if err := a.afterInventory(); err != nil {
			return ArchiveInfo{}, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return ArchiveInfo{}, err
	}
	partial := destination + ".partial"
	file, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ArchiveInfo{}, err
	}
	cleanup := func() { _ = os.Remove(partial) }
	encoder, err := zstd.NewWriter(file, zstd.WithEncoderConcurrency(1), zstd.WithEncoderCRC(true))
	if err != nil {
		_ = file.Close()
		cleanup()
		return ArchiveInfo{}, err
	}
	tw := tar.NewWriter(encoder)
	hardlinks := make(map[string]string)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			_ = tw.Close()
			_ = encoder.Close()
			_ = file.Close()
			cleanup()
			return ArchiveInfo{}, err
		}
		current, err := inspectSourceEntry(entry.path, entry.name)
		if err != nil || !sameEntry(entry, current) {
			_ = tw.Close()
			_ = encoder.Close()
			_ = file.Close()
			cleanup()
			return ArchiveInfo{}, fmt.Errorf("entry %q changed before read: %w", entry.name, ErrRootSnapshotStable)
		}
		info, err := os.Lstat(entry.path)
		if err != nil {
			cleanup()
			return ArchiveInfo{}, err
		}
		header, err := tar.FileInfoHeader(info, entry.linkTarget)
		if err != nil {
			cleanup()
			return ArchiveInfo{}, err
		}
		header.Name = filepath.ToSlash(entry.name)
		header.Format = tar.FormatPAX
		header.AccessTime = time.Time{}
		header.ChangeTime = time.Time{}
		header.PAXRecords = xattrPAX(entry.xattrs)
		if entry.mode.IsRegular() && entry.links > 1 {
			key := strconv.FormatUint(entry.device, 10) + ":" + strconv.FormatUint(entry.inode, 10)
			if first, ok := hardlinks[key]; ok {
				header.Typeflag = tar.TypeLink
				header.Linkname = first
				header.Size = 0
			} else {
				hardlinks[key] = header.Name
			}
		}
		if err := tw.WriteHeader(header); err != nil {
			cleanup()
			return ArchiveInfo{}, err
		}
		if entry.mode.IsRegular() && header.Typeflag != tar.TypeLink {
			fd, err := syscall.Open(entry.path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
			if err != nil {
				cleanup()
				return ArchiveInfo{}, err
			}
			input := os.NewFile(uintptr(fd), entry.path)
			written, copyErr := io.CopyN(tw, input, entry.size)
			var extra [1]byte
			extraCount, extraErr := input.Read(extra[:])
			closeErr := input.Close()
			if copyErr != nil || written != entry.size || extraCount != 0 || extraErr != io.EOF || closeErr != nil {
				cleanup()
				return ArchiveInfo{}, fmt.Errorf("entry %q changed during read: %w", entry.name, ErrRootSnapshotStable)
			}
			after, err := inspectSourceEntry(entry.path, entry.name)
			if err != nil || !sameEntry(entry, after) {
				cleanup()
				return ArchiveInfo{}, fmt.Errorf("entry %q changed after read: %w", entry.name, ErrRootSnapshotStable)
			}
		}
	}
	if err := tw.Close(); err != nil {
		_ = encoder.Close()
		_ = file.Close()
		cleanup()
		return ArchiveInfo{}, err
	}
	if err := encoder.Close(); err != nil {
		_ = file.Close()
		cleanup()
		return ArchiveInfo{}, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return ArchiveInfo{}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return ArchiveInfo{}, err
	}
	afterEntries, err := inventory(ctx, canonicalRoot)
	if err != nil || !sameInventory(entries, afterEntries) {
		cleanup()
		return ArchiveInfo{}, fmt.Errorf("root changed after archive: %w", ErrRootSnapshotStable)
	}
	if err := os.Rename(partial, destination); err != nil {
		cleanup()
		return ArchiveInfo{}, err
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return ArchiveInfo{}, err
	}
	body, err := os.ReadFile(destination)
	if err != nil {
		return ArchiveInfo{}, err
	}
	digest := sha256.Sum256(body)
	return ArchiveInfo{Path: destination, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}, nil
}

func (a *TarZstd) Extract(ctx context.Context, source, destination string) (err error) {
	if !filepath.IsAbs(source) || !filepath.IsAbs(destination) {
		return errors.New("archive source and destination must be absolute")
	}
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	stage := destination + ".partial"
	if _, err := os.Lstat(stage); err == nil {
		return fmt.Errorf("unexplained extraction stage exists: %w", ErrUnsafeArchive)
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		return err
	}
	marker := filepath.Join(stage, ".camp-extract-owner")
	if err := os.WriteFile(marker, []byte("camp-owned-partial\n"), 0o600); err != nil {
		_ = os.RemoveAll(stage)
		return err
	}
	defer func() {
		if err != nil {
			if body, readErr := os.ReadFile(marker); readErr == nil && string(body) == "camp-owned-partial\n" {
				_ = os.RemoveAll(stage)
			}
		}
	}()
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	decoder, err := zstd.NewReader(input, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer decoder.Close()
	tr := tar.NewReader(decoder)
	seen := make(map[string]byte)
	regular := make(map[string]struct{})
	var directories []directoryMetadata
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return fmt.Errorf("read archive: %w", nextErr)
		}
		name, err := safeArchiveName(header.Name)
		if err != nil {
			return err
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("duplicate archive path %q: %w", name, ErrUnsafeArchive)
		}
		target := filepath.Join(stage, filepath.FromSlash(name))
		if !within(stage, target) {
			return ErrUnsafeArchive
		}
		if err := ensureSafeParents(stage, filepath.Dir(target)); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(target, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			seen[name] = tar.TypeDir
			directories = append(directories, directoryMetadata{path: target, header: *header})
		case tar.TypeReg, tar.TypeRegA:
			file, err := openNewRegular(target)
			if err != nil {
				return err
			}
			written, copyErr := io.CopyN(file, tr, header.Size)
			syncErr := file.Sync()
			closeErr := file.Close()
			if copyErr != nil || written != header.Size || syncErr != nil || closeErr != nil {
				return fmt.Errorf("write archive file %q: %w", name, ErrUnsafeArchive)
			}
			if err := applyMetadata(target, header); err != nil {
				return err
			}
			seen[name] = tar.TypeReg
			regular[name] = struct{}{}
		case tar.TypeSymlink:
			if err := validateLink(stage, name, header.Linkname); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
			seen[name] = tar.TypeSymlink
		case tar.TypeLink:
			linkname, err := safeArchiveName(header.Linkname)
			if err != nil {
				return err
			}
			if _, ok := regular[linkname]; !ok {
				return fmt.Errorf("hardlink target %q is not an earlier regular file: %w", linkname, ErrUnsafeArchive)
			}
			linkTarget := filepath.Join(stage, filepath.FromSlash(linkname))
			info, err := os.Lstat(linkTarget)
			if err != nil || !info.Mode().IsRegular() {
				return ErrUnsafeArchive
			}
			if err := os.Link(linkTarget, target); err != nil {
				return err
			}
			seen[name] = tar.TypeLink
		default:
			return fmt.Errorf("unsupported tar type %d for %q: %w", header.Typeflag, name, ErrUnsafeArchive)
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		if err := applyMetadata(directories[index].path, &directories[index].header); err != nil {
			return err
		}
	}
	if err := os.Remove(marker); err != nil {
		return err
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

type directoryMetadata struct {
	path   string
	header tar.Header
}

func inventory(ctx context.Context, root string) ([]sourceEntry, error) {
	var entries []sourceEntry
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == filepath.Join(".camp", "build") || relative == filepath.Join(".camp", "runtime") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		inspected, err := inspectSourceEntry(path, relative)
		if err != nil {
			return err
		}
		if inspected.mode&os.ModeSymlink != 0 {
			if err := validateSourceLink(root, relative, inspected.linkTarget); err != nil {
				return err
			}
		} else if !inspected.mode.IsRegular() && !inspected.mode.IsDir() {
			return fmt.Errorf("special file %q: %w", relative, ErrUnsafeArchive)
		}
		entries = append(entries, inspected)
		return nil
	})
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	return entries, err
}

func inspectSourceEntry(path, name string) (sourceEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return sourceEntry{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return sourceEntry{}, errors.New("source identity unavailable")
	}
	entry := sourceEntry{path: path, name: filepath.ToSlash(name), mode: info.Mode(), size: info.Size(), mtime: info.ModTime().UnixNano(), device: uint64(stat.Dev), inode: stat.Ino, links: uint64(stat.Nlink)}
	if info.Mode()&os.ModeSymlink != 0 {
		entry.linkTarget, err = os.Readlink(path)
		if err != nil {
			return sourceEntry{}, err
		}
	}
	if info.Mode().IsRegular() || info.Mode().IsDir() {
		entry.xattrs, err = readUserXattrs(path)
	}
	return entry, err
}

func sameEntry(left, right sourceEntry) bool {
	return left.name == right.name && left.mode == right.mode && left.size == right.size && left.mtime == right.mtime && left.device == right.device && left.inode == right.inode && left.links == right.links && left.linkTarget == right.linkTarget && equalXattrs(left.xattrs, right.xattrs)
}

func sameInventory(left, right []sourceEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameEntry(left[index], right[index]) {
			return false
		}
	}
	return true
}

func canonicalRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("archive root must be a real directory")
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || canonical != absolute {
		return "", errors.New("archive root has unexplained symlinks")
	}
	return canonical, nil
}

func safeArchiveName(name string) (string, error) {
	if name == "" || name == "." || len(name) > 4096 || strings.ContainsAny(name, "\\\x00") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", ErrUnsafeArchive
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if clean != name || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrUnsafeArchive
	}
	return clean, nil
}

func validateSourceLink(root, name, target string) error {
	if target == "" || filepath.IsAbs(target) || strings.ContainsAny(target, "\\\x00") {
		return ErrUnsafeArchive
	}
	resolved := filepath.Clean(filepath.Join(root, filepath.Dir(filepath.FromSlash(name)), target))
	if !within(root, resolved) {
		return ErrUnsafeArchive
	}
	return nil
}

func validateLink(stage, name, target string) error {
	if target == "" || filepath.IsAbs(target) || strings.ContainsAny(target, "\\\x00") {
		return ErrUnsafeArchive
	}
	resolved := filepath.Clean(filepath.Join(stage, filepath.Dir(filepath.FromSlash(name)), target))
	if !within(stage, resolved) {
		return ErrUnsafeArchive
	}
	return nil
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func ensureSafeParents(root, parent string) error {
	relative, err := filepath.Rel(root, parent)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafeArchive
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsafeArchive
		}
	}
	return nil
}

func openNewRegular(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func applyMetadata(path string, header *tar.Header) error {
	if err := os.Chmod(path, os.FileMode(header.Mode)&os.ModePerm); err != nil {
		return err
	}
	if !header.ModTime.IsZero() {
		if err := os.Chtimes(path, header.ModTime, header.ModTime); err != nil {
			return err
		}
	}
	for key, value := range paxXattrs(header.PAXRecords) {
		if err := syscall.Setxattr(path, key, value, 0); err != nil {
			return err
		}
	}
	return nil
}

func readUserXattrs(path string) (map[string][]byte, error) {
	size, err := syscall.Listxattr(path, nil)
	if err != nil {
		if errors.Is(err, syscall.ENOTSUP) {
			return nil, nil
		}
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	buffer := make([]byte, size)
	read, err := syscall.Listxattr(path, buffer)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte)
	for _, key := range strings.Split(strings.TrimRight(string(buffer[:read]), "\x00"), "\x00") {
		if !strings.HasPrefix(key, "user.") {
			continue
		}
		valueSize, err := syscall.Getxattr(path, key, nil)
		if err != nil {
			return nil, err
		}
		value := make([]byte, valueSize)
		count, err := syscall.Getxattr(path, key, value)
		if err != nil {
			return nil, err
		}
		result[key] = value[:count]
	}
	return result, nil
}

func xattrPAX(xattrs map[string][]byte) map[string]string {
	if len(xattrs) == 0 {
		return nil
	}
	result := make(map[string]string, len(xattrs))
	for key, value := range xattrs {
		result["SCHILY.xattr."+key] = string(value)
	}
	return result
}

func paxXattrs(records map[string]string) map[string][]byte {
	result := make(map[string][]byte)
	for key, value := range records {
		if strings.HasPrefix(key, "SCHILY.xattr.user.") {
			result[strings.TrimPrefix(key, "SCHILY.xattr.")] = []byte(value)
		}
	}
	return result
}

func equalXattrs(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || string(value) != string(other) {
			return false
		}
	}
	return true
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

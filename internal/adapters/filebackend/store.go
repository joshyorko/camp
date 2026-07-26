package filebackend

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/joshyorko/camp/internal/ports"
)

const (
	lockDirectory    = ".camp-locks"
	tempPrefix       = ".camp-tmp-"
	revisionXattr    = "user.camp.revision"
	revisionSize     = 32
	listPageSize     = 100
	pageTokenVersion = 1
)

type Store struct {
	root string
	dev  uint64
	ino  uint64
}

type pageToken struct {
	Version int    `json:"version"`
	Prefix  string `json:"prefix"`
	Cursor  string `json:"cursor"`
}

func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("file backend root: %w", ports.ErrUnsafePath)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("absolute file backend root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create file backend root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve file backend root: %w", err)
	}
	fd, err := syscall.Open(resolved, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open file backend root: %w", err)
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("stat file backend root: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return nil, fmt.Errorf("file backend root is not a directory: %w", ports.ErrUnsafePath)
	}
	store := &Store{root: resolved, dev: uint64(stat.Dev), ino: stat.Ino}
	lockFD, err := ensureDirectoryAt(fd, lockDirectory)
	if err != nil {
		return nil, fmt.Errorf("prepare lock directory: %w", err)
	}
	if err := syscall.Close(lockFD); err != nil {
		return nil, fmt.Errorf("close lock directory: %w", err)
	}
	return store, nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, ports.ObjectMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, ports.ObjectMeta{}, err
	}
	if _, err := splitKey(key); err != nil {
		return nil, ports.ObjectMeta{}, err
	}
	parentFD, name, err := s.openParent(key, false)
	if err != nil {
		return nil, ports.ObjectMeta{}, err
	}
	defer syscall.Close(parentFD)
	file, err := openRegularAt(parentFD, name, key)
	if err != nil {
		return nil, ports.ObjectMeta{}, err
	}
	meta, err := metadataFromFile(file, key)
	if err != nil {
		file.Close()
		return nil, ports.ObjectMeta{}, err
	}
	return file, meta, nil
}

func (s *Store) SourceIdentity(key string) (ports.ObjectSourceFingerprint, error) {
	parentFD, name, err := s.openParent(key, false)
	if err != nil {
		return ports.ObjectSourceFingerprint{}, err
	}
	defer syscall.Close(parentFD)
	file, err := openRegularAt(parentFD, name, key)
	if err != nil {
		return ports.ObjectSourceFingerprint{}, err
	}
	defer file.Close()
	meta, err := metadataFromFile(file, key)
	if err != nil {
		return ports.ObjectSourceFingerprint{}, err
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return ports.ObjectSourceFingerprint{}, fmt.Errorf("stat source identity %q: %w", key, err)
	}
	return ports.ObjectSourceFingerprint{
		Kind:          "file",
		Key:           key,
		Revision:      meta.Revision,
		Size:         meta.Size,
		SHA256:       meta.SHA256,
		CanonicalPath: filepath.Join(s.root, filepath.FromSlash(key)),
		Device:       stat.Dev,
		Inode:        stat.Ino,
	}, nil
}

func (s *Store) Head(ctx context.Context, key string) (ports.ObjectMeta, error) {
	file, meta, err := s.Get(ctx, key)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	if err := file.Close(); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("close object %q: %w", key, err)
	}
	return meta, nil
}

func (s *Store) PutImmutable(ctx context.Context, key string, source ports.RestartableSource, expectedSHA256 string, expectedSize int64) (ports.ObjectMeta, error) {
	if _, err := splitKey(key); err != nil {
		return ports.ObjectMeta{}, err
	}
	if source == nil || expectedSize < 0 || !validSHA256(expectedSHA256) {
		return ports.ObjectMeta{}, fmt.Errorf("immutable object %q expectations: %w", key, ports.ErrIntegrity)
	}
	lock, err := s.lockKey(ctx, key)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer lock.release()

	parentFD, name, err := s.openParent(key, true)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer syscall.Close(parentFD)
	if err := cleanupTemporaryFiles(parentFD, key); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("clean temporary files for %q: %w", key, err)
	}

	temp, tempName, err := createTemporaryFile(parentFD, key)
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("create temporary object %q: %w", key, err)
	}
	tempPresent := true
	defer func() {
		temp.Close()
		if tempPresent {
			_ = syscall.Unlinkat(parentFD, tempName)
		}
	}()

	reader, err := source.Open()
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("open immutable source for %q: %w", key, err)
	}
	if reader == nil {
		return ports.ObjectMeta{}, fmt.Errorf("open immutable source for %q: nil reader: %w", key, ports.ErrIntegrity)
	}
	size, sum, copyErr := copyAndSync(ctx, temp, reader)
	closeSourceErr := reader.Close()
	if copyErr != nil {
		return ports.ObjectMeta{}, fmt.Errorf("write temporary object %q: %w", key, copyErr)
	}
	if closeSourceErr != nil {
		return ports.ObjectMeta{}, fmt.Errorf("close immutable source for %q: %w", key, closeSourceErr)
	}
	if err := temp.Close(); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("close temporary object %q: %w", key, err)
	}
	if size != expectedSize || sum != expectedSHA256 {
		return ports.ObjectMeta{}, fmt.Errorf("immutable source for %q has size %d and sha256 %s: %w", key, size, sum, ports.ErrIntegrity)
	}

	current, err := metadataAt(parentFD, name, key)
	switch {
	case err == nil:
		if current.Size == expectedSize && current.SHA256 == expectedSHA256 {
			if err := syscall.Unlinkat(parentFD, tempName); err != nil {
				return ports.ObjectMeta{}, fmt.Errorf("remove redundant temporary object %q: %w", key, err)
			}
			tempPresent = false
			return current, nil
		}
		return ports.ObjectMeta{}, fmt.Errorf("immutable object %q contains different bytes: %w", key, ports.ErrConflict)
	case !errors.Is(err, ports.ErrNotFound):
		return ports.ObjectMeta{}, err
	}

	if err := ctx.Err(); err != nil {
		return ports.ObjectMeta{}, err
	}
	if err := syscall.Renameat(parentFD, tempName, parentFD, name); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("publish immutable object %q: %w", key, err)
	}
	tempPresent = false
	if err := syscall.Fsync(parentFD); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("sync parent after publishing immutable object %q: %v: %w", key, err, ports.ErrAmbiguous)
	}
	meta, err := metadataAt(parentFD, name, key)
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("read published immutable object %q: %v: %w", key, err, ports.ErrAmbiguous)
	}
	return meta, nil
}

func (s *Store) PutConditional(ctx context.Context, key string, body []byte, condition ports.WriteCondition) (ports.ObjectMeta, error) {
	if _, err := splitKey(key); err != nil {
		return ports.ObjectMeta{}, err
	}
	if condition.MustBeAbsent == (condition.MatchRevision != "") {
		return ports.ObjectMeta{}, fmt.Errorf("conditional write for %q: %w", key, ports.ErrInvalidCondition)
	}
	lock, err := s.lockKey(ctx, key)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer lock.release()

	parentFD, name, err := s.openParent(key, true)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer syscall.Close(parentFD)
	if err := cleanupTemporaryFiles(parentFD, key); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("clean temporary files for %q: %w", key, err)
	}
	current, currentErr := metadataAt(parentFD, name, key)
	if currentErr != nil && !errors.Is(currentErr, ports.ErrNotFound) {
		return ports.ObjectMeta{}, currentErr
	}
	if condition.MustBeAbsent && currentErr == nil {
		return ports.ObjectMeta{}, fmt.Errorf("create object %q: %w", key, ports.ErrConflict)
	}
	if condition.MatchRevision != "" {
		if currentErr != nil || current.Revision != condition.MatchRevision {
			return ports.ObjectMeta{}, fmt.Errorf("replace object %q at revision %q: %w", key, condition.MatchRevision, ports.ErrConflict)
		}
	}

	temp, tempName, err := createTemporaryFile(parentFD, key)
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("create temporary object %q: %w", key, err)
	}
	tempPresent := true
	defer func() {
		temp.Close()
		if tempPresent {
			_ = syscall.Unlinkat(parentFD, tempName)
		}
	}()
	if _, _, err := copyAndSync(ctx, temp, bytes.NewReader(body)); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("write temporary object %q: %w", key, err)
	}
	if err := temp.Close(); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("close temporary object %q: %w", key, err)
	}
	if err := ctx.Err(); err != nil {
		return ports.ObjectMeta{}, err
	}
	if err := syscall.Renameat(parentFD, tempName, parentFD, name); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("publish conditional object %q: %w", key, err)
	}
	tempPresent = false
	if err := syscall.Fsync(parentFD); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("sync parent after publishing conditional object %q: %v: %w", key, err, ports.ErrAmbiguous)
	}
	meta, err := metadataAt(parentFD, name, key)
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("read published conditional object %q: %v: %w", key, err, ports.ErrAmbiguous)
	}
	return meta, nil
}

func (s *Store) DeleteConditional(ctx context.Context, key string, expected ports.Revision) error {
	if _, err := splitKey(key); err != nil {
		return err
	}
	if expected == "" {
		return fmt.Errorf("delete object %q: %w", key, ports.ErrInvalidCondition)
	}
	lock, err := s.lockKey(ctx, key)
	if err != nil {
		return err
	}
	defer lock.release()
	parentFD, name, err := s.openParent(key, false)
	if err != nil {
		return err
	}
	defer syscall.Close(parentFD)
	current, err := metadataAt(parentFD, name, key)
	if err != nil {
		return err
	}
	if current.Revision != expected {
		return fmt.Errorf("delete object %q at revision %q: %w", key, expected, ports.ErrConflict)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := syscall.Unlinkat(parentFD, name); err != nil {
		return classifyPathError("delete", key, err)
	}
	if err := syscall.Fsync(parentFD); err != nil {
		return fmt.Errorf("sync parent after deleting object %q: %v: %w", key, err, ports.ErrAmbiguous)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix, pageToken string) ([]ports.ObjectMeta, string, error) {
	if err := validatePrefix(prefix); err != nil {
		return nil, "", err
	}
	cursor, err := decodePageToken(pageToken, prefix)
	if err != nil {
		return nil, "", err
	}
	rootFD, err := s.openRoot()
	if err != nil {
		return nil, "", err
	}
	defer syscall.Close(rootFD)
	var all []ports.ObjectMeta
	if err := walkDirectory(ctx, rootFD, "", true, &all); err != nil {
		return nil, "", err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	start := sort.Search(len(all), func(i int) bool { return all[i].Key > cursor })
	filtered := make([]ports.ObjectMeta, 0, listPageSize)
	for i := start; i < len(all) && len(filtered) < listPageSize; i++ {
		if strings.HasPrefix(all[i].Key, prefix) {
			filtered = append(filtered, all[i])
		}
	}
	if len(filtered) == 0 {
		return filtered, "", nil
	}
	last := filtered[len(filtered)-1].Key
	more := false
	for i := sort.Search(len(all), func(i int) bool { return all[i].Key > last }); i < len(all); i++ {
		if strings.HasPrefix(all[i].Key, prefix) {
			more = true
			break
		}
	}
	if !more {
		return filtered, "", nil
	}
	next, err := encodePageToken(prefix, last)
	if err != nil {
		return nil, "", err
	}
	return filtered, next, nil
}

type keyLock struct {
	fd int
}

func (s *Store) lockKey(ctx context.Context, key string) (*keyLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rootFD, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer syscall.Close(rootFD)
	lockDirFD, err := ensureDirectoryAt(rootFD, lockDirectory)
	if err != nil {
		return nil, fmt.Errorf("open lock directory: %w", err)
	}
	defer syscall.Close(lockDirFD)
	name := sha256Hex([]byte(key)) + ".lock"
	fd, err := syscall.Openat(lockDirFD, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, classifyPathError("open lock", key, err)
	}
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &keyLock{fd: fd}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			syscall.Close(fd)
			return nil, fmt.Errorf("lock object %q: %w", key, err)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			syscall.Close(fd)
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *keyLock) release() {
	_ = syscall.Flock(l.fd, syscall.LOCK_UN)
	_ = syscall.Close(l.fd)
}

func (s *Store) openRoot() (int, error) {
	fd, err := syscall.Open(s.root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open file backend root: %w", ports.ErrUnsafePath)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		syscall.Close(fd)
		return -1, fmt.Errorf("stat file backend root: %w", err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFDIR || uint64(stat.Dev) != s.dev || stat.Ino != s.ino {
		syscall.Close(fd)
		return -1, fmt.Errorf("file backend root identity changed: %w", ports.ErrUnsafePath)
	}
	return fd, nil
}

func (s *Store) openParent(key string, create bool) (int, string, error) {
	parts, err := splitKey(key)
	if err != nil {
		return -1, "", err
	}
	fd, err := s.openRoot()
	if err != nil {
		return -1, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		next, openErr := openDirectoryAt(fd, part)
		if openErr != nil && create && errors.Is(openErr, syscall.ENOENT) {
			next, openErr = ensureDirectoryAt(fd, part)
		}
		if openErr != nil {
			syscall.Close(fd)
			if errors.Is(openErr, syscall.ENOENT) {
				return -1, "", fmt.Errorf("object parent for %q: %w", key, ports.ErrNotFound)
			}
			return -1, "", classifyPathError("open parent", key, openErr)
		}
		syscall.Close(fd)
		fd = next
	}
	return fd, parts[len(parts)-1], nil
}

func ensureDirectoryAt(parentFD int, name string) (int, error) {
	fd, err := openDirectoryAt(parentFD, name)
	if err == nil {
		return fd, nil
	}
	if !errors.Is(err, syscall.ENOENT) {
		return -1, err
	}
	if err := syscall.Mkdirat(parentFD, name, 0o700); err != nil && !errors.Is(err, syscall.EEXIST) {
		return -1, err
	}
	if err := syscall.Fsync(parentFD); err != nil {
		return -1, err
	}
	return openDirectoryAt(parentFD, name)
}

func openDirectoryAt(parentFD int, name string) (int, error) {
	return syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
}

func openRegularAt(parentFD int, name, key string) (*os.File, error) {
	fd, err := syscall.Openat(parentFD, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, classifyPathError("open", key, err)
	}
	file := os.NewFile(uintptr(fd), key)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, fmt.Errorf("stat object %q: %w", key, err)
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		file.Close()
		return nil, fmt.Errorf("object %q is not a regular file: %w", key, ports.ErrUnsafePath)
	}
	return file, nil
}

func metadataAt(parentFD int, name, key string) (ports.ObjectMeta, error) {
	file, err := openRegularAt(parentFD, name, key)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer file.Close()
	return metadataFromFile(file, key)
}

func metadataFromFile(file *os.File, key string) (ports.ObjectMeta, error) {
	var stat syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &stat); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("stat object %q: %w", key, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("seek object %q: %w", key, err)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("hash object %q: %w", key, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("rewind object %q: %w", key, err)
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	revision, err := persistedRevision(file)
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("read persisted revision for object %q: %v: %w", key, err, ports.ErrIntegrity)
	}
	return ports.ObjectMeta{
		Key:      key,
		Size:     stat.Size,
		Revision: revision,
		SHA256:   sum,
		Modified: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC(),
	}, nil
}

func createTemporaryFile(parentFD int, key string) (*os.File, string, error) {
	prefix := tempNamePrefix(key)
	for i := 0; i < 100; i++ {
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			return nil, "", err
		}
		name := prefix + hex.EncodeToString(random)
		fd, err := syscall.Openat(parentFD, name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			file := os.NewFile(uintptr(fd), name)
			if err := assignRevision(file); err != nil {
				file.Close()
				_ = syscall.Unlinkat(parentFD, name)
				return nil, "", fmt.Errorf("persist unique object revision: %w", err)
			}
			return file, name, nil
		}
		if !errors.Is(err, syscall.EEXIST) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("could not allocate a unique temporary file")
}

func assignRevision(file *os.File) error {
	revision := make([]byte, revisionSize)
	if _, err := rand.Read(revision); err != nil {
		return err
	}
	name, err := syscall.BytePtrFromString(revisionXattr)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall6(
		syscall.SYS_FSETXATTR,
		file.Fd(),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&revision[0])),
		uintptr(len(revision)),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func persistedRevision(file *os.File) (ports.Revision, error) {
	value := make([]byte, revisionSize)
	name, err := syscall.BytePtrFromString(revisionXattr)
	if err != nil {
		return "", err
	}
	size, _, errno := syscall.Syscall6(
		syscall.SYS_FGETXATTR,
		file.Fd(),
		uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(&value[0])),
		uintptr(len(value)),
		0,
		0,
	)
	if errno != 0 {
		return "", errno
	}
	if size != revisionSize {
		return "", fmt.Errorf("revision attribute has %d bytes, want %d", size, revisionSize)
	}
	return ports.Revision(hex.EncodeToString(value)), nil
}

func cleanupTemporaryFiles(parentFD int, key string) error {
	dup, err := syscall.Dup(parentFD)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(dup), "temporary-object-directory")
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	prefix := tempNamePrefix(key)
	removed := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			if err := syscall.Unlinkat(parentFD, entry.Name()); err != nil && !errors.Is(err, syscall.ENOENT) {
				return err
			}
			removed = true
		}
	}
	if removed {
		return syscall.Fsync(parentFD)
	}
	return nil
}

func tempNamePrefix(key string) string {
	return tempPrefix + sha256Hex([]byte(key)) + "-"
}

func copyAndSync(ctx context.Context, file *os.File, reader io.Reader) (int64, string, error) {
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(file, hash), contextReader{ctx: ctx, reader: reader})
	if err != nil {
		return written, "", err
	}
	if err := file.Sync(); err != nil {
		return written, "", err
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func walkDirectory(ctx context.Context, directoryFD int, prefix string, root bool, out *[]ports.ObjectMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dup, err := syscall.Dup(directoryFD)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(dup), prefix)
	entries, readErr := dir.ReadDir(-1)
	closeErr := dir.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if root && name == lockDirectory {
			continue
		}
		if strings.HasPrefix(name, tempPrefix) {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "/" + name
		}
		if _, err := splitKey(key); err != nil {
			return fmt.Errorf("unsafe entry %q in object root: %w", key, ports.ErrUnsafePath)
		}
		fd, err := syscall.Openat(directoryFD, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return classifyPathError("list", key, err)
		}
		var stat syscall.Stat_t
		if err := syscall.Fstat(fd, &stat); err != nil {
			syscall.Close(fd)
			return err
		}
		switch stat.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			err = walkDirectory(ctx, fd, key, false, out)
			syscall.Close(fd)
			if err != nil {
				return err
			}
		case syscall.S_IFREG:
			file := os.NewFile(uintptr(fd), key)
			meta, metaErr := metadataFromFile(file, key)
			closeErr := file.Close()
			if metaErr != nil {
				return metaErr
			}
			if closeErr != nil {
				return closeErr
			}
			*out = append(*out, meta)
		default:
			syscall.Close(fd)
			return fmt.Errorf("object entry %q is not a regular file or directory: %w", key, ports.ErrUnsafePath)
		}
	}
	return nil
}

func splitKey(key string) ([]string, error) {
	if key == "" || filepath.IsAbs(key) || strings.ContainsRune(key, '\x00') || strings.Contains(key, `\`) {
		return nil, fmt.Errorf("object key %q: %w", key, ports.ErrInvalidKey)
	}
	parts := strings.Split(key, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || part == lockDirectory || strings.HasPrefix(part, tempPrefix) {
			return nil, fmt.Errorf("object key %q: %w", key, ports.ErrInvalidKey)
		}
	}
	return parts, nil
}

func validatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	trimmed := strings.TrimSuffix(prefix, "/")
	if trimmed == "" {
		return fmt.Errorf("object prefix %q: %w", prefix, ports.ErrInvalidKey)
	}
	_, err := splitKey(trimmed)
	return err
}

func decodePageToken(token, prefix string) (string, error) {
	if token == "" {
		return "", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", fmt.Errorf("decode page token: %w", ports.ErrInvalidPageToken)
	}
	var payload pageToken
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return "", fmt.Errorf("decode page token document: %w", ports.ErrInvalidPageToken)
	}
	if payload.Version != pageTokenVersion || payload.Prefix != prefix || payload.Cursor == "" {
		return "", fmt.Errorf("page token does not belong to prefix %q: %w", prefix, ports.ErrInvalidPageToken)
	}
	if _, err := splitKey(payload.Cursor); err != nil || !strings.HasPrefix(payload.Cursor, prefix) {
		return "", fmt.Errorf("page token cursor does not belong to prefix %q: %w", prefix, ports.ErrInvalidPageToken)
	}
	return payload.Cursor, nil
}

func encodePageToken(prefix, cursor string) (string, error) {
	encoded, err := json.Marshal(pageToken{Version: pageTokenVersion, Prefix: prefix, Cursor: cursor})
	if err != nil {
		return "", fmt.Errorf("encode page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func classifyPathError(operation, key string, err error) error {
	switch {
	case errors.Is(err, syscall.ENOENT):
		return fmt.Errorf("%s object %q: %w", operation, key, ports.ErrNotFound)
	case errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%s object %q: %w", operation, key, ports.ErrUnsafePath)
	default:
		return fmt.Errorf("%s object %q: %w", operation, key, err)
	}
}

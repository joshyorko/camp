package capsule

import (
	"bytes"
	"context"
	"crypto/rand"
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

	"github.com/joshyorko/camp/internal/domain"
	"golang.org/x/sys/unix"
)

var ErrOwnershipMismatch = errors.New("materialization ownership mismatch")

type Ownership struct {
	materializationRoot string
	rootDevice          uint64
	rootInode           uint64
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

type ownershipMarkerOperations struct {
	write         func(*os.File, []byte) error
	sync          func(*os.File) error
	beforePublish func(int, string) error
	publish       func(int, int, string) error
	afterPublish  func()
}

type ownershipMarkerFileIdentity struct {
	device uint64
	inode  uint64
}

type ownershipRemovalOperations struct {
	sync              func(*os.File) error
	afterQuarantine   func()
	afterEntryRemoved func()
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
	_, _, device, inode, err := inspectRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("inspect materialization root: %w", err)
	}
	return &Ownership{materializationRoot: canonical, rootDevice: device, rootInode: inode}, nil
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
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != token {
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
	runtimeDirectory, err := openOwnershipMarkerDirectory(canonical, device, inode, true, (*os.File).Sync)
	if err != nil {
		return domain.Materialization{}, fmt.Errorf("create ownership marker directory: %w", err)
	}
	defer runtimeDirectory.Close()
	if err := reconcileOwnershipMarkerPublication(runtimeDirectory, marker); err != nil {
		return domain.Materialization{}, err
	}
	matching, err := ownershipMarkerMatches(runtimeDirectory, marker)
	if err != nil {
		return domain.Materialization{}, err
	}
	if matching {
		if err := runtimeDirectory.Sync(); err != nil {
			return domain.Materialization{}, fmt.Errorf("sync ownership marker: %w", err)
		}
		if err := verifyOwnershipMarkerDirectory(canonical, device, inode, runtimeDirectory); err != nil {
			return domain.Materialization{}, fmt.Errorf("revalidate ownership marker path: %w", err)
		}
		return materialization, nil
	}
	operations := ownershipMarkerOperations{
		write:   writeOwnershipMarker,
		sync:    (*os.File).Sync,
		publish: publishOwnershipMarkerNoReplace,
	}
	if err := createOwnershipMarker(runtimeDirectory, body, marker, operations); err != nil {
		return domain.Materialization{}, err
	}
	if err := verifyOwnershipMarkerDirectory(canonical, device, inode, runtimeDirectory); err != nil {
		return domain.Materialization{}, fmt.Errorf("revalidate ownership marker path: %w", err)
	}
	return materialization, nil
}

func ownershipMarkerMatches(directory *os.File, want ownershipMarker) (bool, error) {
	return ownershipMarkerNamedMatches(directory, "ownership.json", want, 1)
}

func ownershipMarkerNamedMatches(directory *os.File, name string, want ownershipMarker, expectedLinks uint64) (bool, error) {
	pathFD, err := unix.Openat(int(directory.Fd()), name, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open ownership marker: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	pathFile := os.NewFile(uintptr(pathFD), name)
	if pathFile == nil {
		_ = unix.Close(pathFD)
		return false, fmt.Errorf("open ownership marker: %w", ErrOwnershipMismatch)
	}
	defer pathFile.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(pathFD, &stat); err != nil {
		return false, fmt.Errorf("inspect ownership marker: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Nlink != expectedLinks {
		return false, fmt.Errorf("ownership marker is not a regular file: %w", ErrOwnershipMismatch)
	}
	readFD, err := unix.Open(fmt.Sprintf("/proc/self/fd/%d", pathFD), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, fmt.Errorf("read ownership marker: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	file := os.NewFile(uintptr(readFD), name)
	if file == nil {
		_ = unix.Close(readFD)
		return false, fmt.Errorf("read ownership marker: %w", ErrOwnershipMismatch)
	}
	var readStat unix.Stat_t
	if err := unix.Fstat(readFD, &readStat); err != nil || uint64(readStat.Dev) != uint64(stat.Dev) || readStat.Ino != stat.Ino {
		_ = file.Close()
		return false, fmt.Errorf("revalidate ownership marker fd: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	body, readErr := io.ReadAll(io.LimitReader(file, 4097))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) > 4096 {
		return false, fmt.Errorf("read ownership marker: %w", ErrOwnershipMismatch)
	}
	var namedStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &namedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || uint64(namedStat.Dev) != uint64(stat.Dev) || namedStat.Ino != stat.Ino || namedStat.Mode&unix.S_IFMT != unix.S_IFREG || namedStat.Mode&0o7777 != 0o600 || namedStat.Nlink != expectedLinks {
		return false, fmt.Errorf("revalidate ownership marker name: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	got, err := decodeOwnershipMarker(body)
	if err != nil || got != want {
		return false, ErrOwnershipMismatch
	}
	canonical, err := json.Marshal(got)
	if err != nil || !bytes.Equal(body, canonical) {
		return false, ErrOwnershipMismatch
	}
	return true, nil
}

func decodeOwnershipMarker(body []byte) (ownershipMarker, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return ownershipMarker{}, ErrOwnershipMismatch
	}
	seen := make(map[string]struct{}, 4)
	var marker ownershipMarker
	for decoder.More() {
		fieldToken, err := decoder.Token()
		field, ok := fieldToken.(string)
		if err != nil || !ok {
			return ownershipMarker{}, ErrOwnershipMismatch
		}
		if _, duplicate := seen[field]; duplicate {
			return ownershipMarker{}, ErrOwnershipMismatch
		}
		seen[field] = struct{}{}
		switch field {
		case "token":
			err = decoder.Decode(&marker.Token)
		case "canonicalPath":
			err = decoder.Decode(&marker.CanonicalPath)
		case "device":
			err = decoder.Decode(&marker.Device)
		case "inode":
			err = decoder.Decode(&marker.Inode)
		default:
			return ownershipMarker{}, ErrOwnershipMismatch
		}
		if err != nil {
			return ownershipMarker{}, ErrOwnershipMismatch
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(seen) != 4 {
		return ownershipMarker{}, ErrOwnershipMismatch
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return ownershipMarker{}, ErrOwnershipMismatch
	}
	return marker, nil
}

func reconcileOwnershipMarkerPublication(directory *os.File, marker ownershipMarker) error {
	temporaryName := ownershipMarkerTemporaryName(marker.Token)
	temporaryExists, err := entryExistsAt(int(directory.Fd()), temporaryName)
	if err != nil || !temporaryExists {
		return err
	}
	finalExists, err := entryExistsAt(int(directory.Fd()), "ownership.json")
	if err != nil {
		return err
	}
	expectedLinks := uint64(1)
	if finalExists {
		expectedLinks = 2
	}
	matching, err := ownershipMarkerNamedMatches(directory, temporaryName, marker, expectedLinks)
	if err != nil || !matching {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	var temporaryStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), temporaryName, &temporaryStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	temporaryIdentity := ownershipMarkerFileIdentity{device: uint64(temporaryStat.Dev), inode: temporaryStat.Ino}
	if finalExists {
		matching, err := ownershipMarkerNamedMatches(directory, "ownership.json", marker, 2)
		if err != nil || !matching {
			return errors.Join(err, ErrOwnershipMismatch)
		}
		var finalStat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), "ownership.json", &finalStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || uint64(finalStat.Dev) != temporaryIdentity.device || finalStat.Ino != temporaryIdentity.inode {
			return errors.Join(err, ErrOwnershipMismatch)
		}
	} else {
		pathFD, err := unix.Openat(int(directory.Fd()), temporaryName, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return errors.Join(err, ErrOwnershipMismatch)
		}
		fileFD, openErr := unix.Open(fmt.Sprintf("/proc/self/fd/%d", pathFD), unix.O_RDWR|unix.O_CLOEXEC, 0)
		_ = unix.Close(pathFD)
		if openErr != nil {
			return errors.Join(openErr, ErrOwnershipMismatch)
		}
		publishErr := publishOwnershipMarkerNoReplace(int(directory.Fd()), fileFD, "ownership.json")
		_ = unix.Close(fileFD)
		if publishErr != nil {
			return fmt.Errorf("resume ownership marker publication: %w", publishErr)
		}
	}
	if err := unlinkOwnershipMarkerTemporary(int(directory.Fd()), temporaryName, temporaryIdentity); err != nil {
		return fmt.Errorf("reconcile ownership marker temporary file: %w", err)
	}
	matching, err = ownershipMarkerMatches(directory, marker)
	if err != nil || !matching {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync reconciled ownership marker: %w", err)
	}
	return nil
}

func createOwnershipMarker(directory *os.File, body []byte, marker ownershipMarker, operations ownershipMarkerOperations) error {
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
	temporaryName := ""
	if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EISDIR) {
		temporaryName = ownershipMarkerTemporaryName(marker.Token)
		fd, err = unix.Openat(int(directory.Fd()), temporaryName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	}
	if err != nil {
		return fmt.Errorf("create ownership marker temporary file: %w", err)
	}
	temporary := os.NewFile(uintptr(fd), "ownership marker temporary file")
	if temporary == nil {
		_ = unix.Close(fd)
		return errors.New("create ownership marker temporary file")
	}
	defer temporary.Close()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set ownership marker mode: %w", err)
	}
	var temporaryStat unix.Stat_t
	if err := unix.Fstat(fd, &temporaryStat); err != nil {
		return fmt.Errorf("inspect ownership marker temporary file: %w", err)
	}
	temporaryIdentity := ownershipMarkerFileIdentity{device: uint64(temporaryStat.Dev), inode: temporaryStat.Ino}
	if temporaryName != "" {
		defer unlinkOwnershipMarkerTemporary(int(directory.Fd()), temporaryName, temporaryIdentity)
	}

	if err := operations.write(temporary, body); err != nil {
		return fmt.Errorf("write ownership marker: %w", err)
	}
	if err := operations.sync(temporary); err != nil {
		return fmt.Errorf("sync ownership marker file: %w", err)
	}
	if operations.beforePublish != nil {
		if err := operations.beforePublish(int(directory.Fd()), temporaryName); err != nil {
			return fmt.Errorf("prepare ownership marker publication: %w", err)
		}
	}
	if err := operations.publish(int(directory.Fd()), fd, "ownership.json"); err != nil {
		if errors.Is(err, os.ErrExist) {
			matching, matchErr := ownershipMarkerMatches(directory, marker)
			if matchErr != nil {
				return matchErr
			}
			if matching {
				if temporaryName != "" {
					if err := unlinkOwnershipMarkerTemporary(int(directory.Fd()), temporaryName, temporaryIdentity); err != nil {
						return fmt.Errorf("remove ownership marker temporary file: %w", err)
					}
				}
				if err := directory.Sync(); err != nil {
					return fmt.Errorf("sync ownership marker: %w", err)
				}
				return nil
			}
		}
		return fmt.Errorf("publish ownership marker: %w", err)
	}
	if operations.afterPublish != nil {
		operations.afterPublish()
	}
	if temporaryName != "" {
		if err := unlinkOwnershipMarkerTemporary(int(directory.Fd()), temporaryName, temporaryIdentity); err != nil {
			return fmt.Errorf("remove ownership marker temporary file: %w", err)
		}
	}
	var finalStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), "ownership.json", &finalStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || uint64(finalStat.Dev) != temporaryIdentity.device || finalStat.Ino != temporaryIdentity.inode || finalStat.Mode&unix.S_IFMT != unix.S_IFREG || finalStat.Mode&0o7777 != 0o600 || finalStat.Nlink != 1 {
		return fmt.Errorf("revalidate published ownership marker: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	matching, err := ownershipMarkerMatches(directory, marker)
	if err != nil || !matching {
		return fmt.Errorf("revalidate published ownership marker: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync ownership marker: %w", err)
	}
	return nil
}

func writeOwnershipMarker(file *os.File, body []byte) error {
	written, err := file.Write(body)
	if err == nil && written != len(body) {
		return io.ErrShortWrite
	}
	return err
}

func ownershipMarkerTemporaryName(token string) string {
	return ".ownership-" + token + ".tmp"
}

func publishOwnershipMarkerNoReplace(directoryFD, temporaryFD int, newName string) error {
	return unix.Linkat(unix.AT_FDCWD, fmt.Sprintf("/proc/self/fd/%d", temporaryFD), directoryFD, newName, unix.AT_SYMLINK_FOLLOW)
}

func unlinkOwnershipMarkerTemporary(directoryFD int, name string, want ownershipMarkerFileIdentity) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || uint64(stat.Dev) != want.device || stat.Ino != want.inode {
		return ErrOwnershipMismatch
	}
	return unix.Unlinkat(directoryFD, name, 0)
}

func openOwnershipMarkerDirectory(root string, device, inode uint64, create bool, syncDirectory func(*os.File) error) (*os.File, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), root)
	if current == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open ownership root")
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(fd, &rootStat); err != nil || uint64(rootStat.Dev) != device || rootStat.Ino != inode {
		_ = current.Close()
		return nil, errors.Join(err, ErrOwnershipMismatch)
	}
	return walkOwnershipMarkerDirectory(current, create, syncDirectory)
}

func walkOwnershipMarkerDirectory(current *os.File, create bool, syncDirectory func(*os.File) error) (*os.File, error) {
	for _, component := range []string{".camp", "runtime"} {
		childFD, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, os.ErrNotExist) && create {
			if err := unix.Mkdirat(int(current.Fd()), component, 0o700); err != nil {
				_ = current.Close()
				return nil, err
			}
			childFD, openErr = unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			_ = current.Close()
			return nil, errors.Join(openErr, ErrOwnershipMismatch)
		}
		childPath := filepath.Join(current.Name(), component)
		child := os.NewFile(uintptr(childFD), childPath)
		if child == nil {
			_ = unix.Close(childFD)
			_ = current.Close()
			return nil, ErrOwnershipMismatch
		}
		if create {
			if err := syncDirectory(current); err != nil {
				_ = child.Close()
				_ = current.Close()
				return nil, err
			}
		}
		if err := current.Close(); err != nil {
			_ = child.Close()
			return nil, err
		}
		current = child
	}
	return current, nil
}

func verifyOwnershipMarkerDirectory(root string, device, inode uint64, anchored *os.File) error {
	current, err := openOwnershipMarkerDirectory(root, device, inode, false, (*os.File).Sync)
	if err != nil {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	defer current.Close()
	var anchoredStat, currentStat unix.Stat_t
	if err := unix.Fstat(int(anchored.Fd()), &anchoredStat); err != nil {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	if err := unix.Fstat(int(current.Fd()), &currentStat); err != nil {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	if uint64(anchoredStat.Dev) != uint64(currentStat.Dev) || anchoredStat.Ino != currentStat.Ino {
		return ErrOwnershipMismatch
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
	runtimeDirectory, err := openOwnershipMarkerDirectory(canonical, device, inode, false, (*os.File).Sync)
	if err != nil {
		return "", fmt.Errorf("validate ownership marker directory: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	defer runtimeDirectory.Close()
	want := ownershipMarker{Token: materialization.OwnershipMarker, CanonicalPath: canonical, Device: device, Inode: inode}
	matching, err := ownershipMarkerMatches(runtimeDirectory, want)
	if err != nil || !matching {
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
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if canonical == "" {
		canonical = materialization.CanonicalPath
	}
	operations := ownershipRemovalOperations{sync: (*os.File).Sync}
	if err := o.quarantineAndRemoveOwned(ctx, materialization, operations); err != nil {
		return false, err
	}
	return true, nil
}

func (o *Ownership) quarantineAndRemoveOwned(ctx context.Context, materialization domain.Materialization, operations ownershipRemovalOperations) error {
	parent, base, err := o.openOwnedMaterializationParent(materialization.CanonicalPath)
	if err != nil {
		return fmt.Errorf("open owned materialization parent: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	defer parent.Close()
	quarantine := ".camp-remove-" + materialization.OwnershipMarker + ".quarantine"
	baseExists, err := entryExistsAt(int(parent.Fd()), base)
	if err != nil {
		return err
	}
	quarantineExists, err := entryExistsAt(int(parent.Fd()), quarantine)
	if err != nil {
		return err
	}
	newlyQuarantined := false
	if quarantineExists {
		if baseExists {
			return ErrOwnershipMismatch
		}
	} else {
		if !baseExists {
			return ErrOwnershipMismatch
		}
		if err := unix.Renameat2(int(parent.Fd()), base, int(parent.Fd()), quarantine, unix.RENAME_NOREPLACE); err != nil {
			return fmt.Errorf("quarantine owned materialization: %w", errors.Join(err, ErrOwnershipMismatch))
		}
		newlyQuarantined = true
		if err := operations.sync(parent); err != nil {
			restoreErr := o.restoreQuarantinedMaterialization(parent, quarantine, base, err, operations.sync)
			return fmt.Errorf("sync owned materialization quarantine: %w", errors.Join(err, restoreErr))
		}
	}

	quarantineFD, err := unix.Openat(int(parent.Fd()), quarantine, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return o.rejectQuarantinedMaterialization(parent, quarantine, base, err, newlyQuarantined, operations.sync)
	}
	quarantineDirectory := os.NewFile(uintptr(quarantineFD), quarantine)
	if quarantineDirectory == nil {
		_ = unix.Close(quarantineFD)
		return o.rejectQuarantinedMaterialization(parent, quarantine, base, ErrOwnershipMismatch, newlyQuarantined, operations.sync)
	}
	var quarantineStat unix.Stat_t
	if err := unix.Fstat(quarantineFD, &quarantineStat); err != nil || quarantineStat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(quarantineStat.Dev) != materialization.Device || quarantineStat.Ino != materialization.Inode {
		_ = quarantineDirectory.Close()
		return o.rejectQuarantinedMaterialization(parent, quarantine, base, errors.Join(err, ErrOwnershipMismatch), newlyQuarantined, operations.sync)
	}
	if err := validateQuarantinedOwnership(quarantineDirectory, materialization); err != nil {
		_ = quarantineDirectory.Close()
		return o.rejectQuarantinedMaterialization(parent, quarantine, base, err, newlyQuarantined, operations.sync)
	}
	if operations.afterQuarantine != nil {
		operations.afterQuarantine()
	}
	if err := removeDirectoryContentsWithHook(ctx, quarantineDirectory, uint64(quarantineStat.Dev), operations.afterEntryRemoved); err != nil {
		_ = quarantineDirectory.Close()
		return fmt.Errorf("remove quarantined materialization contents: %w", err)
	}
	if err := quarantineDirectory.Close(); err != nil {
		return fmt.Errorf("close quarantined materialization: %w", err)
	}
	var namedStat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), quarantine, &namedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || namedStat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(namedStat.Dev) != materialization.Device || namedStat.Ino != materialization.Inode {
		return fmt.Errorf("revalidate quarantined materialization: %w", errors.Join(err, ErrOwnershipMismatch))
	}
	if err := unix.Unlinkat(int(parent.Fd()), quarantine, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove quarantined materialization: %w", err)
	}
	if err := operations.sync(parent); err != nil {
		return fmt.Errorf("sync materialization removal: %w", err)
	}
	return nil
}

func (o *Ownership) rejectQuarantinedMaterialization(parent *os.File, quarantine, base string, cause error, restore bool, syncDirectory func(*os.File) error) error {
	if !restore {
		return errors.Join(cause, ErrOwnershipMismatch)
	}
	return o.restoreQuarantinedMaterialization(parent, quarantine, base, cause, syncDirectory)
}

func (o *Ownership) restoreQuarantinedMaterialization(parent *os.File, quarantine, base string, cause error, syncDirectory func(*os.File) error) error {
	if err := unix.Renameat2(int(parent.Fd()), quarantine, int(parent.Fd()), base, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("preserve mismatched quarantined materialization: %w", errors.Join(cause, err, ErrOwnershipMismatch))
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("sync restored materialization: %w", errors.Join(cause, err, ErrOwnershipMismatch))
	}
	return errors.Join(cause, ErrOwnershipMismatch)
}

func entryExistsAt(directoryFD int, name string) (bool, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

func validateQuarantinedOwnership(root *os.File, materialization domain.Materialization) error {
	duplicateFD, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	duplicate := os.NewFile(uintptr(duplicateFD), root.Name())
	if duplicate == nil {
		_ = unix.Close(duplicateFD)
		return ErrOwnershipMismatch
	}
	runtimeDirectory, err := walkOwnershipMarkerDirectory(duplicate, false, (*os.File).Sync)
	if err != nil {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	defer runtimeDirectory.Close()
	want := ownershipMarker{
		Token:         materialization.OwnershipMarker,
		CanonicalPath: materialization.CanonicalPath,
		Device:        materialization.Device,
		Inode:         materialization.Inode,
	}
	matching, err := ownershipMarkerMatches(runtimeDirectory, want)
	if err != nil || !matching {
		return errors.Join(err, ErrOwnershipMismatch)
	}
	return nil
}

func (o *Ownership) openOwnedMaterializationParent(canonical string) (*os.File, string, error) {
	relative, err := filepath.Rel(o.materializationRoot, canonical)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, "", ErrOwnershipMismatch
	}
	components := strings.Split(relative, string(filepath.Separator))
	base := components[len(components)-1]
	fd, err := unix.Open(o.materializationRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", err
	}
	current := os.NewFile(uintptr(fd), o.materializationRoot)
	if current == nil {
		_ = unix.Close(fd)
		return nil, "", ErrOwnershipMismatch
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(fd, &rootStat); err != nil || uint64(rootStat.Dev) != o.rootDevice || rootStat.Ino != o.rootInode {
		_ = current.Close()
		return nil, "", errors.Join(err, ErrOwnershipMismatch)
	}
	for _, component := range components[:len(components)-1] {
		childFD, err := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			_ = current.Close()
			return nil, "", err
		}
		child := os.NewFile(uintptr(childFD), filepath.Join(current.Name(), component))
		if child == nil {
			_ = unix.Close(childFD)
			_ = current.Close()
			return nil, "", ErrOwnershipMismatch
		}
		if err := current.Close(); err != nil {
			_ = child.Close()
			return nil, "", err
		}
		current = child
	}
	return current, base, nil
}

func removeDirectoryContents(ctx context.Context, directory *os.File, expectedDevice uint64) error {
	return removeDirectoryContentsWithHook(ctx, directory, expectedDevice, nil)
}

func removeDirectoryContentsWithHook(ctx context.Context, directory *os.File, expectedDevice uint64, afterRemove func()) error {
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.SliceStable(entries, func(left, right int) bool {
		return ownershipCleanupPriority(entries[left].Name()) < ownershipCleanupPriority(entries[right].Name())
	})
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		var namedStat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &namedStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if uint64(namedStat.Dev) != expectedDevice {
			return ErrOwnershipMismatch
		}
		if namedStat.Mode&unix.S_IFMT != unix.S_IFDIR {
			if err := unix.Unlinkat(int(directory.Fd()), name, 0); err != nil {
				return err
			}
			if afterRemove != nil {
				afterRemove()
			}
			continue
		}
		childFD, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		child := os.NewFile(uintptr(childFD), name)
		if child == nil {
			_ = unix.Close(childFD)
			return ErrOwnershipMismatch
		}
		var childStat unix.Stat_t
		if err := unix.Fstat(childFD, &childStat); err != nil || uint64(childStat.Dev) != uint64(namedStat.Dev) || childStat.Ino != namedStat.Ino {
			_ = child.Close()
			return errors.Join(err, ErrOwnershipMismatch)
		}
		if err := removeDirectoryContentsWithHook(ctx, child, expectedDevice, afterRemove); err != nil {
			_ = child.Close()
			return err
		}
		if err := child.Close(); err != nil {
			return err
		}
		var currentStat unix.Stat_t
		if err := unix.Fstatat(int(directory.Fd()), name, &currentStat, unix.AT_SYMLINK_NOFOLLOW); err != nil || currentStat.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(currentStat.Dev) != uint64(childStat.Dev) || currentStat.Ino != childStat.Ino {
			return errors.Join(err, ErrOwnershipMismatch)
		}
		if err := unix.Unlinkat(int(directory.Fd()), name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
		if afterRemove != nil {
			afterRemove()
		}
	}
	return nil
}

func ownershipCleanupPriority(name string) int {
	switch name {
	case "ownership.json":
		return 3
	case "runtime":
		return 2
	case ".camp":
		return 1
	default:
		return 0
	}
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

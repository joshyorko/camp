package remoteworker

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"

	"github.com/joshyorko/camp/internal/jsonstrict"
	"golang.org/x/sys/unix"
)

type serviceActorPublicationOps struct {
	fsync       func(int) error
	openTmpfile func(int) (int, error)
	linkDirect  func(int, int, string) error
	linkProc    func(int, int, string) error
	beforeLink  func(int, int, string) error
	afterLink   func(int, int, string) error
	write       func(*os.File, []byte) (int, error)
	chmod       func(*os.File, os.FileMode) error
	fileSync    func(*os.File) error
}

func defaultServiceActorPublicationOps() serviceActorPublicationOps {
	return serviceActorPublicationOps{
		fsync: unix.Fsync,
		openTmpfile: func(parentFD int) (int, error) {
			return unix.Openat(parentFD, ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0o600)
		},
		linkDirect: func(parentFD, stagingFD int, name string) error {
			return unix.Linkat(stagingFD, "", parentFD, name, unix.AT_EMPTY_PATH)
		},
		linkProc: func(parentFD, stagingFD int, name string) error {
			return unix.Linkat(
				unix.AT_FDCWD,
				fmt.Sprintf("/proc/self/fd/%d", stagingFD),
				parentFD,
				name,
				unix.AT_SYMLINK_FOLLOW,
			)
		},
		write:    func(file *os.File, body []byte) (int, error) { return file.Write(body) },
		chmod:    func(file *os.File, mode os.FileMode) error { return file.Chmod(mode) },
		fileSync: func(file *os.File) error { return file.Sync() },
	}
}

func publishServiceActorEvidence(path string, evidence ServiceActorEvidence) error {
	return publishServiceActorEvidenceWithOps(path, evidence, defaultServiceActorPublicationOps())
}

func publishServiceActorEvidenceWithOps(
	path string,
	evidence ServiceActorEvidence,
	ops serviceActorPublicationOps,
) (resultErr error) {
	if evidence.SchemaVersion != ProtocolSchemaVersion || evidence.SessionID == "" ||
		validateServiceActors(evidence.Worker, evidence.Supervisor) != nil {
		return ErrServiceEvidence
	}
	body, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > maxDiagnosticBytes {
		return ErrServiceEvidence
	}
	parentFD, name, err := openServiceActorParent(path)
	if err != nil {
		return actorEvidenceError(err)
	}
	defer unix.Close(parentFD)
	if existing, identity, observeErr := observeServiceActorAtIdentity(parentFD, name, nil); observeErr == nil {
		if !bytes.Equal(existing, body) {
			return actorEvidenceError(errors.New("existing actor evidence differs"))
		}
		return confirmExistingServiceActorEvidence(parentFD, name, body, identity, ops)
	} else if !errors.Is(observeErr, syscall.ENOENT) {
		return actorEvidenceError(observeErr)
	}

	fd, err := ops.openTmpfile(parentFD)
	if err != nil {
		return actorEvidenceError(err)
	}
	file := os.NewFile(uintptr(fd), "unnamed actor evidence staging")
	if file == nil {
		_ = unix.Close(fd)
		return actorEvidenceError(errors.New("open unnamed actor evidence staging"))
	}
	defer func() {
		if err := file.Close(); err != nil && resultErr == nil {
			resultErr = actorEvidenceError(err)
		}
	}()
	if err := ops.chmod(file, 0o600); err != nil {
		return actorEvidenceError(err)
	}
	if n, err := ops.write(file, body); err != nil {
		return actorEvidenceError(err)
	} else if n != len(body) {
		return actorEvidenceError(io.ErrShortWrite)
	}
	if err := ops.fileSync(file); err != nil {
		return actorEvidenceError(err)
	}
	var staged unix.Stat_t
	if err := unix.Fstat(fd, &staged); err != nil {
		return actorEvidenceError(err)
	}
	if !validUnnamedServiceActorStaging(staged, int64(len(body))) {
		return actorEvidenceError(errors.New("unnamed actor evidence staging has invalid shape"))
	}
	if ops.beforeLink != nil {
		if err := ops.beforeLink(parentFD, fd, name); err != nil {
			return actorEvidenceError(err)
		}
	}
	if err := publishServiceActorExactFD(parentFD, fd, name, ops); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			existing, identity, observeErr := observeServiceActorAtIdentity(parentFD, name, nil)
			if observeErr == nil && bytes.Equal(existing, body) {
				return confirmExistingServiceActorEvidence(parentFD, name, body, identity, ops)
			}
			return actorEvidenceError(errors.Join(err, observeErr))
		}
		return actorEvidenceError(err)
	}
	if ops.afterLink != nil {
		if err := ops.afterLink(parentFD, fd, name); err != nil {
			return actorEvidenceError(err)
		}
	}
	if err := validatePublishedServiceActorEvidence(parentFD, fd, name, body); err != nil {
		return actorEvidenceError(err)
	}
	if err := ops.fsync(parentFD); err != nil {
		return actorEvidenceError(err)
	}
	if err := validatePublishedServiceActorEvidence(parentFD, fd, name, body); err != nil {
		return actorEvidenceError(err)
	}
	return nil
}

func confirmExistingServiceActorEvidence(
	parentFD int,
	name string,
	body []byte,
	first unix.Stat_t,
	ops serviceActorPublicationOps,
) error {
	if !validExistingServiceActorEvidence(first, int64(len(body))) {
		return actorEvidenceError(errors.New("existing actor evidence is not an exact single-link file"))
	}
	if err := ops.fsync(parentFD); err != nil {
		return actorEvidenceError(err)
	}
	existing, second, err := observeServiceActorAtIdentity(parentFD, name, nil)
	if err != nil || !bytes.Equal(existing, body) ||
		!validExistingServiceActorEvidence(second, int64(len(body))) ||
		first.Dev != second.Dev || first.Ino != second.Ino {
		return actorEvidenceError(errors.Join(err, errors.New("existing actor evidence changed")))
	}
	return nil
}

func validExistingServiceActorEvidence(identity unix.Stat_t, size int64) bool {
	return identity.Mode&unix.S_IFMT == unix.S_IFREG &&
		identity.Mode&0o777 == 0o600 &&
		identity.Size == size &&
		size > 0 && size <= maxDiagnosticBytes &&
		identity.Nlink == 1
}

func publishServiceActorExactFD(
	parentFD int,
	stagingFD int,
	name string,
	ops serviceActorPublicationOps,
) error {
	err := ops.linkDirect(parentFD, stagingFD, name)
	if err == nil || errors.Is(err, syscall.EEXIST) {
		return err
	}
	if !exactFDLinkFallbackAllowed(err) {
		return err
	}
	return ops.linkProc(parentFD, stagingFD, name)
}

func exactFDLinkFallbackAllowed(err error) bool {
	return errors.Is(err, syscall.EPERM) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOENT) ||
		errors.Is(err, syscall.ENOSYS)
}

func validUnnamedServiceActorStaging(staged unix.Stat_t, size int64) bool {
	return staged.Mode&unix.S_IFMT == unix.S_IFREG &&
		staged.Mode&0o777 == 0o600 &&
		staged.Size == size &&
		size > 0 && size <= maxDiagnosticBytes &&
		staged.Nlink == 0
}

func validatePublishedServiceActorEvidence(parentFD, stagingFD int, name string, body []byte) error {
	var staged unix.Stat_t
	if err := unix.Fstat(stagingFD, &staged); err != nil {
		return err
	}
	observed, named, err := observeServiceActorAtIdentity(parentFD, name, nil)
	if err != nil {
		return err
	}
	if !bytes.Equal(observed, body) ||
		staged.Dev != named.Dev || staged.Ino != named.Ino ||
		staged.Mode&unix.S_IFMT != unix.S_IFREG || staged.Mode&0o777 != 0o600 ||
		staged.Size != int64(len(body)) || staged.Nlink != 1 ||
		named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o777 != 0o600 ||
		named.Size != int64(len(body)) || named.Nlink != 1 {
		return errors.New("published actor evidence differs from exact staging descriptor")
	}
	return nil
}

func observeServiceActorEvidence(path string, expected ServiceActorEvidence) error {
	body, err := observeServiceActorFile(path, nil)
	if err != nil {
		return actorEvidenceError(err)
	}
	if jsonstrict.RejectDuplicateKeys(body) != nil {
		return ErrServiceEvidence
	}
	var observed ServiceActorEvidence
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observed); err != nil {
		return ErrServiceEvidence
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrServiceEvidence
	}
	if !reflect.DeepEqual(observed, expected) || validateServiceActors(observed.Worker, observed.Supervisor) != nil {
		return ErrServiceEvidence
	}
	return nil
}

func observeServiceActorFile(path string, afterRead func()) ([]byte, error) {
	parentFD, name, err := openServiceActorParent(path)
	if err != nil {
		return nil, actorEvidenceError(err)
	}
	defer unix.Close(parentFD)
	body, err := observeServiceActorAt(parentFD, name, afterRead)
	if err != nil {
		return nil, actorEvidenceError(err)
	}
	return body, nil
}

func openServiceActorParent(path string) (int, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, "", errors.New("actor evidence path must be absolute and clean")
	}
	name := filepath.Base(path)
	if !safeSegment(name) {
		return -1, "", errors.New("actor evidence name is unsafe")
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, "", err
	}
	components := strings.Split(strings.TrimPrefix(filepath.Dir(path), "/"), "/")
	for _, component := range components {
		if component == "" {
			continue
		}
		if !safeSegment(component) {
			unix.Close(current)
			return -1, "", errors.New("actor evidence parent component is unsafe")
		}
		next, err := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		unix.Close(current)
		if err != nil {
			return -1, "", err
		}
		current = next
	}
	return current, name, nil
}

func observeServiceActorAt(parentFD int, name string, afterRead func()) ([]byte, error) {
	body, _, err := observeServiceActorAtIdentity(parentFD, name, afterRead)
	return body, err
}

func observeServiceActorAtIdentity(
	parentFD int,
	name string,
	afterRead func(),
) ([]byte, unix.Stat_t, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, unix.Stat_t{}, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 || before.Size > maxDiagnosticBytes ||
		before.Mode&0o777 != 0o600 {
		return nil, unix.Stat_t{}, errors.New("actor evidence is not a private bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxDiagnosticBytes+1))
	if err != nil || int64(len(body)) != before.Size {
		return nil, unix.Stat_t{}, errors.Join(err, errors.New("actor evidence size changed"))
	}
	if afterRead != nil {
		afterRead()
	}
	var after, named unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, unix.Stat_t{}, err
	}
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, unix.Stat_t{}, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size ||
		before.Dev != named.Dev || before.Ino != named.Ino || before.Size != named.Size ||
		before.Mode != after.Mode || before.Mode != named.Mode ||
		before.Nlink != after.Nlink || before.Nlink != named.Nlink ||
		after.Mode&unix.S_IFMT != unix.S_IFREG || after.Mode&0o777 != 0o600 ||
		named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o777 != 0o600 {
		return nil, unix.Stat_t{}, errors.New("actor evidence identity changed")
	}
	return body, named, nil
}

func actorEvidenceError(err error) error {
	if err == nil {
		return ErrServiceEvidence
	}
	return fmt.Errorf("%w: %v", ErrServiceEvidence, err)
}

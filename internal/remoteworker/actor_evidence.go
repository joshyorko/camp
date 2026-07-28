package remoteworker

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
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
	fsync                  func(int) error
	rename                 func(int, string, int, string, uint) error
	beforeRename           func(int, string) error
	afterCleanupCheck      func(int, string) error
	afterCleanupQuarantine func(int, string) error
	afterCleanupValidation func(int, string) error
	write                  func(*os.File, []byte) (int, error)
	chmod                  func(*os.File, os.FileMode) error
	fileSync               func(*os.File) error
}

func defaultServiceActorPublicationOps() serviceActorPublicationOps {
	return serviceActorPublicationOps{
		fsync:    unix.Fsync,
		rename:   unix.Renameat2,
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
	if existing, observeErr := observeServiceActorAt(parentFD, name, nil); observeErr == nil {
		if bytes.Equal(existing, body) {
			if err := ops.fsync(parentFD); err != nil {
				return actorEvidenceError(err)
			}
			return nil
		}
		return actorEvidenceError(errors.New("existing actor evidence differs"))
	} else if !errors.Is(observeErr, syscall.ENOENT) {
		return actorEvidenceError(observeErr)
	}

	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	partial := "." + name + ".partial-" + hex.EncodeToString(random)
	fd, err := unix.Openat(parentFD, partial, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return actorEvidenceError(err)
	}
	var partialStat unix.Stat_t
	if err := unix.Fstat(fd, &partialStat); err != nil {
		unix.Close(fd)
		return actorEvidenceError(err)
	}
	file := os.NewFile(uintptr(fd), partial)
	cleanup := true
	defer func() {
		if cleanup {
			if err := cleanupServiceActorPartial(parentFD, partial, partialStat, int64(len(body)), ops); err != nil {
				resultErr = actorEvidenceError(errors.Join(resultErr, err))
			}
		}
	}()
	if n, err := ops.write(file, body); err != nil {
		file.Close()
		return actorEvidenceError(err)
	} else if n != len(body) {
		file.Close()
		return actorEvidenceError(io.ErrShortWrite)
	}
	if err := ops.chmod(file, 0o600); err != nil {
		file.Close()
		return actorEvidenceError(err)
	}
	if err := ops.fileSync(file); err != nil {
		file.Close()
		return actorEvidenceError(err)
	}
	if err := file.Close(); err != nil {
		return actorEvidenceError(err)
	}
	if err := ops.fsync(parentFD); err != nil {
		return actorEvidenceError(err)
	}
	if ops.beforeRename != nil {
		if err := ops.beforeRename(parentFD, partial); err != nil {
			return actorEvidenceError(err)
		}
	}
	if err := ops.rename(parentFD, partial, parentFD, name, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			existing, observeErr := observeServiceActorAt(parentFD, name, nil)
			if observeErr == nil && bytes.Equal(existing, body) {
				if fsyncErr := ops.fsync(parentFD); fsyncErr != nil {
					return actorEvidenceError(fsyncErr)
				}
				return nil
			}
			return actorEvidenceError(errors.Join(err, observeErr))
		}
		return actorEvidenceError(err)
	}
	cleanup = false
	if err := ops.fsync(parentFD); err != nil {
		return actorEvidenceError(err)
	}
	published, err := observeServiceActorAt(parentFD, name, nil)
	if err != nil || !bytes.Equal(published, body) {
		return actorEvidenceError(errors.Join(err, errors.New("published actor evidence differs")))
	}
	return nil
}

func cleanupServiceActorPartial(
	parentFD int,
	name string,
	created unix.Stat_t,
	maxSize int64,
	ops serviceActorPublicationOps,
) error {
	var named unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("observe actor evidence partial for cleanup: %w", err)
	}
	if !validServiceActorPartial(created, named, maxSize) {
		return errors.New("actor evidence partial identity or shape changed; refusing cleanup")
	}
	if ops.afterCleanupCheck != nil {
		if err := ops.afterCleanupCheck(parentFD, name); err != nil {
			return fmt.Errorf("actor evidence cleanup boundary: %w", err)
		}
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("allocate actor evidence partial quarantine: %w", err)
	}
	quarantine := ".actor-cleanup-" + hex.EncodeToString(random)
	if err := ops.rename(parentFD, name, parentFD, quarantine, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("quarantine actor evidence partial: %w", err)
	}
	if err := ops.fsync(parentFD); err != nil {
		restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
		return fmt.Errorf("sync actor evidence partial quarantine: %w", errors.Join(err, restoreErr))
	}
	if ops.afterCleanupQuarantine != nil {
		if err := ops.afterCleanupQuarantine(parentFD, name); err != nil {
			restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
			return fmt.Errorf("actor evidence quarantined cleanup boundary: %w", errors.Join(err, restoreErr))
		}
	}
	var quarantined unix.Stat_t
	if err := unix.Fstatat(parentFD, quarantine, &quarantined, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
		return fmt.Errorf("observe quarantined actor evidence partial: %w", errors.Join(err, restoreErr))
	}
	if !validServiceActorPartial(created, quarantined, maxSize) {
		restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
		return fmt.Errorf(
			"actor evidence partial identity or shape changed; refusing cleanup: %w",
			errors.Join(restoreErr, ErrServiceEvidence),
		)
	}
	if ops.afterCleanupValidation != nil {
		if err := ops.afterCleanupValidation(parentFD, quarantine); err != nil {
			restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
			return fmt.Errorf("actor evidence quarantine validation boundary: %w", errors.Join(err, restoreErr))
		}
	}
	capture, placeholder, err := createServiceActorCleanupPlaceholder(parentFD)
	if err != nil {
		restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
		return fmt.Errorf("create actor evidence cleanup displacement: %w", errors.Join(err, restoreErr))
	}
	if err := ops.fsync(parentFD); err != nil {
		restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
		return fmt.Errorf("sync actor evidence cleanup displacement: %w", errors.Join(err, restoreErr))
	}
	if err := ops.rename(parentFD, quarantine, parentFD, capture, unix.RENAME_EXCHANGE); err != nil {
		restoreErr := restoreServiceActorPartial(parentFD, quarantine, name, ops)
		return fmt.Errorf("capture actor evidence partial for deletion: %w", errors.Join(err, restoreErr))
	}
	if err := ops.fsync(parentFD); err != nil {
		return fmt.Errorf("sync captured actor evidence partial: %w", err)
	}
	var captured, displaced unix.Stat_t
	capturedErr := unix.Fstatat(parentFD, capture, &captured, unix.AT_SYMLINK_NOFOLLOW)
	displacedErr := unix.Fstatat(parentFD, quarantine, &displaced, unix.AT_SYMLINK_NOFOLLOW)
	if capturedErr != nil || displacedErr != nil ||
		!validServiceActorPartial(created, captured, maxSize) ||
		!samePrivateEmptyServiceActorPlaceholder(placeholder, displaced) {
		restoreErr := restoreServiceActorPartial(parentFD, capture, name, ops)
		return fmt.Errorf(
			"captured actor evidence partial identity or shape changed; refusing cleanup: %w",
			errors.Join(capturedErr, displacedErr, restoreErr, ErrServiceEvidence),
		)
	}
	if err := unix.Unlinkat(parentFD, capture, 0); err != nil {
		return fmt.Errorf("remove captured actor evidence partial: %w", err)
	}
	if err := ops.fsync(parentFD); err != nil {
		return fmt.Errorf("sync removed captured actor evidence partial: %w", err)
	}
	if err := unix.Unlinkat(parentFD, quarantine, 0); err != nil {
		return fmt.Errorf("remove actor evidence cleanup placeholder: %w", err)
	}
	if err := ops.fsync(parentFD); err != nil {
		return fmt.Errorf("sync removed actor evidence cleanup placeholder: %w", err)
	}
	return nil
}

func createServiceActorCleanupPlaceholder(parentFD int) (string, unix.Stat_t, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", unix.Stat_t{}, err
	}
	name := ".actor-displace-" + hex.EncodeToString(random)
	fd, err := unix.Openat(
		parentFD,
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return "", unix.Stat_t{}, err
	}
	defer unix.Close(fd)
	var created unix.Stat_t
	if err := unix.Fstat(fd, &created); err != nil {
		return "", unix.Stat_t{}, err
	}
	if created.Mode&unix.S_IFMT != unix.S_IFREG || created.Mode&0o777 != 0o600 ||
		created.Size != 0 || created.Nlink != 1 {
		return "", unix.Stat_t{}, ErrServiceEvidence
	}
	return name, created, nil
}

func samePrivateEmptyServiceActorPlaceholder(created, current unix.Stat_t) bool {
	return created.Dev == current.Dev && created.Ino == current.Ino &&
		current.Mode&unix.S_IFMT == unix.S_IFREG && current.Mode&0o777 == 0o600 &&
		current.Size == 0 && current.Nlink == 1
}

func validServiceActorPartial(created, current unix.Stat_t, maxSize int64) bool {
	return created.Mode&unix.S_IFMT == unix.S_IFREG && created.Mode&0o077 == 0 && created.Size == 0 &&
		created.Dev == current.Dev && created.Ino == current.Ino &&
		current.Mode&unix.S_IFMT == unix.S_IFREG && current.Mode&0o077 == 0 &&
		current.Size >= 0 && current.Size <= maxSize && maxSize <= maxDiagnosticBytes
}

func restoreServiceActorPartial(
	parentFD int,
	quarantine string,
	name string,
	ops serviceActorPublicationOps,
) error {
	if err := ops.rename(parentFD, quarantine, parentFD, name, unix.RENAME_NOREPLACE); err != nil {
		return fmt.Errorf("preserve quarantined actor evidence partial without overwriting current name: %w", err)
	}
	if err := ops.fsync(parentFD); err != nil {
		return fmt.Errorf("sync restored actor evidence partial: %w", err)
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
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 || before.Size > maxDiagnosticBytes ||
		before.Mode&0o777 != 0o600 {
		return nil, errors.New("actor evidence is not a private bounded regular file")
	}
	body, err := io.ReadAll(io.LimitReader(file, maxDiagnosticBytes+1))
	if err != nil || int64(len(body)) != before.Size {
		return nil, errors.Join(err, errors.New("actor evidence size changed"))
	}
	if afterRead != nil {
		afterRead()
	}
	var after, named unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if err := unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size ||
		before.Dev != named.Dev || before.Ino != named.Ino || before.Size != named.Size ||
		after.Mode&unix.S_IFMT != unix.S_IFREG || after.Mode&0o777 != 0o600 ||
		named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o777 != 0o600 {
		return nil, errors.New("actor evidence identity changed")
	}
	return body, nil
}

func actorEvidenceError(err error) error {
	if err == nil {
		return ErrServiceEvidence
	}
	return fmt.Errorf("%w: %v", ErrServiceEvidence, err)
}

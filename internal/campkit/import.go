package campkit

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/klauspost/compress/zstd"
)

var importBeforeExtract = func(string, string) error { return nil }

// ImportResult describes an import whose archive was verified before any
// destination directory was published.
type ImportResult struct {
	Manifest     Manifest     `json:"manifest"`
	Verification Verification `json:"verification"`
	Destination  string       `json:"destination"`
}

// ImportFile verifies input completely, extracts only verified regular
// payloads into an owned staging directory, and publishes that directory
// without replacing an existing destination.
func ImportFile(ctx context.Context, input, destination string, evaluator TrustEvaluator) (ImportResult, error) {
	if input == "" || destination == "" {
		return ImportResult{}, fmt.Errorf("CampKit input and destination are required")
	}
	file, err := os.Open(input)
	if err != nil {
		return ImportResult{}, err
	}
	snapshot, err := os.CreateTemp("", "campkit-verified-*")
	if err != nil {
		_ = file.Close()
		return ImportResult{}, err
	}
	snapshotPath := snapshot.Name()
	cleanupSnapshot := true
	defer func() {
		_ = file.Close()
		_ = snapshot.Close()
		if cleanupSnapshot {
			_ = removeOwnedPath(snapshotPath, snapshotIdentity(snapshotPath))
		}
	}()
	if _, err := io.Copy(snapshot, &contextReader{ctx: ctx, reader: file}); err != nil {
		return ImportResult{}, err
	}
	if err := snapshot.Sync(); err != nil {
		return ImportResult{}, err
	}
	if err := file.Close(); err != nil {
		return ImportResult{}, err
	}
	if err := snapshot.Chmod(0o400); err != nil {
		return ImportResult{}, err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return ImportResult{}, err
	}
	verification, err := Verify(ctx, &contextReader{ctx: ctx, reader: snapshot}, DefaultArchiveLimits(), evaluator)
	if err != nil {
		return ImportResult{}, err
	}
	if _, err := snapshot.Seek(0, io.SeekStart); err != nil {
		return ImportResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ImportResult{}, err
	}

	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return ImportResult{}, err
	}
	if _, err := os.Lstat(destination); err == nil {
		return ImportResult{}, fmt.Errorf("CampKit destination already exists: %w", os.ErrExist)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ImportResult{}, err
	}
	staging, err := os.MkdirTemp(parent, ".campkit-import-")
	if err != nil {
		return ImportResult{}, err
	}
	stagingIdentity, err := directoryIdentity(staging)
	if err != nil {
		return ImportResult{}, err
	}
	owned := true
	defer func() {
		if owned {
			_ = removeOwnedTree(staging, stagingIdentity)
		}
	}()

	if err := importBeforeExtract(input, staging); err != nil {
		return ImportResult{}, err
	}
	if err := extractVerified(ctx, snapshot, staging, verification.Manifest, DefaultArchiveLimits()); err != nil {
		return ImportResult{}, err
	}
	if err := syncTree(ctx, staging); err != nil {
		return ImportResult{}, err
	}
	if err := publishNoReplace(staging, destination); err != nil {
		return ImportResult{}, fmt.Errorf("publish imported CampKit: %w", err)
	}
	owned = false
	if err := syncDirectory(parent); err != nil {
		return ImportResult{}, err
	}
	cleanupSnapshot = false
	return ImportResult{Manifest: verification.Manifest, Verification: verification, Destination: destination}, nil
}

func extractVerified(ctx context.Context, input io.Reader, destination string, manifest Manifest, limits ArchiveLimits) error {
	decoder, err := zstd.NewReader(input,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxMemory(uint64(limits.MaxOuterExpandedBytes)),
		zstd.WithDecoderMaxWindow(uint64(limits.MaxOuterEntryBytes)),
	)
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read verified CampKit: %w", err)
		}
		if header.Name == "manifest.json" {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("verified CampKit payload %q is not regular", header.Name)
		}
		payload := findPayload(manifest.Payloads, header.Name)
		if payload.Path == "" {
			return fmt.Errorf("verified CampKit payload %q is not declared", header.Name)
		}
		path := filepath.Join(destination, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, &contextReader{ctx: ctx, reader: io.LimitReader(reader, payload.Size)})
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Chmod(path, 0o444); err != nil {
			return err
		}
	}
}

type pathIdentity struct {
	device uint64
	inode  uint64
}

func snapshotIdentity(path string) pathIdentity {
	identity, _ := directoryIdentity(path)
	return identity
}

func directoryIdentity(path string) (pathIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return pathIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return pathIdentity{}, errors.New("path identity unavailable")
	}
	return pathIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func removeOwnedPath(path string, want pathIdentity) error {
	got, err := directoryIdentity(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("refusing cleanup of replaced path")
	}
	return os.Remove(path)
}

func removeOwnedTree(path string, want pathIdentity) error {
	got, err := directoryIdentity(path)
	if err != nil || got != want {
		return nil
	}
	return os.RemoveAll(path)
}

func syncTree(ctx context.Context, root string) error {
	var directories []string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink in imported tree: %w", ErrUnsafeArchive)
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		return errors.Join(err, closeErr)
	}); err != nil {
		return err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return err
		}
		dir, err := os.Open(directories[i])
		if err != nil {
			return err
		}
		err = dir.Sync()
		closeErr := dir.Close()
		if err := errors.Join(err, closeErr); err != nil {
			return err
		}
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	return errors.Join(err, closeErr)
}

package haulkit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrInvalidChunks = errors.New("invalid Camp Hauler kit chunks")

var atomicBoundaryHook func(string) error
var beforeOpenRegular func(string) error

func Split(ctx context.Context, archive, directory string, chunkSize int64) ([]ChunkIdentity, error) {
	if chunkSize <= 0 || chunkSize > DefaultChunkSize || !filepath.IsAbs(archive) || !filepath.IsAbs(directory) {
		return nil, fmt.Errorf("%w: invalid split request", ErrInvalidChunks)
	}
	source, err := openRegular(archive)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = os.RemoveAll(directory)
			_ = syncDirectory(filepath.Dir(directory))
		}
	}()
	var chunks []ChunkIdentity
	for index := uint32(0); ; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := fmt.Sprintf("%s.part-%06d", filepath.Base(archive), index)
		path := filepath.Join(directory, name)
		temporary, err := os.CreateTemp(directory, ".chunk-*")
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		if err := runAtomicBoundaryHook("chunk-write"); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
			return nil, err
		}
		written, copyErr := io.CopyN(io.MultiWriter(temporary, hash), source, chunkSize)
		if errors.Is(copyErr, io.EOF) {
			copyErr = nil
		}
		if written == 0 {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
			if copyErr != nil {
				return nil, copyErr
			}
			break
		}
		if copyErr != nil {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
			return nil, copyErr
		}
		if err := syncPublish(temporary, path); err != nil {
			return nil, err
		}
		chunks = append(chunks, ChunkIdentity{Index: index, Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: written})
		if written < chunkSize {
			break
		}
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("%w: empty archive", ErrInvalidChunks)
	}
	if err := syncDirectory(directory); err != nil {
		return nil, err
	}
	success = true
	return chunks, nil
}

func Reassemble(ctx context.Context, directory string, chunks []ChunkIdentity, output string, maxChunkSize int64) error {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(output) || maxChunkSize <= 0 || maxChunkSize > DefaultChunkSize || len(chunks) == 0 {
		return fmt.Errorf("%w: invalid reassembly request", ErrInvalidChunks)
	}
	temporary, err := os.CreateTemp(filepath.Dir(output), ".haulkit-reassemble-*")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
			_ = syncDirectory(filepath.Dir(output))
		}
	}()
	seen := make(map[string]struct{}, len(chunks))
	for index, identity := range chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if identity.Index != uint32(index) || !safeRelativeName(identity.Name) || !validSHA256(identity.SHA256) ||
			identity.Size <= 0 || identity.Size > maxChunkSize {
			return fmt.Errorf("%w: malformed chunk identity", ErrInvalidChunks)
		}
		if _, exists := seen[identity.Name]; exists {
			return fmt.Errorf("%w: duplicate chunk", ErrInvalidChunks)
		}
		seen[identity.Name] = struct{}{}
		chunk, err := openRegular(filepath.Join(directory, identity.Name))
		if err != nil {
			return err
		}
		hash := sha256.New()
		if err := runAtomicBoundaryHook("reassembly-write"); err != nil {
			_ = chunk.Close()
			return err
		}
		written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(chunk, identity.Size+1))
		closeErr := chunk.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != identity.Size || hex.EncodeToString(hash.Sum(nil)) != identity.SHA256 {
			return fmt.Errorf("%w: chunk %d size or digest mismatch", ErrInvalidChunks, index)
		}
	}
	if err := syncPublish(temporary, output); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(output)); err != nil {
		_ = os.Remove(output)
		return err
	}
	cleanup = false
	return nil
}

func openRegular(path string) (*os.File, error) {
	if beforeOpenRegular != nil {
		if err := beforeOpenRegular(path); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %q is not a regular file", ErrInvalidChunks, path)
	}
	return file, nil
}

func syncPublish(temporary *os.File, output string) error {
	name := temporary.Name()
	if err := runAtomicBoundaryHook("file-fsync"); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Chmod(0o400); err != nil {
		_ = temporary.Close()
		_ = os.Remove(name)
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := runAtomicBoundaryHook("publish"); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Link(name, output); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Remove(name); err != nil {
		_ = os.Remove(output)
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := runAtomicBoundaryHook("directory-fsync"); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return err
	}
	return nil
}

func runAtomicBoundaryHook(boundary string) error {
	if atomicBoundaryHook == nil {
		return nil
	}
	return atomicBoundaryHook(boundary)
}

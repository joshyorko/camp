package campkit

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

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
	verification, err := Verify(ctx, file, DefaultArchiveLimits(), evaluator)
	closeErr := file.Close()
	if err != nil {
		return ImportResult{}, err
	}
	if closeErr != nil {
		return ImportResult{}, closeErr
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
	owned := true
	defer func() {
		if owned {
			_ = os.RemoveAll(staging)
		}
	}()

	if err := extractVerified(ctx, input, staging, verification.Manifest); err != nil {
		return ImportResult{}, err
	}
	if err := publishNoReplace(staging, destination); err != nil {
		return ImportResult{}, fmt.Errorf("publish imported CampKit: %w", err)
	}
	owned = false
	return ImportResult{Manifest: verification.Manifest, Verification: verification, Destination: destination}, nil
}

func extractVerified(ctx context.Context, input, destination string, manifest Manifest) error {
	file, err := os.Open(input)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true))
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
		_, copyErr := io.Copy(out, io.LimitReader(reader, payload.Size))
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

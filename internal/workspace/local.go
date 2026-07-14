package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrNotLocalNoop = errors.New("workspace is not a proven local no-op")
	ErrTargetEscape = errors.New("workspace target escapes effective root")
)

type Local struct{}

func (Local) ReturnToStaging(ctx context.Context, request ports.MirrorRequest) (ports.MirrorResult, error) {
	if err := ctx.Err(); err != nil {
		return ports.MirrorResult{}, err
	}
	if !request.LocalProvider || request.Provider == "" {
		return ports.MirrorResult{}, ErrNotLocalNoop
	}
	staging, err := canonicalDirectory(request.StagingRoot)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	workspace, err := canonicalDirectory(request.WorkspaceLocalFolder)
	if err != nil {
		return ports.MirrorResult{}, err
	}
	if staging != workspace {
		return ports.MirrorResult{}, ErrNotLocalNoop
	}
	return ports.MirrorResult{Mode: ports.MirrorLocalNoop, Root: staging}, nil
}

func MapTarget(stagingRoot, effectiveRoot, relative string) (string, error) {
	if !filepath.IsAbs(stagingRoot) || !filepath.IsAbs(effectiveRoot) || filepath.IsAbs(relative) {
		return "", ErrTargetEscape
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrTargetEscape
	}
	mapped := filepath.Join(effectiveRoot, clean)
	relativeToRoot, err := filepath.Rel(effectiveRoot, mapped)
	if err != nil || relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(filepath.Separator)) {
		return "", ErrTargetEscape
	}
	return mapped, nil
}

func DeterministicID(capsule, lineage, root string) string {
	digest := sha256.Sum256([]byte(capsule + "\x00" + lineage + "\x00" + root))
	slug := strings.Trim(sanitize(capsule), "-")
	if slug == "" {
		slug = "capsule"
	}
	if len(slug) > 24 {
		slug = slug[:24]
	}
	return "camp-" + slug + "-" + hex.EncodeToString(digest[:6])
}

func sanitize(value string) string {
	var builder strings.Builder
	dash := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			dash = false
		} else if !dash {
			builder.WriteByte('-')
			dash = true
		}
	}
	return builder.String()
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", absolute)
	}
	return filepath.EvalSymlinks(absolute)
}

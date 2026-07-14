package target

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrTargetNotFound = errors.New("target directory not found")
	ErrTargetOutside  = errors.New("target resolves outside capsule root")
)

type AmbiguousError struct {
	Target     string
	Candidates []string
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("target %q is ambiguous: %s", e.Target, strings.Join(e.Candidates, ", "))
}

type Zoxide interface {
	Query(context.Context, string) ([]string, error)
}

type CommandZoxide struct {
	executable string
	runner     ports.Runner
}

func NewCommandZoxide(executable string, runner ports.Runner) *CommandZoxide {
	return &CommandZoxide{executable: executable, runner: runner}
}

func (z *CommandZoxide) Query(ctx context.Context, requested string) ([]string, error) {
	if z == nil || z.executable == "" || z.runner == nil {
		return nil, errors.New("zoxide is unavailable")
	}
	result, err := z.runner.Run(ctx, ports.Command{Executable: z.executable, Argv: []string{"query", "--list", requested}})
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		if path := strings.TrimSpace(line); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

type Resolver struct {
	Zoxide Zoxide
}

type Result struct {
	Absolute string
	Relative string
}

func (r Resolver) Resolve(ctx context.Context, root, requested string) (Result, error) {
	canonicalRoot, err := canonicalDirectory(root)
	if err != nil {
		return Result{}, fmt.Errorf("resolve capsule root: %w", err)
	}
	if requested == "" {
		return result(canonicalRoot, canonicalRoot)
	}
	if filepath.IsAbs(requested) {
		candidate, err := canonicalDirectory(requested)
		if err != nil {
			return Result{}, fmt.Errorf("absolute target: %w", ErrTargetNotFound)
		}
		if !within(canonicalRoot, candidate) {
			return Result{}, ErrTargetOutside
		}
		return result(canonicalRoot, candidate)
	}
	direct := filepath.Join(canonicalRoot, filepath.Clean(requested))
	if candidate, err := canonicalDirectory(direct); err == nil {
		if !within(canonicalRoot, candidate) {
			return Result{}, ErrTargetOutside
		}
		return result(canonicalRoot, candidate)
	}
	matches, err := basenameMatches(ctx, canonicalRoot, requested)
	if err != nil {
		return Result{}, err
	}
	if len(matches) == 1 {
		return result(canonicalRoot, matches[0])
	}
	if len(matches) > 1 {
		return Result{}, ambiguity(canonicalRoot, requested, matches)
	}
	if r.Zoxide == nil {
		return Result{}, fmt.Errorf("target %q: %w", requested, ErrTargetNotFound)
	}
	paths, err := r.Zoxide.Query(ctx, requested)
	if err != nil {
		return Result{}, fmt.Errorf("zoxide target query: %w", err)
	}
	var accepted []string
	for _, path := range paths {
		candidate, err := canonicalDirectory(path)
		if err != nil {
			continue
		}
		if !within(canonicalRoot, candidate) {
			return Result{}, ErrTargetOutside
		}
		accepted = append(accepted, candidate)
	}
	accepted = uniqueSorted(accepted)
	if len(accepted) == 1 {
		return result(canonicalRoot, accepted[0])
	}
	if len(accepted) > 1 {
		return Result{}, ambiguity(canonicalRoot, requested, accepted)
	}
	return Result{}, fmt.Errorf("target %q: %w", requested, ErrTargetNotFound)
}

func basenameMatches(ctx context.Context, root, requested string) ([]string, error) {
	if filepath.Base(requested) != requested || requested == "." || requested == ".." {
		return nil, nil
	}
	var matches []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == filepath.Join(root, ".camp") && entry.IsDir() {
			return filepath.SkipDir
		}
		if path != root && entry.IsDir() && entry.Name() == requested {
			canonical, err := canonicalDirectory(path)
			if err == nil && within(root, canonical) {
				matches = append(matches, canonical)
			}
		}
		return nil
	})
	return uniqueSorted(matches), err
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
		return "", errors.New("not a directory")
	}
	return filepath.EvalSymlinks(absolute)
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func result(root, absolute string) (Result, error) {
	relative, err := filepath.Rel(root, absolute)
	if err != nil {
		return Result{}, err
	}
	return Result{Absolute: absolute, Relative: filepath.ToSlash(relative)}, nil
}

func ambiguity(root, requested string, paths []string) *AmbiguousError {
	candidates := make([]string, 0, len(paths))
	for _, path := range paths {
		relative, _ := filepath.Rel(root, path)
		candidates = append(candidates, filepath.ToSlash(relative))
	}
	sort.Strings(candidates)
	return &AmbiguousError{Target: requested, Candidates: candidates}
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

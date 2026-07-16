package target

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type fakeZoxide struct {
	results []string
	err     error
}

func (z fakeZoxide) Query(context.Context, string) ([]string, error) { return z.results, z.err }

func TestResolveUsesAbsoluteRelativeThenUniqueBasename(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	memory := filepath.Join(root, "MemoryD")
	nested := filepath.Join(root, "Projects", "camp")
	for _, path := range []string{memory, nested} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	resolver := Resolver{}
	for _, test := range []struct {
		name    string
		input   string
		want    string
		wantRel string
	}{
		{"root", "", root, "."},
		{"absolute", memory, memory, "MemoryD"},
		{"relative", "Projects/camp", nested, "Projects/camp"},
		{"basename", "camp", nested, "Projects/camp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := resolver.Resolve(context.Background(), root, test.input)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if resolved.Absolute != test.want || resolved.Relative != test.wantRel {
				t.Fatalf("resolved = %#v", resolved)
			}
		})
	}
}

func TestResolveRejectsAmbiguityAndZoxideEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{filepath.Join(root, "a", "same"), filepath.Join(root, "b", "same")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := (Resolver{}).Resolve(context.Background(), root, "same")
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) || !reflect.DeepEqual(ambiguous.Candidates, []string{"a/same", "b/same"}) {
		t.Fatalf("ambiguous error = %#v", err)
	}

	outside := t.TempDir()
	_, err = (Resolver{Zoxide: fakeZoxide{results: []string{outside}}}).Resolve(context.Background(), root, "zoxide-only")
	if !errors.Is(err, ErrTargetOutside) {
		t.Fatalf("zoxide escape error = %v", err)
	}
	inside := filepath.Join(root, "zoxide-target")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := (Resolver{Zoxide: fakeZoxide{results: []string{inside}}}).Resolve(context.Background(), root, "zoxide-only")
	if err != nil || resolved.Absolute != inside {
		t.Fatalf("zoxide inside = %#v, %v", resolved, err)
	}
}

type recordingRunner struct{ command ports.Command }

func (r *recordingRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.command = command
	return ports.Result{Stdout: []byte("/root/a\n/root/b\n")}, nil
}

func TestCommandZoxideUsesStructuredQueryAndParsesCandidates(t *testing.T) {
	t.Parallel()
	runner := &recordingRunner{}
	paths, err := NewCommandZoxide("/usr/bin/zoxide", runner).Query(context.Background(), "Memory D; touch nope")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{"/root/a", "/root/b"}) || !reflect.DeepEqual(runner.command.Argv, []string{"query", "--list", "Memory D; touch nope"}) {
		t.Fatalf("paths=%#v command=%#v", paths, runner.command)
	}
}

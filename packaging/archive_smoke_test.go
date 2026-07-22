package packaging_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	testVersion = "0.0.0-test"
	testCommit  = "0123456789abcdef0123456789abcdef01234567"
	testEpoch   = "1784678400"
)

func TestArchiveBuildIsReproducibleAndSmokeTestable(t *testing.T) {
	repositoryRoot := filepath.Clean("..")
	first := filepath.Join(t.TempDir(), "dist")
	second := filepath.Join(t.TempDir(), "dist")

	buildArchives(t, repositoryRoot, first)
	buildArchives(t, repositoryRoot, second)

	if got, want := directoryDigests(t, first), directoryDigests(t, second); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated builds differ:\nfirst:  %v\nsecond: %v", got, want)
	}

	archive := filepath.Join(first, "camp_"+testVersion+"_linux_amd64.tar.gz")
	extracted := t.TempDir()
	extractArchive(t, archive, extracted)
	root := filepath.Join(extracted, "camp_"+testVersion+"_linux_amd64")
	for _, name := range []string{
		"camp",
		"README.md",
		"INSTALL.md",
		"completions/camp.bash",
		"completions/_camp",
		"completions/camp.fish",
	} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("archive entry %q: %v", name, err)
		}
	}

	binary := filepath.Join(root, "camp")
	assertCommandContains(t, binary, []string{"--version"}, testVersion, testCommit)
	assertCommandContains(t, binary, []string{"--help"}, "Available Commands:", "completion", "open", "sync", "close")
	for _, shell := range []string{"bash", "zsh", "fish"} {
		assertCommandContains(t, binary, []string{"completion", shell}, "camp")
	}

	install, err := os.ReadFile(filepath.Join(root, "INSTALL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(install), "passt") || !strings.Contains(string(install), "pasta") {
		t.Fatalf("INSTALL.md does not declare the external passt/pasta prerequisite:\n%s", install)
	}
}

func buildArchives(t *testing.T, repositoryRoot, output string) {
	t.Helper()
	command := exec.Command("./packaging/build-archives.sh")
	command.Dir = repositoryRoot
	command.Env = append(os.Environ(),
		"VERSION="+testVersion,
		"COMMIT="+testCommit,
		"SOURCE_DATE_EPOCH="+testEpoch,
		"OUTPUT_DIR="+output,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build archives: %v\n%s", err, output)
	}
}

func directoryDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	digests := make(map[string]string, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		digests[entry.Name()] = fmt.Sprintf("%x", sha256.Sum256(contents))
	}
	return digests
}

func extractArchive(t *testing.T, archive, destination string) {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tape := tar.NewReader(gzipReader)
	for {
		header, err := tape.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(destination, filepath.Clean(header.Name))
		if !strings.HasPrefix(target, destination+string(os.PathSeparator)) {
			t.Fatalf("unsafe archive path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(output, tape); err != nil {
				output.Close()
				t.Fatal(err)
			}
			if err := output.Close(); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected archive entry type %d for %q", header.Typeflag, header.Name)
		}
	}
}

func assertCommandContains(t *testing.T, binary string, args []string, fragments ...string) {
	t.Helper()
	command := exec.Command(binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", binary, strings.Join(args, " "), err, output)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(output), fragment) {
			t.Fatalf("%s %s output does not contain %q:\n%s", binary, strings.Join(args, " "), fragment, output)
		}
	}
}

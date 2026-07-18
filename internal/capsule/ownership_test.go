package capsule

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/domain"
	"golang.org/x/sys/unix"
)

func TestOwnershipMarkerCreationDoesNotExposePartialFinalOnWriteFailure(t *testing.T) {
	if os.Getenv("CAMP_OWNERSHIP_SHORT_WRITE_HELPER") == "1" {
		runOwnershipShortWriteHelper(t)
		return
	}

	command := exec.Command(os.Args[0], "-test.run=^TestOwnershipMarkerCreationDoesNotExposePartialFinalOnWriteFailure$")
	command.Env = append(os.Environ(), "CAMP_OWNERSHIP_SHORT_WRITE_HELPER=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("short-write helper failed: %v\n%s", err, output)
	}
}

func runOwnershipShortWriteHelper(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenBytes := make([]byte, 32)
	for index := range tokenBytes {
		tokenBytes[index] = 0xaa
	}
	token := hex.EncodeToString(tokenBytes)

	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}
	limit.Cur = 1
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal(err)
	}

	if _, err := ownership.MarkCreatedWithToken(root, token); err == nil {
		t.Fatal("MarkCreatedWithToken() error = nil, want short-write failure")
	}
	markerPath := filepath.Join(root, ".camp", "runtime", "ownership.json")
	body, err := os.ReadFile(markerPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, device, inode, err := inspectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	want := ownershipMarker{Token: token, CanonicalPath: canonical, Device: device, Inode: inode}
	var got ownershipMarker
	if err := json.Unmarshal(body, &got); err != nil || got != want {
		t.Fatalf("write failure exposed malformed final marker %q: %v", body, err)
	}
}

func TestOwnershipMarkerCreationCrashCutsNeverExposeMalformedFinal(t *testing.T) {
	t.Parallel()
	marker := ownershipMarker{
		Token:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalPath: "/materializations/session-a",
		Device:        11,
		Inode:         29,
	}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("injected crash cut")

	tests := []struct {
		name          string
		operations    ownershipMarkerOperations
		wantPublished bool
	}{
		{
			name: "during write",
			operations: ownershipMarkerOperations{
				write: func(file *os.File, body []byte) error {
					if _, err := file.Write(body[:1]); err != nil {
						return err
					}
					return crash
				},
				sync:    (*os.File).Sync,
				publish: publishOwnershipMarkerNoReplace,
			},
		},
		{
			name: "after file fsync before publish",
			operations: ownershipMarkerOperations{
				write: writeOwnershipMarker,
				sync: func(file *os.File) error {
					if err := file.Sync(); err != nil {
						return err
					}
					return crash
				},
				publish: publishOwnershipMarkerNoReplace,
			},
		},
		{
			name: "before no-replace publish",
			operations: ownershipMarkerOperations{
				write: writeOwnershipMarker,
				sync:  (*os.File).Sync,
				publish: func(int, int, string) error {
					return crash
				},
			},
		},
		{
			name: "after no-replace publish",
			operations: ownershipMarkerOperations{
				write: writeOwnershipMarker,
				sync:  (*os.File).Sync,
				publish: func(directoryFD, temporaryFD int, newName string) error {
					if err := publishOwnershipMarkerNoReplace(directoryFD, temporaryFD, newName); err != nil {
						return err
					}
					return crash
				},
			},
			wantPublished: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directoryPath := t.TempDir()
			directory, err := os.Open(directoryPath)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			markerPath := filepath.Join(directoryPath, "ownership.json")
			if err := createOwnershipMarker(directory, body, marker, test.operations); !errors.Is(err, crash) {
				t.Fatalf("createOwnershipMarker() error = %v, want injected crash", err)
			}
			gotBody, err := os.ReadFile(markerPath)
			if !test.wantPublished && errors.Is(err, os.ErrNotExist) {
				return
			}
			if err != nil {
				t.Fatalf("read final marker: %v", err)
			}
			var got ownershipMarker
			if err := json.Unmarshal(gotBody, &got); err != nil || got != marker {
				t.Fatalf("crash cut exposed malformed final marker %q: %v", gotBody, err)
			}
			info, err := os.Stat(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode().Perm(); mode != 0o600 {
				t.Fatalf("final marker mode = %o, want 600", mode)
			}
		})
	}
}

func TestOwnershipMarkerCreationRejectsMismatchedExistingFinal(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	runtimeDirectory := filepath.Join(root, ".camp", "runtime")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(runtimeDirectory, "ownership.json")
	unexplained := []byte(`{"token":"unexplained"}`)
	if err := os.WriteFile(markerPath, unexplained, 0o600); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ownership.MarkCreatedWithToken(root, token); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
	}
	got, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(unexplained) {
		t.Fatalf("mismatched final marker was changed: got %q, want %q", got, unexplained)
	}
}

func TestOwnershipMarkerCreationRejectsUppercaseToken(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	token := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if _, err := ownership.MarkCreatedWithToken(root, token); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
	}
	markerPath := filepath.Join(root, ".camp", "runtime", "ownership.json")
	if _, err := os.Lstat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("noncanonical token created marker: %v", err)
	}
}

func TestOwnershipMarkerCreationRequiresStrictExistingMarker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(t *testing.T, markerPath string, body []byte) []byte
	}{
		{
			name: "mode is not 0600",
			mutate: func(t *testing.T, markerPath string, body []byte) []byte {
				t.Helper()
				if err := os.Chmod(markerPath, 0o644); err != nil {
					t.Fatal(err)
				}
				return body
			},
		},
		{
			name: "unknown field",
			mutate: func(t *testing.T, markerPath string, body []byte) []byte {
				t.Helper()
				body = append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"unknown":true}`)...)
				if err := os.WriteFile(markerPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
				return body
			},
		},
		{
			name: "duplicate field",
			mutate: func(t *testing.T, markerPath string, body []byte) []byte {
				t.Helper()
				body = append(append([]byte(nil), body[:len(body)-1]...), []byte(`,"token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)...)
				if err := os.WriteFile(markerPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
				return body
			},
		},
		{
			name: "trailing data",
			mutate: func(t *testing.T, markerPath string, body []byte) []byte {
				t.Helper()
				body = append(append([]byte(nil), body...), []byte(` {}`)...)
				if err := os.WriteFile(markerPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
				return body
			},
		},
		{
			name: "additional hard link",
			mutate: func(t *testing.T, markerPath string, body []byte) []byte {
				t.Helper()
				if err := os.Link(markerPath, markerPath+".link"); err != nil {
					t.Fatal(err)
				}
				return body
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dataHome := filepath.Join(t.TempDir(), "data")
			ownership, err := NewOwnership(dataHome)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(ownership.MaterializationRoot(), "session-a")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			if _, err := ownership.MarkCreatedWithToken(root, token); err != nil {
				t.Fatal(err)
			}
			markerPath := filepath.Join(root, ".camp", "runtime", "ownership.json")
			body, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			want := test.mutate(t, markerPath, body)
			if _, err := ownership.MarkCreatedWithToken(root, token); !errors.Is(err, ErrOwnershipMismatch) {
				t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
			}
			got, err := os.ReadFile(markerPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("unexplained marker changed: got %q, want %q", got, want)
			}
		})
	}
}

func TestOwnershipMarkerDirectorySyncsEachNewParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_, _, device, inode, err := inspectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	var synced []string
	runtimeDirectory, err := openOwnershipMarkerDirectory(root, device, inode, true, func(directory *os.File) error {
		synced = append(synced, directory.Name())
		return directory.Sync()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeDirectory.Close(); err != nil {
		t.Fatal(err)
	}
	want := []string{root, filepath.Join(root, ".camp")}
	if len(synced) != len(want) {
		t.Fatalf("synced parents = %q, want %q", synced, want)
	}
	for index := range want {
		if synced[index] != want[index] {
			t.Fatalf("synced parent %d = %q, want %q", index, synced[index], want[index])
		}
	}
}

func TestOwnershipMarkerCreationPreservesUnexplainedTemporary(t *testing.T) {
	t.Parallel()
	directoryPath := t.TempDir()
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	marker := ownershipMarker{
		Token:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalPath: "/materializations/session-a",
		Device:        11,
		Inode:         29,
	}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	crash := errors.New("injected write crash")
	unexplained := []byte("unexplained temporary")
	unexplainedPath := filepath.Join(directoryPath, ".ownership-unexplained.tmp")
	if err := os.WriteFile(unexplainedPath, unexplained, 0o600); err != nil {
		t.Fatal(err)
	}
	operations := ownershipMarkerOperations{
		write: func(file *os.File, body []byte) error {
			if err := writeOwnershipMarker(file, body); err != nil {
				return err
			}
			return crash
		},
		sync:    (*os.File).Sync,
		publish: publishOwnershipMarkerNoReplace,
	}
	if err := createOwnershipMarker(directory, body, marker, operations); !errors.Is(err, crash) {
		t.Fatalf("createOwnershipMarker() error = %v, want injected crash", err)
	}
	got, err := os.ReadFile(unexplainedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(unexplained) {
		t.Fatalf("unexplained temporary = %q, want %q", got, unexplained)
	}
}

func TestOwnershipMarkerCreationHandlesNoReplaceCollisionFailClosed(t *testing.T) {
	t.Parallel()
	marker := ownershipMarker{
		Token:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalPath: "/materializations/session-a",
		Device:        11,
		Inode:         29,
	}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		collision []byte
		wantError bool
	}{
		{name: "accept exact collision", collision: body},
		{name: "reject mismatched collision", collision: []byte(`{"token":"unexplained"}`), wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directoryPath := t.TempDir()
			directory, err := os.Open(directoryPath)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			stalePath := filepath.Join(directoryPath, ".ownership-unexplained.tmp")
			if err := os.WriteFile(stalePath, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			operations := ownershipMarkerOperations{
				write: writeOwnershipMarker,
				sync:  (*os.File).Sync,
				publish: func(directoryFD, temporaryFD int, newName string) error {
					writeOwnershipMarkerAt(t, directoryFD, newName, test.collision)
					return publishOwnershipMarkerNoReplace(directoryFD, temporaryFD, newName)
				},
			}
			err = createOwnershipMarker(directory, body, marker, operations)
			if test.wantError && !errors.Is(err, ErrOwnershipMismatch) {
				t.Fatalf("createOwnershipMarker() error = %v, want ErrOwnershipMismatch", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("createOwnershipMarker() error = %v, want nil", err)
			}
			got, err := os.ReadFile(filepath.Join(directoryPath, "ownership.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(test.collision) {
				t.Fatalf("collision marker changed: got %q, want %q", got, test.collision)
			}
			if got, err := os.ReadFile(stalePath); err != nil || string(got) != "preserve" {
				t.Fatalf("unexplained stale temporary changed: got %q, error %v", got, err)
			}
		})
	}
}

func TestOwnershipMarkerDescriptorDoesNotFollowReplacedRuntimePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	originalRuntime := filepath.Join(root, ".camp", "runtime")
	if err := os.MkdirAll(originalRuntime, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(originalRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	movedCamp := filepath.Join(root, ".camp-original")
	if err := os.Rename(filepath.Join(root, ".camp"), movedCamp); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Mkdir(filepath.Join(outside, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".camp")); err != nil {
		t.Fatal(err)
	}
	marker := ownershipMarker{
		Token:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalPath: root,
		Device:        11,
		Inode:         29,
	}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	operations := ownershipMarkerOperations{
		write:   writeOwnershipMarker,
		sync:    (*os.File).Sync,
		publish: publishOwnershipMarkerNoReplace,
	}
	if err := createOwnershipMarker(directory, body, marker, operations); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "runtime", "ownership.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement runtime was modified: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(movedCamp, "runtime", "ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Fatalf("anchored marker = %q, want %q", got, body)
	}
}

func TestOwnershipMarkerCreationRejectsSymlinkedFinal(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	runtimeDirectory := filepath.Join(root, ".camp", "runtime")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-marker")
	original := []byte("preserve outside marker")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(runtimeDirectory, "ownership.json")); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ownership.MarkCreatedWithToken(root, token); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("symlink target changed: got %q, want %q", got, original)
	}
}

func TestOwnershipMarkerAnchorRejectsRootOrRuntimeReplacement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(t *testing.T, root string)
	}{
		{
			name: "root replacement",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Rename(root, root+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(root, ".camp", "runtime"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "runtime replacement",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				runtimeDirectory := filepath.Join(root, ".camp", "runtime")
				if err := os.Rename(runtimeDirectory, runtimeDirectory+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(runtimeDirectory, 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "session")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			_, _, device, inode, err := inspectRoot(root)
			if err != nil {
				t.Fatal(err)
			}
			runtimeDirectory, err := openOwnershipMarkerDirectory(root, device, inode, true, (*os.File).Sync)
			if err != nil {
				t.Fatal(err)
			}
			defer runtimeDirectory.Close()
			test.mutate(t, root)
			if err := verifyOwnershipMarkerDirectory(root, device, inode, runtimeDirectory); !errors.Is(err, ErrOwnershipMismatch) {
				t.Fatalf("verifyOwnershipMarkerDirectory() error = %v, want ErrOwnershipMismatch", err)
			}
		})
	}
}

func TestOwnershipQuarantinePreservesPostValidationReplacement(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(root)
	if err != nil {
		t.Fatal(err)
	}
	original := root + "-original"
	if err := os.Rename(root, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("preserve replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	operations := ownershipRemovalOperations{sync: (*os.File).Sync}
	if err := ownership.quarantineAndRemoveOwned(context.Background(), record, operations); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("quarantineAndRemoveOwned() error = %v, want ErrOwnershipMismatch", err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "preserve replacement" {
		t.Fatalf("replacement root changed: got %q, error %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(original, ".camp", "runtime", "ownership.json")); err != nil {
		t.Fatalf("original owned root changed: %v", err)
	}
	quarantines, err := filepath.Glob(filepath.Join(ownership.MaterializationRoot(), ".camp-remove-*.quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantines) != 0 {
		t.Fatalf("mismatched quarantine was not restored: %q", quarantines)
	}
}

func TestOwnershipRemovalRejectsDifferentDeviceEntry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	keep := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keep, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	if err := removeDirectoryContents(context.Background(), directory, uint64(stat.Dev)+1); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("removeDirectoryContents() error = %v, want ErrOwnershipMismatch", err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "preserve" {
		t.Fatalf("different-device entry changed: got %q, error %v", got, err)
	}
}

func TestOwnershipRemovalRejectsBindMountedEntry(t *testing.T) {
	root := t.TempDir()
	source, err := os.MkdirTemp("/dev/shm", "camp-ownership-mount-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(source)
	sourceKeep := filepath.Join(source, "keep.txt")
	if err := os.WriteFile(sourceKeep, []byte("preserve mount"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "mounted")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); errors.Is(err, unix.EPERM) {
		t.Skip("bind mounts are not permitted")
	} else if err != nil {
		t.Fatal(err)
	}
	defer unix.Unmount(target, unix.MNT_DETACH)
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &stat); err != nil {
		t.Fatal(err)
	}
	if err := removeDirectoryContents(context.Background(), directory, uint64(stat.Dev)); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("removeDirectoryContents() error = %v, want ErrOwnershipMismatch", err)
	}
	if got, err := os.ReadFile(sourceKeep); err != nil || string(got) != "preserve mount" {
		t.Fatalf("bind-mounted entry changed: got %q, error %v", got, err)
	}
}

func TestOwnershipRemovalPreservesUnexplainedQuarantine(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(root)
	if err != nil {
		t.Fatal(err)
	}
	unexplained := filepath.Join(ownership.MaterializationRoot(), ".camp-remove-unexplained.quarantine")
	if err := os.Mkdir(unexplained, 0o700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(unexplained, "keep.txt")
	if err := os.WriteFile(keep, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := ownership.RemoveOwned(context.Background(), record)
	if err != nil || !removed {
		t.Fatalf("RemoveOwned() = %v, %v, want removed", removed, err)
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "preserve" {
		t.Fatalf("unexplained quarantine changed: got %q, error %v", got, err)
	}
}

func TestOwnershipQuarantineRestoresAfterParentSyncFailure(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(root)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected parent fsync failure")
	syncCalls := 0
	operations := ownershipRemovalOperations{
		sync: func(directory *os.File) error {
			syncCalls++
			if syncCalls == 1 {
				return injected
			}
			return directory.Sync()
		},
	}
	if err := ownership.quarantineAndRemoveOwned(context.Background(), record, operations); !errors.Is(err, injected) {
		t.Fatalf("quarantineAndRemoveOwned() error = %v, want injected failure", err)
	}
	if syncCalls < 2 {
		t.Fatalf("sync calls = %d, want quarantine failure plus durable restore", syncCalls)
	}
	if err := ownership.Revalidate(record); err != nil {
		t.Fatalf("restored root did not revalidate: %v", err)
	}
	quarantine := filepath.Join(ownership.MaterializationRoot(), ".camp-remove-"+record.OwnershipMarker+".quarantine")
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine remained after restore: %v", err)
	}
}

func TestOwnershipRemovalConvergesAfterProcessCrash(t *testing.T) {
	if mode := os.Getenv("CAMP_OWNERSHIP_REMOVAL_CRASH_HELPER"); mode != "" {
		runOwnershipRemovalCrashHelper(t, mode)
		return
	}
	tests := []struct {
		mode     string
		exitCode int
	}{
		{mode: "after-quarantine", exitCode: 77},
		{mode: "during-cleanup", exitCode: 78},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			dataHome := filepath.Join(t.TempDir(), "data")
			ownership, err := NewOwnership(dataHome)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(ownership.MaterializationRoot(), "session-a")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			record, err := ownership.MarkCreated(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "payload.txt"), []byte("payload"), 0o600); err != nil {
				t.Fatal(err)
			}
			recordBody, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestOwnershipRemovalConvergesAfterProcessCrash$")
			command.Env = append(os.Environ(),
				"CAMP_OWNERSHIP_REMOVAL_CRASH_HELPER="+test.mode,
				"CAMP_OWNERSHIP_CRASH_DATA_HOME="+dataHome,
				"CAMP_OWNERSHIP_CRASH_RECORD="+string(recordBody),
			)
			err = command.Run()
			exitError, ok := err.(*exec.ExitError)
			if !ok || exitError.ExitCode() != test.exitCode {
				t.Fatalf("crash helper error = %v, want exit %d", err, test.exitCode)
			}
			quarantine := filepath.Join(ownership.MaterializationRoot(), ".camp-remove-"+record.OwnershipMarker+".quarantine")
			if _, err := os.Stat(quarantine); err != nil {
				t.Fatalf("durable quarantine missing after crash: %v", err)
			}
			removed, err := ownership.RemoveOwned(context.Background(), record)
			if err != nil || !removed {
				t.Fatalf("RemoveOwned(resume) = %v, %v, want removed", removed, err)
			}
			if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("canonical root exists after recovery: %v", err)
			}
			if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("quarantine exists after recovery: %v", err)
			}
		})
	}
}

func runOwnershipRemovalCrashHelper(t *testing.T, mode string) {
	var record domain.Materialization
	if err := json.Unmarshal([]byte(os.Getenv("CAMP_OWNERSHIP_CRASH_RECORD")), &record); err != nil {
		t.Fatal(err)
	}
	ownership, err := NewOwnership(os.Getenv("CAMP_OWNERSHIP_CRASH_DATA_HOME"))
	if err != nil {
		t.Fatal(err)
	}
	operations := ownershipRemovalOperations{sync: (*os.File).Sync}
	switch mode {
	case "after-quarantine":
		operations.afterQuarantine = func() { os.Exit(77) }
	case "during-cleanup":
		operations.afterEntryRemoved = func() { os.Exit(78) }
	default:
		t.Fatalf("unknown crash mode %q", mode)
	}
	if err := ownership.quarantineAndRemoveOwned(context.Background(), record, operations); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash helper returned without exiting")
}

func TestOwnershipMarkerConvergesAfterProcessCrash(t *testing.T) {
	if mode := os.Getenv("CAMP_OWNERSHIP_MARKER_CRASH_HELPER"); mode != "" {
		runOwnershipMarkerCrashHelper(t, mode)
		return
	}
	for _, mode := range []string{"before-publish", "after-publish"} {
		t.Run(mode, func(t *testing.T) {
			dataHome := filepath.Join(t.TempDir(), "data")
			ownership, err := NewOwnership(dataHome)
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(ownership.MaterializationRoot(), "session-a")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			command := exec.Command(os.Args[0], "-test.run=^TestOwnershipMarkerConvergesAfterProcessCrash$")
			command.Env = append(os.Environ(),
				"CAMP_OWNERSHIP_MARKER_CRASH_HELPER="+mode,
				"CAMP_OWNERSHIP_CRASH_DATA_HOME="+dataHome,
				"CAMP_OWNERSHIP_CRASH_ROOT="+root,
				"CAMP_OWNERSHIP_CRASH_TOKEN="+token,
			)
			err = command.Run()
			exitError, ok := err.(*exec.ExitError)
			if !ok || exitError.ExitCode() != 79 {
				t.Fatalf("crash helper error = %v, want exit 79", err)
			}
			record, err := ownership.MarkCreatedWithToken(root, token)
			if err != nil {
				t.Fatalf("MarkCreatedWithToken(resume) error = %v", err)
			}
			if err := ownership.Revalidate(record); err != nil {
				t.Fatalf("reconciled marker did not revalidate: %v", err)
			}
			temporary := filepath.Join(root, ".camp", "runtime", ownershipMarkerTemporaryName(token))
			if _, err := os.Lstat(temporary); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marker temporary remains after recovery: %v", err)
			}
		})
	}
}

func runOwnershipMarkerCrashHelper(t *testing.T, mode string) {
	root := os.Getenv("CAMP_OWNERSHIP_CRASH_ROOT")
	token := os.Getenv("CAMP_OWNERSHIP_CRASH_TOKEN")
	canonical, _, device, inode, err := inspectRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	runtimeDirectory, err := openOwnershipMarkerDirectory(canonical, device, inode, true, (*os.File).Sync)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeDirectory.Close()
	marker := ownershipMarker{Token: token, CanonicalPath: canonical, Device: device, Inode: inode}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	operations := ownershipMarkerOperations{
		write:   writeOwnershipMarker,
		sync:    (*os.File).Sync,
		publish: publishOwnershipMarkerNoReplace,
	}
	switch mode {
	case "before-publish":
		operations.beforePublish = func(int, string) error { os.Exit(79); return nil }
	case "after-publish":
		operations.afterPublish = func() { os.Exit(79) }
	default:
		t.Fatalf("unknown crash mode %q", mode)
	}
	if err := createOwnershipMarker(runtimeDirectory, body, marker, operations); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash helper returned without exiting")
}

func TestOwnershipMarkerReadRejectsFIFOWithoutBlocking(t *testing.T) {
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "session-a")
	runtimeDirectory := filepath.Join(root, ".camp", "runtime")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(runtimeDirectory, "ownership.json")
	if err := unix.Mkfifo(markerPath, 0o600); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	done := make(chan error, 1)
	go func() {
		_, err := ownership.MarkCreatedWithToken(root, token)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrOwnershipMismatch) {
			t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
		}
	case <-time.After(200 * time.Millisecond):
		writerFD, openErr := unix.Open(markerPath, unix.O_WRONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if openErr == nil {
			_ = unix.Close(writerFD)
		}
		<-done
		t.Fatal("MarkCreatedWithToken() blocked opening FIFO marker")
	}
}

func TestOwnershipMarkerPublishRejectsSubstitutedTemporaryName(t *testing.T) {
	t.Parallel()
	directoryPath := t.TempDir()
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	marker := ownershipMarker{
		Token:         "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CanonicalPath: "/materializations/session-a",
		Device:        11,
		Inode:         29,
	}
	body, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	replacement := []byte("unexplained replacement")
	replacedName := ".ownership-unexplained.tmp"
	operations := ownershipMarkerOperations{
		createTemporary: createNamedOwnershipMarkerTemporary,
		write:           writeOwnershipMarker,
		sync:            (*os.File).Sync,
		beforePublish: func(directoryFD int, temporaryName string) error {
			if temporaryName == "" {
				return errors.New("test requires named fallback temporary")
			}
			replacedName = temporaryName
			if err := unix.Unlinkat(directoryFD, temporaryName, 0); err != nil {
				return err
			}
			writeOwnershipMarkerAt(t, directoryFD, temporaryName, replacement)
			return nil
		},
		publish: publishOwnershipMarkerNoReplace,
	}
	if err := createOwnershipMarker(directory, body, marker, operations); err == nil {
		t.Fatal("createOwnershipMarker() error = nil, want fail-closed substitution error")
	}
	if _, err := os.Lstat(filepath.Join(directoryPath, "ownership.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("substituted temporary produced final marker: %v", err)
	}
	gotReplacement, err := os.ReadFile(filepath.Join(directoryPath, replacedName))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotReplacement) != string(replacement) {
		t.Fatalf("replacement temporary = %q, want preserved %q", gotReplacement, replacement)
	}
}

func writeOwnershipMarkerAt(t *testing.T, directoryFD int, name string, body []byte) {
	t.Helper()
	fd, err := unix.Openat(directoryFD, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	file := os.NewFile(uintptr(fd), name)
	if err := writeOwnershipMarker(file, body); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOwnershipNeverDeletesAdoptedRootAndDeletesOnlyMatchingCreatedRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}

	adopted := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(adopted, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(adopted, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	adoptedRecord, err := ownership.Adopt(adopted)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := ownership.RemoveOwned(ctx, adoptedRecord)
	if err != nil || removed {
		t.Fatalf("RemoveOwned(adopted) = %v, %v; want preserved", removed, err)
	}
	if _, err := os.Stat(filepath.Join(adopted, "keep.txt")); err != nil {
		t.Fatalf("adopted source was changed: %v", err)
	}

	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	createdRecord, err := ownership.MarkCreated(created)
	if err != nil {
		t.Fatal(err)
	}
	removed, err = ownership.RemoveOwned(ctx, createdRecord)
	if err != nil || !removed {
		t.Fatalf("RemoveOwned(created) = %v, %v; want removed", removed, err)
	}
	if _, err := os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created root still exists: %v", err)
	}
}

func TestOwnershipRevalidateAdoptedRejectsReplacedRootIdentity(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "adopted")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.Adopt(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ownership.Revalidate(record); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("Revalidate(replaced adopted root) error = %v, want ErrOwnershipMismatch", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("Revalidate mutated replacement root: info=%v error=%v", info, err)
	}
}

func TestOwnershipRevalidateCreatedAcceptsExactOwnedMarker(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(ownership.MaterializationRoot(), "brain", "main", "session-a")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ownership.Revalidate(record); err != nil {
		t.Fatalf("Revalidate(created) error = %v", err)
	}
}

func TestOwnershipFailsClosedOnMarkerOrIdentityMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(created)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(created, ".camp", "runtime", "ownership.json")
	if err := os.WriteFile(marker, []byte(`{"token":"attacker"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(ctx, record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned(marker mismatch) = %v, %v", removed, err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("mismatched root was deleted: %v", err)
	}

	if err := os.RemoveAll(created); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(ctx, record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned(inode mismatch) = %v, %v", removed, err)
	}
}

func TestOwnershipRejectsSymlinkedMarkerParents(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(created, ".camp")); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := ownership.MarkCreatedWithToken(created, token); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("MarkCreatedWithToken() error = %v, want ErrOwnershipMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "runtime", "ownership.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink target was modified: %v", err)
	}
}

func TestOwnershipRemovalRejectsSymlinkedMarkerParents(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	token := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	record, err := ownership.MarkCreatedWithToken(created, token)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(created, ".camp", "runtime", "ownership.json"))
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(created, ".camp")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(created, ".camp")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(outside, "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "runtime", "ownership.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(context.Background(), record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned() = %v, %v, want ownership mismatch", removed, err)
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("mismatched root was removed: %v", err)
	}
}

func TestOwnershipRemovalRejectsSymlinkedMarkerFile(t *testing.T) {
	t.Parallel()
	dataHome := filepath.Join(t.TempDir(), "data")
	ownership, err := NewOwnership(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(dataHome, "camp", "materializations", "session-a")
	if err := os.MkdirAll(created, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := ownership.MarkCreated(created)
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(created, ".camp", "runtime", "ownership.json")
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "ownership.json")
	if err := os.WriteFile(outside, marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, markerPath); err != nil {
		t.Fatal(err)
	}
	if removed, err := ownership.RemoveOwned(context.Background(), record); removed || !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("RemoveOwned() = %v, %v, want ownership mismatch", removed, err)
	}
}

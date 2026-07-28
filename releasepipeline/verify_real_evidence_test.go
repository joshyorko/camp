package releasepipeline_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestVerifyRealEvidenceCleansOnlyItsReceiptOnTermination(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	for _, signal := range []struct {
		name   string
		signal syscall.Signal
	}{
		{name: "return"},
		{name: "interrupt", signal: syscall.SIGINT},
		{name: "terminate", signal: syscall.SIGTERM},
	} {
		t.Run(signal.name, func(t *testing.T) {
			workspace := t.TempDir()
			receipts := filepath.Join(workspace, "receipts")
			if err := os.Mkdir(receipts, 0o700); err != nil {
				t.Fatal(err)
			}
			unowned := filepath.Join(receipts, "unowned-receipt")
			if err := os.WriteFile(unowned, []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(workspace, "build"), 0o700); err != nil {
				t.Fatal(err)
			}
			candidate := filepath.Join(workspace, "build", "camp")
			if err := os.WriteFile(candidate, []byte("#!/usr/bin/env bash\n"), 0o700); err != nil {
				t.Fatal(err)
			}

			bin := filepath.Join(workspace, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			goPath := filepath.Join(bin, "go")
			fakeGo := `#!/usr/bin/env bash
if [[ " $* " == *" -list "* ]]; then
  printf '%s\n' TestLocalLifecycleVertical TestLocalLifecycleCrashMatrix TestMinIOLifecycleVertical TestS3TwoWriterConflict TestMountedFileBackendParity
  exit 0
fi
printf '%s\n' '--- PASS: TestMountedFileBackendParity'
sleep 30
`
			if signal.signal == 0 {
				fakeGo = strings.Replace(fakeGo, "sleep 30", "", 1)
			}
			if err := os.WriteFile(goPath, []byte(fakeGo), 0o700); err != nil {
				t.Fatal(err)
			}

			command := exec.Command("bash", filepath.Join(root, "scripts", "verify-real-evidence.sh"), "file")
			command.Dir = workspace
			command.Env = append(os.Environ(),
				"CAMP_TEST_BINARY="+candidate,
				"TMPDIR="+receipts,
				"PATH="+bin+":"+os.Getenv("PATH"),
			)
			command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			if signal.signal == 0 {
				if err := command.Wait(); err != nil {
					t.Fatalf("verify-real-evidence failed after return: %v", err)
				}
			} else {
				waitForReceipt(t, receipts)
				if err := syscall.Kill(-command.Process.Pid, signal.signal); err != nil {
					t.Fatal(err)
				}
				if err := command.Wait(); err == nil {
					t.Fatal("verify-real-evidence succeeded after termination")
				}
			}

			entries, err := os.ReadDir(receipts)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Name() != filepath.Base(unowned) {
				t.Fatalf("receipts after termination = %v, want only %q", entries, filepath.Base(unowned))
			}
		})
	}
}

func waitForReceipt(t *testing.T, directory string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "tmp") || strings.HasPrefix(entry.Name(), "camp-real-evidence.") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a temporary receipt in %s", directory)
}

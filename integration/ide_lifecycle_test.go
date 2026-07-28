package integration

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExactCandidateIDECommandContract(t *testing.T) {
	if strings.TrimSpace(os.Getenv("CAMP_TEST_BINARY")) == "" {
		t.Skip("set CAMP_TEST_BINARY to the exact Camp candidate")
	}
	bin := candidateBinary(t)
	root := t.TempDir()
	environment := []string{
		"HOME=" + filepath.Join(root, "home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"PATH=",
	}

	help, stderr, exitCode := runExactIDECandidate(t, bin, root, environment, "attach", "--help")
	if exitCode != 0 || len(stderr) != 0 {
		t.Fatalf("attach --help exit=%d stderr=%q\n%s", exitCode, stderr, help)
	}
	for _, contract := range []string{
		"--ide string",
		"entry mode: none, vscode, vscode-insiders, or t3-code",
		"--insiders",
		"alias for --ide=vscode-insiders",
	} {
		if !bytes.Contains(help, []byte(contract)) {
			t.Fatalf("attach --help missing %q:\n%s", contract, help)
		}
	}

	conflict, conflictStderr, conflictExit := runExactIDECandidate(t, bin, root, environment,
		"--json", "attach", "nested", "--insiders", "--ide=vscode",
	)
	if conflictExit != 2 || len(conflictStderr) != 0 {
		t.Fatalf("conflicting IDE aliases exit=%d stderr=%q\n%s", conflictExit, conflictStderr, conflict)
	}
	assertCandidateFailureEnvelope(t, conflict, "usage", "--insiders conflicts with --ide unless --ide=vscode-insiders")

	accepted, acceptedStderr, acceptedExit := runExactIDECandidate(t, bin, root, environment,
		"--json", "attach", "nested", "--insiders", "--ide=vscode-insiders",
	)
	if acceptedExit != 1 || len(acceptedStderr) != 0 {
		t.Fatalf("matching IDE aliases exit=%d stderr=%q\n%s", acceptedExit, acceptedStderr, accepted)
	}
	assertCandidateFailureEnvelope(t, accepted, "command_failed", "")
}

func runExactIDECandidate(t *testing.T, binary, directory string, environment []string, arguments ...string) ([]byte, []byte, int) {
	t.Helper()
	command := exec.Command(binary, arguments...)
	command.Dir = directory
	command.Env = mergeCommandEnvironment(os.Environ(), environment)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("run exact candidate: %v", err)
	}
	return stdout.Bytes(), stderr.Bytes(), exit.ExitCode()
}

func assertCandidateFailureEnvelope(t *testing.T, body []byte, code, message string) {
	t.Helper()
	var envelope struct {
		SchemaVersion int    `json:"schemaVersion"`
		Kind          string `json:"kind"`
		Error         struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode candidate failure %q: %v", body, err)
	}
	if envelope.SchemaVersion != 1 || envelope.Kind != "error" || envelope.Error.Code != code {
		t.Fatalf("failure envelope = %#v", envelope)
	}
	if message != "" && envelope.Error.Message != message {
		t.Fatalf("failure message = %q, want %q", envelope.Error.Message, message)
	}
}

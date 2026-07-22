package integration

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type createdWorkspaceTracker struct {
	ids  []string
	seen map[string]struct{}
}

func newCreatedWorkspaceTracker() *createdWorkspaceTracker {
	return &createdWorkspaceTracker{seen: map[string]struct{}{}}
}

func (t *createdWorkspaceTracker) Track(id string) {
	if id == "" {
		return
	}
	if _, ok := t.seen[id]; ok {
		return
	}
	t.seen[id] = struct{}{}
	t.ids = append(t.ids, id)
}

func (t *createdWorkspaceTracker) DeleteAll(deleteWorkspace func(string) error) error {
	var result error
	for index := len(t.ids) - 1; index >= 0; index-- {
		if err := deleteWorkspace(t.ids[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("delete exact DevPod workspace %q: %w", t.ids[index], err))
		}
	}
	return result
}

func parseWorkspaceImageDigest(output []byte) (string, error) {
	value := strings.TrimSpace(string(output))
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return "", fmt.Errorf("workspace image digest is not one exact sha256: %q", value)
	}
	for _, character := range value[len("sha256:"):] {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHex {
			return "", fmt.Errorf("workspace image digest is not lowercase hexadecimal: %q", value)
		}
	}
	return value, nil
}

func TestCreatedWorkspaceCleanupDeletesOnlyTrackedExactIDs(t *testing.T) {
	t.Parallel()

	tracker := newCreatedWorkspaceTracker()
	tracker.Track("camp-session-a")
	tracker.Track("camp-session-b")
	tracker.Track("camp-session-a")
	tracker.Track("")

	var deleted []string
	tracker.DeleteAll(func(id string) error {
		deleted = append(deleted, id)
		return nil
	})
	if want := []string{"camp-session-b", "camp-session-a"}; !reflect.DeepEqual(deleted, want) {
		t.Fatalf("deleted workspace IDs = %#v, want %#v", deleted, want)
	}
}

func TestParseWorkspaceImageDigestRequiresOneExactSHA256(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("a", 64)
	got, err := parseWorkspaceImageDigest([]byte(digest + "\n"))
	if err != nil || got != digest {
		t.Fatalf("parseWorkspaceImageDigest() = %q, %v", got, err)
	}
	for _, invalid := range [][]byte{
		[]byte(""),
		[]byte("sha256:short\n"),
		[]byte("devpod noise\n" + digest + "\n"),
		[]byte(digest + "\n" + digest + "\n"),
	} {
		if got, err := parseWorkspaceImageDigest(invalid); err == nil {
			t.Fatalf("parseWorkspaceImageDigest(%q) = %q, want error", invalid, got)
		}
	}
}

func TestNamedImageReopenProofIsDigestQualifiedValidShell(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("b", 64)
	command := namedImageReopenProofCommand(digest)
	for _, required := range []string{
		`$CAMP_REGISTRY/camp-acceptance:named`,
		`image_id=$("$engine" image inspect`,
		`image rm -f`,
		`$CAMP_REGISTRY/camp-acceptance@$expected_digest`,
		`pull "$digest_reference"`,
		`json .RepoDigests`,
		`run --rm "$digest_reference"`,
	} {
		if !strings.Contains(command, required) {
			t.Fatalf("named image proof omitted %q: %s", required, command)
		}
	}
	if strings.Contains(command, "if image_id=") {
		t.Fatalf("named image proof may skip cache eviction when the restored tag is missing: %s", command)
	}
	if err := exec.Command("sh", "-n", "-c", command).Run(); err != nil {
		t.Fatalf("named image proof is invalid POSIX shell: %v\n%s", err, command)
	}
}

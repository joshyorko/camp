package capsule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/joshyorko/camp/internal/domain"
)

var ErrInvalidDevcontainer = errors.New("invalid devcontainer configuration")

const roomIPTablesCompatibility = `#!/bin/sh
set -eu

command_name=${0##*/}
case "${command_name}" in
iptables|iptables-save|iptables-restore)
	if /usr/bin/iptables-legacy -t nat -L >/dev/null 2>&1; then
		backend=iptables-legacy
	else
		backend=iptables-nft
	fi
	;;
ip6tables|ip6tables-save|ip6tables-restore)
	if /usr/bin/ip6tables-legacy -t nat -L >/dev/null 2>&1; then
		backend=ip6tables-legacy
	else
		backend=ip6tables-nft
	fi
	;;
*)
	echo "unsupported iptables compatibility command: ${command_name}" >&2
	exit 2
	;;
esac

case "${command_name}" in
*-save) suffix=-save ;;
*-restore) suffix=-restore ;;
*) suffix= ;;
esac

exec "/usr/bin/${backend}${suffix}" "$@"
`

type Devcontainer struct {
	Path      string
	Generated bool
}

func ResolveDevcontainer(root, explicit string, lock domain.CapsuleLock) (Devcontainer, error) {
	canonicalRoot, _, _, _, _, err := inspectRoot(root)
	if err != nil {
		return Devcontainer{}, err
	}
	if explicit != "" {
		path := explicit
		if !filepath.IsAbs(path) {
			path = filepath.Join(canonicalRoot, path)
		}
		validated, err := validateDevcontainerPath(canonicalRoot, path)
		if err != nil {
			return Devcontainer{}, err
		}
		return Devcontainer{Path: validated}, nil
	}
	standard := []string{
		filepath.Join(canonicalRoot, ".devcontainer", "devcontainer.json"),
		filepath.Join(canonicalRoot, ".devcontainer.json"),
	}
	var existing []string
	for _, path := range standard {
		if _, err := os.Lstat(path); err == nil {
			existing = append(existing, path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return Devcontainer{}, err
		}
	}
	if len(existing) > 1 {
		return Devcontainer{}, fmt.Errorf("multiple root devcontainer configurations: %w", ErrInvalidDevcontainer)
	}
	if len(existing) == 1 {
		validated, err := validateDevcontainerPath(canonicalRoot, existing[0])
		if err != nil {
			return Devcontainer{}, err
		}
		return Devcontainer{Path: validated}, nil
	}
	if lock.Room.Image == "" || !digestPattern.MatchString(lock.Room.Digest) {
		return Devcontainer{}, fmt.Errorf("fallback Room image is not digest locked: %w", ErrInvalidDevcontainer)
	}
	runtimeDirectory := filepath.Join(canonicalRoot, ".camp", "runtime")
	if err := os.MkdirAll(runtimeDirectory, 0o700); err != nil {
		return Devcontainer{}, err
	}
	compatibilityPath := filepath.Join(runtimeDirectory, "iptables-compat")
	if err := writeExecutableStable(compatibilityPath, []byte(roomIPTablesCompatibility)); err != nil {
		return Devcontainer{}, err
	}
	mounts := make([]string, 0, 6)
	for _, executable := range []string{"iptables", "iptables-save", "iptables-restore", "ip6tables", "ip6tables-save", "ip6tables-restore"} {
		mounts = append(mounts, "source=${localWorkspaceFolder}/.camp/runtime/iptables-compat,target=/usr/local/sbin/"+executable+",type=bind,readonly")
	}
	path := filepath.Join(runtimeDirectory, "devcontainer.json")
	document := map[string]any{
		"name":          "Camp Room of Requirement",
		"image":         lock.Room.Image + "@" + lock.Room.Digest,
		"mounts":        mounts,
		"runArgs":       []string{"--privileged"},
		"containerUser": "root",
		"remoteUser":    "vscode",
	}
	body, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return Devcontainer{}, err
	}
	body = append(body, '\n')
	if err := writeStable(path, body); err != nil {
		return Devcontainer{}, err
	}
	return Devcontainer{Path: path, Generated: true}, nil
}

func writeExecutableStable(path string, body []byte) error {
	if err := writeStable(path, body); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("stable executable %q is not a regular file: %w", path, ErrInitializationConflict)
	}
	return os.Chmod(path, 0o755)
}

func validateDevcontainerPath(root, path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("devcontainer path is not a regular file: %w", ErrInvalidDevcontainer)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil || !contained(root, canonical) {
		return "", fmt.Errorf("devcontainer path escapes capsule root: %w", ErrInvalidDevcontainer)
	}
	body, err := os.ReadFile(canonical)
	if err != nil {
		return "", err
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return "", fmt.Errorf("decode devcontainer %q: %v: %w", canonical, err, ErrInvalidDevcontainer)
	}
	if _, ok := document["image"]; !ok {
		if _, dockerfile := document["build"]; !dockerfile {
			return "", fmt.Errorf("devcontainer has neither image nor build: %w", ErrInvalidDevcontainer)
		}
	}
	return canonical, nil
}

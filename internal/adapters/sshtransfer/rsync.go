package sshtransfer

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var workspaceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

const (
	buildExclusion   = "/.camp/build/***"
	runtimeExclusion = "/.camp/runtime/***"
)

type RsyncMirrorSpec struct {
	Executable  string
	WorkspaceID string
	RemoteRoot  string
	LocalRoot   string
}

func BuildRsyncMirror(spec RsyncMirrorSpec) (ports.Command, error) {
	if spec.Executable == "" {
		return ports.Command{}, errors.New("rsync executable is required")
	}
	host, err := DevPodHost(spec.WorkspaceID)
	if err != nil {
		return ports.Command{}, err
	}
	if !validAbsoluteRoot(spec.RemoteRoot) || !validAbsoluteRoot(spec.LocalRoot) {
		return ports.Command{}, errors.New("rsync roots must be clean absolute paths")
	}
	return ports.Command{
		Executable: spec.Executable,
		Argv: []string{
			"--archive",
			"--hard-links",
			"--delete",
			"--protect-args",
			"--exclude=" + buildExclusion,
			"--exclude=" + runtimeExclusion,
			"--",
			host + ":" + directoryPath(spec.RemoteRoot),
			directoryPath(spec.LocalRoot),
		},
	}, nil
}

func DevPodHost(workspaceID string) (string, error) {
	if !workspaceIDPattern.MatchString(workspaceID) {
		return "", errors.New("DevPod workspace ID is invalid")
	}
	return workspaceID + ".devpod", nil
}

func validAbsoluteRoot(root string) bool {
	return root != "" && filepath.IsAbs(root) && filepath.Clean(root) == root && !strings.ContainsAny(root, "\r\n\x00")
}

func directoryPath(root string) string {
	if root == "/" {
		return root
	}
	return root + "/"
}

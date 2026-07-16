package sshtransfer

import (
	"errors"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

type TarPipeSpec struct {
	SSHExecutable string
	TarExecutable string
	WorkspaceID   string
	RemoteRoot    string
	LocalRoot     string
}

// TarPipe describes two directly connected processes. Callers must connect the
// producer's stdout to the consumer's stdin; no command shell is involved.
type TarPipe struct {
	Producer ports.Command
	Consumer ports.Command
}

func BuildTarPipe(spec TarPipeSpec) (TarPipe, error) {
	if spec.SSHExecutable == "" || spec.TarExecutable == "" {
		return TarPipe{}, errors.New("ssh and tar executables are required")
	}
	host, err := DevPodHost(spec.WorkspaceID)
	if err != nil {
		return TarPipe{}, err
	}
	if !validAbsoluteRoot(spec.RemoteRoot) || !validAbsoluteRoot(spec.LocalRoot) {
		return TarPipe{}, errors.New("tar roots must be clean absolute paths")
	}
	return TarPipe{
		Producer: ports.Command{
			Executable: spec.SSHExecutable,
			Argv: []string{
				host,
				"tar --create --file=- --directory=" + remoteShellQuote(spec.RemoteRoot) +
					" --exclude='./.camp/build' --exclude='./.camp/runtime' .",
			},
		},
		Consumer: ports.Command{
			Executable: spec.TarExecutable,
			Argv: []string{
				"--extract", "--file=-", "--directory=" + spec.LocalRoot,
				"--same-permissions", "--delay-directory-restore",
			},
		},
	}, nil
}

func remoteShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

package devpod

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrDevPodPassthroughDenied   = errors.New("DevPod passthrough command is denied")
	ErrDevPodPassthroughConflict = errors.New("DevPod passthrough conflicts with Camp-owned state")
	ErrDevPodPassthroughInvalid  = errors.New("DevPod passthrough argv is invalid")
	ErrDevPodPassthroughUnknown  = errors.New("DevPod passthrough command is unknown")
)

var devPodDeniedCommands = map[string]struct{}{
	"build": {}, "context": {}, "delete": {}, "ide": {}, "list": {}, "machine": {},
	"provider": {}, "pro": {}, "purge": {}, "reset": {}, "ssh": {}, "status": {},
	"stop": {}, "up": {},
}

var devPodReservedFlags = map[string]struct{}{
	"--": {}, "--config": {}, "--context": {}, "--env": {}, "--home": {}, "--id": {},
	"--provider": {}, "--provider-option": {}, "--workspace": {}, "--workspace-env": {},
}

// Passthrough runs only effect-free DevPod identity/help commands. Camp-owned
// lifecycle, session, provider, configuration, and environment inputs have no
// raw passthrough path.
func (c *Client) Passthrough(ctx context.Context, argv []string) (ports.Result, error) {
	if err := validateDevPodPassthrough(argv); err != nil {
		return ports.Result{}, err
	}
	return c.run(ctx, append([]string(nil), argv...))
}

func validateDevPodPassthrough(argv []string) error {
	if len(argv) == 0 {
		return ErrDevPodPassthroughInvalid
	}
	for _, argument := range argv {
		if argument == "" || unsafeArgument(argument) {
			return ErrDevPodPassthroughInvalid
		}
		name := argument
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		if _, reserved := devPodReservedFlags[name]; reserved {
			return fmt.Errorf("%w: %s", ErrDevPodPassthroughConflict, name)
		}
	}
	if _, denied := devPodDeniedCommands[argv[0]]; denied {
		return fmt.Errorf("%w: %s", ErrDevPodPassthroughDenied, argv[0])
	}
	if len(argv) == 1 && (argv[0] == "version" || argv[0] == "help" || argv[0] == "--help") {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrDevPodPassthroughUnknown, argv[0])
}

package hauler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var (
	ErrHaulerPassthroughDenied   = errors.New("Hauler passthrough command is denied")
	ErrHaulerPassthroughConflict = errors.New("Hauler passthrough conflicts with Camp-owned state")
	ErrHaulerPassthroughInvalid  = errors.New("Hauler passthrough argv is invalid")
	ErrHaulerPassthroughUnknown  = errors.New("Hauler passthrough command is unknown")
)

var haulerDeniedCommands = map[string]struct{}{
	"completion": {}, "login": {}, "store": {},
}

var haulerReservedFlags = map[string]struct{}{
	"--": {}, "--config": {}, "--directory": {}, "--env": {}, "--home": {},
	"--platform": {}, "--registry": {}, "--store": {}, "--temp-dir": {},
}

// Passthrough runs only effect-free Hauler identity/help commands. Camp-owned
// stores, services, configuration, and environment inputs have no raw path.
func (c *Client) Passthrough(ctx context.Context, argv []string) (ports.Result, error) {
	if err := validateHaulerPassthrough(argv); err != nil {
		return ports.Result{}, err
	}
	return c.run(ctx, append([]string(nil), argv...))
}

func validateHaulerPassthrough(argv []string) error {
	if len(argv) == 0 {
		return ErrHaulerPassthroughInvalid
	}
	for _, argument := range argv {
		if argument == "" || strings.ContainsAny(argument, "\x00\r\n") {
			return ErrHaulerPassthroughInvalid
		}
		name := argument
		if index := strings.IndexByte(name, '='); index >= 0 {
			name = name[:index]
		}
		if _, reserved := haulerReservedFlags[name]; reserved {
			return fmt.Errorf("%w: %s", ErrHaulerPassthroughConflict, name)
		}
	}
	if _, denied := haulerDeniedCommands[argv[0]]; denied {
		return fmt.Errorf("%w: %s", ErrHaulerPassthroughDenied, argv[0])
	}
	if len(argv) == 1 && (argv[0] == "version" || argv[0] == "help" || argv[0] == "--help") {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrHaulerPassthroughUnknown, argv[0])
}

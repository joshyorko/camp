package supervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

type ConfinementCapability = ports.ConfinementCapability

type ConfinementUnavailable struct {
	Reason   string
	Package  string
	Boundary string
	Recovery string
	Cause    error
}

func (e *ConfinementUnavailable) Error() string {
	return fmt.Sprintf("pasta confinement unavailable (%s) at %s; external package %s is required; %s", e.Reason, e.Boundary, e.Package, e.Recovery)
}

func (e *ConfinementUnavailable) Unwrap() error { return e.Cause }

type ConfinementResolver struct {
	runner   ports.Runner
	lookup   func(string) (string, error)
	boundary func() string
}

func NewConfinementResolver(runner ports.Runner, lookup func(string) (string, error), boundary func() string) *ConfinementResolver {
	return &ConfinementResolver{runner: runner, lookup: lookup, boundary: boundary}
}

func (r *ConfinementResolver) Resolve(ctx context.Context) (ports.ConfinementCapability, error) {
	boundary := "unknown-host"
	if r.boundary != nil && r.boundary() != "" {
		boundary = r.boundary()
	}
	if r.runner == nil || r.lookup == nil {
		return ports.ConfinementCapability{}, unavailable("resolver-unconfigured", boundary, errors.New("confinement resolver is unconfigured"))
	}
	executable, err := r.lookup("pasta")
	if err != nil {
		return ports.ConfinementCapability{}, unavailable("missing-executable", boundary, err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil || !filepath.IsAbs(executable) {
		return ports.ConfinementCapability{}, unavailable("invalid-executable", boundary, err)
	}
	info, err := os.Stat(executable)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return ports.ConfinementCapability{}, unavailable("unusable-executable", boundary, err)
	}
	versionResult, err := r.runner.Run(ctx, ports.Command{Executable: executable, Argv: []string{"--version"}})
	if err != nil || versionResult.ExitCode != 0 {
		return ports.ConfinementCapability{}, unavailable("version-probe-failed", boundary, err)
	}
	version := strings.TrimSpace(string(versionResult.Stdout))
	if version == "" {
		version = strings.TrimSpace(string(versionResult.Stderr))
	}
	help, err := r.runner.Run(ctx, ports.Command{Executable: executable, Argv: []string{"--help"}})
	if err != nil || help.ExitCode != 0 {
		return ports.ConfinementCapability{}, unavailable("help-probe-failed", boundary, err)
	}
	surface := string(help.Stdout) + "\n" + string(help.Stderr)
	for _, option := range []string{"--foreground", "--quiet", "--log-file", "--pid", "--ipv4-only", "--host-lo-to-ns-lo", "--tcp-ports", "--udp-ports", "--tcp-ns", "--udp-ns"} {
		if !strings.Contains(surface, option) {
			return ports.ConfinementCapability{}, unavailable("incompatible-options", boundary, fmt.Errorf("pasta help lacks %s", option))
		}
	}
	childContextPrefix, err := r.childContextPrefix()
	if err != nil {
		return ports.ConfinementCapability{}, unavailable("selinux-child-context", boundary, err)
	}
	fingerprintSource := strings.Join(append([]string{executable, version, boundary, runtime.GOOS, runtime.GOARCH, strconv.Itoa(os.Getuid())}, childContextPrefix...), "\x00")
	digest := sha256.Sum256([]byte(fingerprintSource))
	return ports.ConfinementCapability{
		Executable:             executable,
		Version:                version,
		EnvironmentFingerprint: hex.EncodeToString(digest[:]),
		Boundary:               boundary,
		ChildContextPrefix:     childContextPrefix,
	}, nil
}

func (r *ConfinementResolver) childContextPrefix() ([]string, error) {
	enforcing, err := os.ReadFile("/sys/fs/selinux/enforce")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read SELinux enforcement state: %w", err)
	}
	if strings.TrimSpace(string(enforcing)) != "1" {
		return nil, nil
	}
	runcon, err := r.lookup("runcon")
	if err != nil {
		return nil, fmt.Errorf("SELinux enforcing but runcon is unavailable: %w", err)
	}
	runcon, err = filepath.Abs(runcon)
	if err != nil || !filepath.IsAbs(runcon) {
		return nil, fmt.Errorf("resolve runcon: %w", err)
	}
	info, err := os.Stat(runcon)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		return nil, fmt.Errorf("validate runcon: %w", err)
	}
	return []string{runcon, "-t", "unconfined_t"}, nil
}

func unavailable(reason, boundary string, cause error) *ConfinementUnavailable {
	return &ConfinementUnavailable{
		Reason:   reason,
		Package:  "passt",
		Boundary: boundary,
		Recovery: "install or repair pasta from passt outside Camp, then rerun the command; Camp does not mutate the host",
		Cause:    cause,
	}
}

package supervisor

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type confinementRunner struct {
	commands []ports.Command
	result   ports.Result
	err      error
}

func (r *confinementRunner) Run(_ context.Context, command ports.Command) (ports.Result, error) {
	r.commands = append(r.commands, command)
	if len(command.Argv) == 1 && command.Argv[0] == "--version" {
		return ports.Result{Stdout: []byte("pasta 2026_05_26\n")}, nil
	}
	return r.result, r.err
}

func TestConfinementResolverFailsClosedWithRecoveryGuidance(t *testing.T) {
	t.Parallel()
	runner := &confinementRunner{}
	resolver := NewConfinementResolver(runner, func(string) (string, error) {
		return "", errors.New("not found")
	}, func() string { return "bluefin-host" })
	_, err := resolver.Resolve(context.Background())
	var unavailable *ConfinementUnavailable
	if !errors.As(err, &unavailable) {
		t.Fatalf("Resolve() error = %T %v, want ConfinementUnavailable", err, err)
	}
	if unavailable.Package != "passt" || unavailable.Boundary != "bluefin-host" || unavailable.Recovery == "" {
		t.Fatalf("typed error = %#v", unavailable)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("runner invoked after missing executable: %#v", runner.commands)
	}
}

func TestConfinementResolverRejectsIncompatiblePasta(t *testing.T) {
	t.Parallel()
	runner := &confinementRunner{result: ports.Result{Stdout: []byte("--foreground --quiet --ipv4-only")}}
	resolver := NewConfinementResolver(runner, func(string) (string, error) { return "/usr/bin/pasta", nil }, func() string { return "host" })
	_, err := resolver.Resolve(context.Background())
	var unavailable *ConfinementUnavailable
	if !errors.As(err, &unavailable) || unavailable.Reason != "incompatible-options" {
		t.Fatalf("Resolve() error = %#v, want incompatible ConfinementUnavailable", err)
	}
}

func TestPastaLoopbackBuildsOnlyAcceptedStructuredArgv(t *testing.T) {
	t.Parallel()
	child := ports.Command{
		Executable:  "/opt/hauler",
		Argv:        []string{"store", "--store", "/state/store", "serve", "registry", "--directory", "/state/registry", "--port", "5100", "--readonly=false"},
		Environment: map[string]string{"HOME": "/state/home", "TMPDIR": "/state/tmp"},
	}
	spec, err := BuildPastaLoopback(PastaLoopback{
		Capability: ConfinementCapability{Executable: "/usr/bin/pasta", Version: "pasta 2026_05_26", EnvironmentFingerprint: "env-sha"},
		Mapping:    PortMapping{HostAddress: "127.0.0.1", HostPort: 5000, GuestPort: 5100},
		LogPath:    "/state/private/pasta.log",
		PIDPath:    "/state/private/pasta.pid",
		Child:      child,
	})
	if err != nil {
		t.Fatalf("BuildPastaLoopback() error = %v", err)
	}
	want := []string{
		"--foreground", "--quiet", "--log-file", "/state/private/pasta.log", "--pid", "/state/private/pasta.pid",
		"--ipv4-only", "--host-lo-to-ns-lo", "--tcp-ports", "127.0.0.1/5000:5100",
		"--udp-ports", "none", "--tcp-ns", "none", "--udp-ns", "none", "--",
		"/opt/hauler", "store", "--store", "/state/store", "serve", "registry", "--directory", "/state/registry", "--port", "5100", "--readonly=false",
	}
	if spec.Command.Executable != "/usr/bin/pasta" || !reflect.DeepEqual(spec.Command.Argv, want) {
		t.Fatalf("process command = %#v, want executable /usr/bin/pasta argv %#v", spec.Command, want)
	}
	if !spec.NewSession || spec.LogPath != "/state/private/pasta.log" || !reflect.DeepEqual(spec.Command.Environment, child.Environment) {
		t.Fatalf("process spec = %#v", spec)
	}
}

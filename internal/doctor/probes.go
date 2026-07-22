package doctor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/ports"
)

const maxToolBytes = int64(512 << 20)

var (
	identityAssignmentSecret = regexp.MustCompile(`(?i)\b(token|password|secret|credential|accesskey)=([^\s"']+)`)
	identityURLCredentials   = regexp.MustCompile(`://[^/@\s"']+@`)
	preferredVersionLine     = regexp.MustCompile(`(?i)^(gitversion|version)\s*:`)
)

type Probe interface {
	Capability() string
	Probe(context.Context) Result
}

type Runner struct {
	Probes  []Probe
	Timeout time.Duration
}

func (r Runner) Run(ctx context.Context) Report {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	results := make([]Result, 0, len(r.Probes))
	for _, probe := range r.Probes {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		result := probe.Probe(probeCtx)
		probeErr := probeCtx.Err()
		cancel()
		if errors.Is(probeErr, context.DeadlineExceeded) {
			result = Result{Capability: probe.Capability(), Status: StatusBlocked, Code: "probe_timeout", Summary: "capability probe timed out", Remediation: "repair the capability or increase host responsiveness, then rerun camp doctor"}
		} else if errors.Is(probeErr, context.Canceled) {
			result = Result{Capability: probe.Capability(), Status: StatusBlocked, Code: "probe_cancelled", Summary: "capability probe was cancelled", Remediation: "rerun camp doctor"}
		}
		results = append(results, result)
	}
	return NewReport(results)
}

type ToolProbe struct {
	Name      string
	Arguments []string
	LookPath  func(string) (string, error)
	Run       func(context.Context, string, ...string) ([]byte, error)
}

func (p ToolProbe) Capability() string { return p.Name }

func (p ToolProbe) Probe(ctx context.Context) Result {
	lookup := p.LookPath
	if lookup == nil {
		lookup = exec.LookPath
	}
	path, err := lookup(p.Name)
	if err != nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "tool_missing", Summary: p.Name + " is unavailable on PATH", Remediation: "install or repair " + p.Name + " outside Camp, then rerun camp doctor"}
	}
	path, err = canonicalExecutable(path)
	if err != nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "tool_unusable", Summary: p.Name + " executable identity cannot be read", Remediation: "repair the executable, then rerun camp doctor"}
	}
	digest, err := hashExecutable(path)
	if err != nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "tool_unusable", Summary: p.Name + " executable identity cannot be read", Remediation: "repair the executable, then rerun camp doctor"}
	}
	run := p.Run
	if run == nil {
		run = func(ctx context.Context, path string, arguments ...string) ([]byte, error) {
			return exec.CommandContext(ctx, path, arguments...).CombinedOutput()
		}
	}
	output, err := run(ctx, path, p.Arguments...)
	if err != nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "tool_identity_command_failed", Summary: p.Name + " identity command failed", Evidence: map[string]string{"path": path, "sha256": digest}, Remediation: "repair the executable, then rerun camp doctor"}
	}
	version := safeSingleLine(string(output))
	if version == "" {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "tool_identity_empty", Summary: p.Name + " identity command returned no identity", Evidence: map[string]string{"path": path, "sha256": digest}, Remediation: "repair the executable, then rerun camp doctor"}
	}
	return Result{Capability: p.Name, Status: StatusDegraded, Code: "tool_identity_observed", Summary: p.Name + " executable identity is observed but not lock-verified", Evidence: map[string]string{"path": path, "sha256": digest, "version": version}, Remediation: "use issue #10 managed tool resolution to verify the executable against tools.lock.yaml"}
}

func canonicalExecutable(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Size() > maxToolBytes {
		return "", errors.New("invalid executable shape")
	}
	return canonical, nil
}

func hashExecutable(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxToolBytes+1))
	if err != nil || written > maxToolBytes {
		return "", errors.New("executable exceeds identity bound")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type confinementResolver interface {
	Resolve(context.Context) (ports.ConfinementCapability, error)
}

type ConfinementProbe struct{ Resolver confinementResolver }

func (ConfinementProbe) Capability() string { return "pasta" }

func (p ConfinementProbe) Probe(ctx context.Context) Result {
	if p.Resolver == nil {
		return Result{Capability: "pasta", Status: StatusBlocked, Code: "pasta_probe_unconfigured", Summary: "pasta confinement probe is unavailable", Remediation: "repair Camp composition, then rerun camp doctor"}
	}
	capability, err := p.Resolver.Resolve(ctx)
	if err != nil {
		return Result{Capability: "pasta", Status: StatusBlocked, Code: "pasta_confinement_unavailable", Summary: "pasta cannot provide Camp's required confinement capability", Remediation: "install or repair pasta from passt outside Camp, then rerun camp doctor"}
	}
	return Result{Capability: "pasta", Status: StatusHealthy, Code: "pasta_confinement_available", Summary: "pasta provides the required confinement option surface and host policy context", Evidence: map[string]string{"path": capability.Executable, "version": safeSingleLine(capability.Version), "boundary": capability.Boundary, "fingerprint": capability.EnvironmentFingerprint}}
}

type BackendProbe struct {
	ConfigPath       string
	Environment      map[string]string
	DefaultBackend   string
	CheckCredentials func(context.Context, config.Backend) error
}

func (BackendProbe) Capability() string { return "backend" }

func (p BackendProbe) Probe(ctx context.Context) Result {
	bootstrap, err := config.ResolveBootstrap(config.BootstrapInput{ConfigPath: p.ConfigPath, Environment: p.Environment})
	if err != nil {
		return Result{Capability: "backend", Status: StatusBlocked, Code: "backend_configuration_invalid", Summary: "backend configuration is invalid", Remediation: "repair Camp's non-secret backend configuration, then rerun camp doctor"}
	}
	if bootstrap.Backend == "" {
		bootstrap.Backend = p.DefaultBackend
	}
	if bootstrap.Backend == "" {
		return Result{Capability: "backend", Status: StatusSkippedNotConfigured, Code: "backend_not_configured", Summary: "backend is not configured", Remediation: "set CAMP_BACKEND or configure backend in Camp's config file"}
	}
	backend, err := config.ResolveBackend(bootstrap.Backend, bootstrap.S3)
	if err != nil {
		return Result{Capability: "backend", Status: StatusBlocked, Code: "backend_configuration_invalid", Summary: "backend configuration is invalid", Remediation: "repair Camp's non-secret backend configuration, then rerun camp doctor"}
	}
	evidence := map[string]string{"kind": string(backend.Kind), "url": backend.SanitizedURL, "fingerprint": backend.Fingerprint}
	if backend.Kind == config.BackendFile {
		return Result{Capability: "backend", Status: StatusHealthy, Code: "backend_configuration_valid", Summary: "file backend configuration is valid; backend I/O health was not probed", Evidence: evidence}
	}
	if p.CheckCredentials == nil {
		return Result{Capability: "backend", Status: StatusBlocked, Code: "s3_credential_probe_unconfigured", Summary: "S3 credential-chain probe is unavailable", Evidence: evidence, Remediation: "repair Camp composition, then rerun camp doctor"}
	}
	if err := p.CheckCredentials(ctx, backend); err != nil {
		return Result{Capability: "backend", Status: StatusBlocked, Code: "s3_credentials_unavailable", Summary: "S3 host credential chain did not resolve", Evidence: evidence, Remediation: "configure the host AWS credential chain, then rerun camp doctor"}
	}
	return Result{Capability: "backend", Status: StatusDegraded, Code: "s3_credentials_available_backend_unprobed", Summary: "S3 configuration is valid and the host credential chain resolved; backend I/O health was not probed", Evidence: evidence, Remediation: "run a later issue #11 backend functional probe before relying on backend health"}
}

func safeSingleLine(value string) string {
	line := ""
	for _, candidate := range strings.Split(value, "\n") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if line == "" {
			line = candidate
		}
		if preferredVersionLine.MatchString(candidate) {
			line = candidate
			break
		}
	}
	if len(line) > 256 {
		line = line[:256]
	}
	line = identityURLCredentials.ReplaceAllString(line, "://[REDACTED]@")
	line = identityAssignmentSecret.ReplaceAllString(line, "$1=[REDACTED]")
	return strings.TrimSpace(line)
}

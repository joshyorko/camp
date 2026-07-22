package doctor

import (
	"context"
	"strconv"
)

type ManagedToolIdentity struct {
	Path         string
	Repository   string
	Version      string
	BinarySHA256 string
	Managed      bool
}

type managedToolResolver interface {
	Inspect(context.Context, string) (ManagedToolIdentity, error)
}

type ManagedToolProbe struct {
	Name     string
	Resolver managedToolResolver
}

func (p ManagedToolProbe) Capability() string { return p.Name }

func (p ManagedToolProbe) Probe(ctx context.Context) Result {
	if p.Resolver == nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "managed_tool_probe_unconfigured", Summary: p.Name + " managed identity probe is unavailable", Remediation: "repair Camp composition, then rerun camp doctor"}
	}
	identity, err := p.Resolver.Inspect(ctx, p.Name)
	if err != nil || identity.Path == "" || identity.Repository == "" || identity.Version == "" || len(identity.BinarySHA256) != 64 {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: "managed_tool_identity_unverified", Summary: p.Name + " executable does not match its locked identity", Remediation: "run camp setup to install or select the locked tool, then rerun camp doctor"}
	}
	return Result{Capability: p.Name, Status: StatusHealthy, Code: "managed_tool_identity_verified", Summary: p.Name + " executable matches its locked identity", Evidence: map[string]string{
		"path": identity.Path, "repository": identity.Repository, "version": identity.Version,
		"sha256": identity.BinarySHA256, "managed": strconv.FormatBool(identity.Managed),
	}}
}

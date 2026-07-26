package doctor

import (
	"context"
	"strings"
)

type ConfiguredProbe struct {
	Name        string
	Configured  bool
	Remediation string
	Check       func(context.Context) (map[string]string, error)
}

func (p ConfiguredProbe) Capability() string { return p.Name }

func (p ConfiguredProbe) Probe(ctx context.Context) Result {
	codeName := strings.ReplaceAll(p.Name, "-", "_")
	if !p.Configured {
		return Result{Capability: p.Name, Status: StatusSkippedNotConfigured, Code: codeName + "_not_configured", Summary: p.Name + " is not configured", Remediation: "configure " + p.Name + " when this capability is required"}
	}
	if p.Check == nil {
		return Result{Capability: p.Name, Status: StatusBlocked, Code: codeName + "_probe_unconfigured", Summary: p.Name + " reachability probe is unavailable", Remediation: "repair Camp composition, then rerun camp doctor"}
	}
	evidence, err := p.Check(ctx)
	if err != nil {
		remediation := p.Remediation
		if remediation == "" {
			remediation = "repair the configured " + p.Name + " capability, then rerun camp doctor"
		}
		return Result{Capability: p.Name, Status: StatusBlocked, Code: codeName + "_unreachable", Summary: p.Name + " functional reachability failed", Remediation: remediation}
	}
	return Result{Capability: p.Name, Status: StatusHealthy, Code: codeName + "_reachable", Summary: p.Name + " functional reachability is verified", Evidence: evidence}
}

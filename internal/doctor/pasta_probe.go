package doctor

import (
	"context"
	"time"
)

type PastaInstance struct {
	HelperIdentity string
	ChildIdentity  string
	HostEndpoint   string
	HostNetNS      string
	ChildNetNS     string
}

type PastaRuntime interface {
	Start(context.Context) (PastaInstance, error)
	Reach(context.Context, PastaInstance) error
	Stop(context.Context, PastaInstance) error
	VerifyStopped(context.Context, PastaInstance) error
}

type PastaProbe struct{ Runtime PastaRuntime }

func (PastaProbe) Capability() string { return "pasta" }

func (p PastaProbe) Probe(ctx context.Context) (result Result) {
	if p.Runtime == nil {
		return pastaBlocked("pasta_runtime_probe_unconfigured", "pasta runtime probe is unavailable", "repair Camp composition, then rerun camp doctor")
	}
	instance, err := p.Runtime.Start(ctx)
	if err != nil {
		return pastaBlocked("pasta_runtime_start_failed", "pasta could not start a disposable namespace", "install or repair pasta and user namespace support, then rerun camp doctor")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		if err := p.Runtime.Stop(cleanupCtx, instance); err != nil {
			result = pastaBlocked("pasta_cleanup_failed", "pasta disposable runtime cleanup failed", "inspect the recorded process identity before stopping or removing it")
			return
		}
		if err := p.Runtime.VerifyStopped(cleanupCtx, instance); err != nil {
			result = pastaBlocked("pasta_cleanup_unverified", "pasta disposable runtime teardown could not be verified", "inspect the recorded process identity and loopback listener")
		}
	}()
	if instance.HelperIdentity == "" || instance.ChildIdentity == "" || instance.HostEndpoint == "" || instance.HostNetNS == "" || instance.ChildNetNS == "" {
		return pastaBlocked("pasta_runtime_identity_incomplete", "pasta runtime identity evidence is incomplete", "repair Camp process inspection, then rerun camp doctor")
	}
	if instance.HostNetNS == instance.ChildNetNS {
		return pastaBlocked("pasta_namespace_unconfined", "pasta child remained in the host network namespace", "repair user namespace and pasta configuration, then rerun camp doctor")
	}
	if err := p.Runtime.Reach(ctx, instance); err != nil {
		return pastaBlocked("pasta_listener_unreachable", "pasta loopback listener did not reach the disposable child", "repair pasta loopback mapping, then rerun camp doctor")
	}
	return Result{Capability: "pasta", Status: StatusHealthy, Code: "pasta_runtime_verified", Summary: "pasta namespace, loopback listener, and cleanup are verified", Evidence: map[string]string{
		"hostEndpoint": instance.HostEndpoint, "hostNetNS": instance.HostNetNS, "childNetNS": instance.ChildNetNS,
		"helperIdentity": instance.HelperIdentity, "childIdentity": instance.ChildIdentity,
	}}
}

func pastaBlocked(code, summary, remediation string) Result {
	return Result{Capability: "pasta", Status: StatusBlocked, Code: code, Summary: summary, Remediation: remediation}
}

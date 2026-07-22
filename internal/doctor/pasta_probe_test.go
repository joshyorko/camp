package doctor

import (
	"context"
	"errors"
	"testing"
)

type fakePastaRuntime struct {
	instance  PastaInstance
	startErr  error
	reachErr  error
	stopErr   error
	verifyErr error
	started   int
	stopped   int
	verified  int
}

func (r *fakePastaRuntime) Start(context.Context) (PastaInstance, error) {
	r.started++
	return r.instance, r.startErr
}
func (r *fakePastaRuntime) Reach(context.Context, PastaInstance) error { return r.reachErr }
func (r *fakePastaRuntime) Stop(context.Context, PastaInstance) error  { r.stopped++; return r.stopErr }
func (r *fakePastaRuntime) VerifyStopped(context.Context, PastaInstance) error {
	r.verified++
	return r.verifyErr
}

func TestPastaProbeProvesNamespaceListenerAndCleanup(t *testing.T) {
	runtime := &fakePastaRuntime{instance: PastaInstance{HelperIdentity: "101:55", ChildIdentity: "102:66", HostEndpoint: "127.0.0.1:45001", ChildNetNS: "net:[2]", HostNetNS: "net:[1]"}}
	result := (PastaProbe{Runtime: runtime}).Probe(context.Background())
	if result.Status != StatusHealthy || result.Code != "pasta_runtime_verified" || result.Evidence["hostEndpoint"] != "127.0.0.1:45001" || runtime.started != 1 || runtime.stopped != 1 || runtime.verified != 1 {
		t.Fatalf("result = %#v, runtime = %#v", result, runtime)
	}
}

func TestPastaProbeBlocksWhenNamespaceIsNotDistinct(t *testing.T) {
	runtime := &fakePastaRuntime{instance: PastaInstance{HelperIdentity: "101:55", ChildIdentity: "102:66", HostEndpoint: "127.0.0.1:45001", ChildNetNS: "net:[1]", HostNetNS: "net:[1]"}}
	result := (PastaProbe{Runtime: runtime}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "pasta_namespace_unconfined" || runtime.stopped != 1 {
		t.Fatalf("result = %#v, runtime = %#v", result, runtime)
	}
}

func TestPastaProbeReportsCleanupFailureInsteadOfHealth(t *testing.T) {
	runtime := &fakePastaRuntime{instance: PastaInstance{HelperIdentity: "101:55", ChildIdentity: "102:66", HostEndpoint: "127.0.0.1:45001", ChildNetNS: "net:[2]", HostNetNS: "net:[1]"}, stopErr: errors.New("token=secret")}
	result := (PastaProbe{Runtime: runtime}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "pasta_cleanup_failed" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPastaProbeReportsListenerFailureAndStillCleansUp(t *testing.T) {
	runtime := &fakePastaRuntime{instance: PastaInstance{HelperIdentity: "101:55", ChildIdentity: "102:66", HostEndpoint: "127.0.0.1:45001", ChildNetNS: "net:[2]", HostNetNS: "net:[1]"}, reachErr: errors.New("unreachable")}
	result := (PastaProbe{Runtime: runtime}).Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "pasta_listener_unreachable" || runtime.stopped != 1 {
		t.Fatalf("result = %#v, runtime = %#v", result, runtime)
	}
}

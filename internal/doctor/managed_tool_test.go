package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type staticManagedToolResolver struct {
	identity ManagedToolIdentity
	err      error
}

func (r staticManagedToolResolver) Inspect(context.Context, string) (ManagedToolIdentity, error) {
	return r.identity, r.err
}

func TestManagedToolProbeReportsLockVerifiedIdentity(t *testing.T) {
	probe := ManagedToolProbe{Name: "devpod", Resolver: staticManagedToolResolver{identity: ManagedToolIdentity{
		Path: "/camp/tools/devpod", Version: "v0.26.1", Repository: "loft-sh/devpod", BinarySHA256: strings.Repeat("a", 64), Managed: true,
	}}}
	result := probe.Probe(context.Background())
	if result.Status != StatusHealthy || result.Code != "managed_tool_identity_verified" || result.Evidence["sha256"] != strings.Repeat("a", 64) || result.Evidence["managed"] != "true" {
		t.Fatalf("result = %#v", result)
	}
}

func TestManagedToolProbeBlocksOnIdentityMismatchWithoutCause(t *testing.T) {
	probe := ManagedToolProbe{Name: "hauler", Resolver: staticManagedToolResolver{err: errors.New("password=secret")}}
	result := probe.Probe(context.Background())
	if result.Status != StatusBlocked || result.Code != "managed_tool_identity_unverified" || strings.Contains(result.Summary+result.Remediation, "secret") {
		t.Fatalf("result = %#v", result)
	}
}

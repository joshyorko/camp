package supervisor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type sequenceRunner struct {
	results []ports.Result
	index   int
}

func (r *sequenceRunner) Run(context.Context, ports.Command) (ports.Result, error) {
	result := r.results[r.index]
	r.index++
	return result, nil
}

type staticDoer struct{ status int }

func (d staticDoer) Do(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: d.status, Body: io.NopCloser(strings.NewReader("ok")), Request: request}, nil
}

func TestUnitInspectorRequiresExactHostLoopbackGuestOwnershipAndHTTP(t *testing.T) {
	t.Parallel()
	service := ServiceSpec{Name: "registry", Mapping: PortMapping{HostAddress: "127.0.0.1", HostPort: 5000, GuestPort: 5100}}
	helper := ports.ProcessStatus{Identity: domain.ProcessIdentity{PID: 101}, NetNS: "net:[host]"}
	child := ports.ProcessStatus{Identity: domain.ProcessIdentity{PID: 102}, NetNS: "net:[child]"}
	runner := &sequenceRunner{results: []ports.Result{
		{Stdout: []byte("LISTEN 0 4096 127.0.0.1:5000 0.0.0.0:*\n")}, // host mapping v4
		{}, // host mapping v6
		{}, // guest port host v4
		{}, // guest port host v6
		{Stdout: []byte(`LISTEN 0 4096 *:5100 *:* users:(("hauler",pid=102,fd=4))`)},
	}}
	inspector := NewUnitInspector(runner, staticDoer{status: http.StatusOK})
	evidence, err := inspector.Ready(context.Background(), service, helper, child)
	if err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
	if evidence.HostEndpoint != "127.0.0.1:5000" || evidence.GuestEndpoint != "127.0.0.1:5100" || evidence.ChildNetNS != "net:[child]" {
		t.Fatalf("evidence = %#v", evidence)
	}

	wildcard := &sequenceRunner{results: []ports.Result{{Stdout: []byte("LISTEN 0 4096 0.0.0.0:5000 0.0.0.0:*\n")}, {}, {}, {}, {}}}
	if _, err := NewUnitInspector(wildcard, staticDoer{status: 200}).Ready(context.Background(), service, helper, child); err == nil {
		t.Fatal("Ready() accepted wildcard host exposure")
	}
}

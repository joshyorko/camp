package workspace

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

type Reachability struct {
	executor ports.WorkspaceExecutor
	curl     string
}

func NewReachability(executor ports.WorkspaceExecutor, curlExecutable string) *Reachability {
	return &Reachability{executor: executor, curl: curlExecutable}
}

func (r *Reachability) Probe(ctx context.Context, request ports.ReachabilityRequest) error {
	if r == nil || r.executor == nil || r.curl == "" || request.WorkspaceID == "" || len(request.Endpoints) == 0 {
		return errors.New("workspace reachability request is incomplete")
	}
	for _, endpoint := range request.Endpoints {
		host, _, err := net.SplitHostPort(endpoint.Address)
		if err != nil || host != "127.0.0.1" || !strings.HasPrefix(endpoint.Path, "/") {
			return fmt.Errorf("workspace endpoint %q is not exact loopback", endpoint.Name)
		}
		_, err = r.executor.Execute(ctx, ports.WorkspaceCommand{
			Context: request.Context, WorkspaceID: request.WorkspaceID,
			Argv: []string{r.curl, "--fail", "--silent", "--show-error", "http://" + endpoint.Address + endpoint.Path},
		})
		if err != nil {
			return fmt.Errorf("workspace cannot reach %s at %s: %w", endpoint.Name, endpoint.Address, err)
		}
	}
	return nil
}

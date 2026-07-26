//go:build linux

package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/adapters/devpod"
	lifecycleadapter "github.com/joshyorko/camp/internal/adapters/lifecycle"
	"github.com/joshyorko/camp/internal/adapters/subprocess"
	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/config"
	"github.com/joshyorko/camp/internal/doctor"
	"github.com/joshyorko/camp/internal/domain"
)

type doctorToolResolver interface {
	Inspect(context.Context, string) (doctor.ManagedToolIdentity, error)
}

func productionReachabilityProbes(bootstrap config.Bootstrap, sessions []domain.JournalSnapshot, tools doctorToolResolver) []doctor.Probe {
	active := make([]domain.JournalSnapshot, 0, len(sessions))
	for _, session := range sessions {
		if session.State != domain.SessionClosed {
			active = append(active, session)
		}
	}
	workspaceConfigured := false
	forwardingConfigured := false
	serviceConfigured := false
	for _, session := range active {
		workspaceConfigured = workspaceConfigured || session.Workspace.ID != ""
		forwardingConfigured = forwardingConfigured || len(session.Recovery.Forwarding) != 0
		serviceConfigured = serviceConfigured || len(session.Services) != 0
	}
	clientFor := func(ctx context.Context) (*devpod.Client, error) {
		identity, err := tools.Inspect(ctx, "devpod")
		if err != nil {
			return nil, err
		}
		return devpod.NewClient(identity.Path, subprocess.NewRunner()), nil
	}
	return []doctor.Probe{
		doctor.ConfiguredProbe{Name: "provider", Configured: bootstrap.DevPodProvider != "", Remediation: fmt.Sprintf("camp provider add %s --context %s", bootstrap.DevPodProvider, bootstrap.DevPodContext), Check: func(ctx context.Context) (map[string]string, error) {
			client, err := clientFor(ctx)
			if err != nil {
				return nil, err
			}
			if err := client.ProbeProvider(ctx, bootstrap.DevPodContext, bootstrap.DevPodProvider); err != nil {
				return nil, err
			}
			return map[string]string{"context": bootstrap.DevPodContext, "provider": bootstrap.DevPodProvider, "operation": "read-only-provider-list"}, nil
		}},
		doctor.ConfiguredProbe{Name: "workspace", Configured: workspaceConfigured, Check: func(ctx context.Context) (map[string]string, error) {
			client, err := clientFor(ctx)
			if err != nil {
				return nil, err
			}
			count := 0
			for _, session := range active {
				if session.Workspace.ID == "" {
					continue
				}
				status, err := client.StatusInContext(ctx, session.Workspace.Context, session.Workspace.ID)
				if err != nil || status.ID != session.Workspace.ID || (status.State != devpod.StateRunning && status.State != devpod.StateBusy) {
					return nil, errors.New("configured workspace identity is not running")
				}
				count++
			}
			return map[string]string{"count": strconv.Itoa(count), "operation": "status-identity"}, nil
		}},
		doctor.ConfiguredProbe{Name: "forwarding", Configured: forwardingConfigured, Check: func(ctx context.Context) (map[string]string, error) {
			client, err := clientFor(ctx)
			if err != nil {
				return nil, err
			}
			processes, err := supervisor.NewProcessManager()
			if err != nil {
				return nil, err
			}
			manager := lifecycleadapter.NewForwarderManager(client, processes)
			count := 0
			for _, session := range active {
				for _, record := range session.Recovery.Forwarding {
					request := domain.ForwardingRequest{
						Name: record.Name, WorkspaceID: session.Workspace.ID, Context: session.Workspace.Context,
						LocalEndpoint: record.LocalEndpoint, WorkspaceEndpoint: record.WorkspaceEndpoint,
						EvidencePath: record.EvidencePath, LogPath: strings.TrimSuffix(record.EvidencePath, filepath.Ext(record.EvidencePath)) + ".log",
					}
					if _, err := manager.Observe(ctx, request); err != nil {
						return nil, err
					}
					count++
				}
			}
			return map[string]string{"count": strconv.Itoa(count), "operation": "process-identity-and-workspace-http"}, nil
		}},
		doctor.ConfiguredProbe{Name: "service", Configured: serviceConfigured, Check: func(ctx context.Context) (map[string]string, error) {
			processes, err := supervisor.NewProcessManager()
			if err != nil {
				return nil, err
			}
			units := supervisor.NewServiceSupervisor(nil, processes, supervisor.NewUnitInspector(subprocess.NewRunner(), http.DefaultClient))
			count := 0
			for _, session := range active {
				for _, record := range session.Services {
					observation, err := units.Observe(ctx, record)
					if err != nil || observation.State != supervisor.UnitLive {
						return nil, errors.New("configured service identity and HTTP reachability are not live")
					}
					count++
				}
			}
			return map[string]string{"count": strconv.Itoa(count), "operation": "process-namespace-and-http"}, nil
		}},
	}
}

//go:build linux

package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/joshyorko/camp/internal/adapters/supervisor"
	"github.com/joshyorko/camp/internal/doctor"
	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type productionPastaRuntime struct {
	resolver  *supervisor.ConfinementResolver
	processes *supervisor.ProcessManager
	allocator *supervisor.PortAllocator
	dataRoot  string
	binary    string
	mu        sync.Mutex
	states    map[string]pastaRuntimeState
}

type pastaRuntimeState struct {
	helper domain.ProcessIdentity
	child  domain.ProcessIdentity
	token  string
	dir    string
	dev    uint64
	ino    uint64
}

func newProductionPastaRuntime(resolver *supervisor.ConfinementResolver, dataRoot string) (*productionPastaRuntime, error) {
	processes, err := supervisor.NewProcessManager()
	if err != nil {
		return nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, err
	}
	return &productionPastaRuntime{resolver: resolver, processes: processes, allocator: supervisor.NewPortAllocator(), dataRoot: dataRoot, binary: binary, states: make(map[string]pastaRuntimeState)}, nil
}

func (r *productionPastaRuntime) Start(ctx context.Context) (doctor.PastaInstance, error) {
	capability, err := r.resolver.Resolve(ctx)
	if err != nil {
		return doctor.PastaInstance{}, err
	}
	portsFound, err := r.allocator.Candidates(ctx, 0, 2)
	if err != nil {
		return doctor.PastaInstance{}, err
	}
	root := filepath.Join(r.dataRoot, "doctor", "pasta")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return doctor.PastaInstance{}, err
	}
	directory, err := os.MkdirTemp(root, "probe-")
	if err != nil {
		return doctor.PastaInstance{}, err
	}
	owned := false
	defer func() {
		if !owned {
			_ = os.RemoveAll(directory)
		}
	}()
	info, err := os.Lstat(directory)
	if err != nil {
		return doctor.PastaInstance{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return doctor.PastaInstance{}, errors.New("doctor pasta directory identity is unavailable")
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return doctor.PastaInstance{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	hostPort, guestPort := portsFound[0], portsFound[1]
	spec, err := supervisor.BuildPastaLoopback(supervisor.PastaLoopback{
		Capability: capability,
		Mapping:    supervisor.PortMapping{HostAddress: "127.0.0.1", HostPort: hostPort, GuestPort: guestPort},
		LogPath:    filepath.Join(directory, "pasta.log"), PIDPath: filepath.Join(directory, "pasta.pid"),
		Child: ports.Command{Executable: r.binary, Argv: []string{"__doctor-listener", strconv.Itoa(guestPort), token}},
	})
	if err != nil {
		return doctor.PastaInstance{}, err
	}
	helper, err := r.processes.Start(ctx, spec)
	if err != nil {
		return doctor.PastaInstance{}, err
	}
	child, err := r.waitForChild(ctx, helper)
	if err != nil {
		_ = r.processes.Stop(context.WithoutCancel(ctx), helper, time.Second)
		return doctor.PastaInstance{}, err
	}
	helperStatus, err := r.processes.Inspect(ctx, helper)
	if err != nil {
		_ = r.processes.Stop(context.WithoutCancel(ctx), helper, time.Second)
		return doctor.PastaInstance{}, err
	}
	state := pastaRuntimeState{helper: helper, child: child.Identity, token: token, dir: directory, dev: uint64(stat.Dev), ino: stat.Ino}
	key := processIdentityKey(helper)
	r.mu.Lock()
	r.states[key] = state
	r.mu.Unlock()
	owned = true
	return doctor.PastaInstance{
		HelperIdentity: key, ChildIdentity: processIdentityKey(child.Identity),
		HostEndpoint: net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)),
		HostNetNS:    helperStatus.NetNS, ChildNetNS: child.NetNS,
	}, nil
}

func (r *productionPastaRuntime) waitForChild(ctx context.Context, helper domain.ProcessIdentity) (ports.ProcessStatus, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		children, err := r.processes.Children(ctx, helper)
		if err == nil {
			for _, child := range children {
				if child.Running && child.Executable == r.binary && len(child.Argv) >= 4 && child.Argv[1] == "__doctor-listener" {
					return child, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return ports.ProcessStatus{}, errors.New("pasta child identity did not stabilize")
		}
		select {
		case <-ctx.Done():
			return ports.ProcessStatus{}, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (r *productionPastaRuntime) Reach(ctx context.Context, instance doctor.PastaInstance) error {
	state, err := r.state(instance)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		dialer := net.Dialer{Timeout: 50 * time.Millisecond}
		connection, dialErr := dialer.DialContext(ctx, "tcp4", instance.HostEndpoint)
		if dialErr == nil {
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			_, _ = connection.Write([]byte(state.token + "\n"))
			response, readErr := bufio.NewReader(connection).ReadString('\n')
			_ = connection.Close()
			if readErr == nil && response == state.token+"\n" {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return errors.New("pasta mapped listener did not return the probe identity")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (r *productionPastaRuntime) Stop(ctx context.Context, instance doctor.PastaInstance) error {
	state, err := r.state(instance)
	if err != nil {
		return err
	}
	if err := r.processes.Stop(ctx, state.helper, time.Second); err != nil {
		return err
	}
	child, err := r.processes.Inspect(ctx, state.child)
	if err == nil && child.Running {
		return r.processes.Stop(ctx, state.child, time.Second)
	}
	if err != nil && !errors.Is(err, supervisor.ErrProcessIdentity) {
		return err
	}
	return nil
}

func (r *productionPastaRuntime) VerifyStopped(ctx context.Context, instance doctor.PastaInstance) error {
	state, err := r.state(instance)
	if err != nil {
		return err
	}
	for _, identity := range []domain.ProcessIdentity{state.helper, state.child} {
		status, inspectErr := r.processes.Inspect(ctx, identity)
		if inspectErr != nil && !errors.Is(inspectErr, supervisor.ErrProcessIdentity) {
			return inspectErr
		}
		if inspectErr == nil && status.Running {
			return errors.New("pasta process identity remains running")
		}
	}
	connection, dialErr := net.DialTimeout("tcp4", instance.HostEndpoint, 50*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("pasta host listener remains reachable")
	}
	info, err := os.Lstat(state.dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || uint64(stat.Dev) != state.dev || stat.Ino != state.ino {
		return errors.New("pasta probe directory identity changed")
	}
	if err := os.RemoveAll(state.dir); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.states, instance.HelperIdentity)
	r.mu.Unlock()
	return nil
}

func (r *productionPastaRuntime) state(instance doctor.PastaInstance) (pastaRuntimeState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.states[instance.HelperIdentity]
	if !ok || processIdentityKey(state.child) != instance.ChildIdentity {
		return pastaRuntimeState{}, errors.New("pasta probe identity is unknown or changed")
	}
	return state, nil
}

func processIdentityKey(identity domain.ProcessIdentity) string {
	return fmt.Sprintf("%d:%s:%d", identity.PID, identity.BootID, identity.StartTicks)
}

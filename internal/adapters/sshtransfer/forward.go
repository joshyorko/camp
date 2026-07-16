package sshtransfer

import (
	"errors"
	"net"
	"strconv"

	"github.com/joshyorko/camp/internal/ports"
)

type Endpoint struct {
	Address string
	Port    uint16
}

type ReverseForwardSpec struct {
	SSHExecutable string
	WorkspaceID   string
	Remote        Endpoint
	Local         Endpoint
}

type ReverseForward struct {
	Host    string
	Remote  Endpoint
	Local   Endpoint
	Command ports.Command
}

func BuildReverseForward(spec ReverseForwardSpec) (ReverseForward, error) {
	if spec.SSHExecutable == "" {
		return ReverseForward{}, errors.New("ssh executable is required")
	}
	host, err := DevPodHost(spec.WorkspaceID)
	if err != nil {
		return ReverseForward{}, err
	}
	if !validLoopbackEndpoint(spec.Remote) || !validLoopbackEndpoint(spec.Local) {
		return ReverseForward{}, errors.New("reverse forward endpoints must use IPv4 loopback and nonzero ports")
	}
	binding := endpointString(spec.Remote) + ":" + endpointString(spec.Local)
	return ReverseForward{
		Host:   host,
		Remote: spec.Remote,
		Local:  spec.Local,
		Command: ports.Command{
			Executable: spec.SSHExecutable,
			Argv: []string{
				"-N", "-T", "-o", "ExitOnForwardFailure=yes",
				"-R", binding,
				host,
			},
		},
	}, nil
}

func validLoopbackEndpoint(endpoint Endpoint) bool {
	return endpoint.Address == "127.0.0.1" && endpoint.Port != 0
}

func endpointString(endpoint Endpoint) string {
	return net.JoinHostPort(endpoint.Address, strconv.FormatUint(uint64(endpoint.Port), 10))
}

package cli

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

func RunDoctorProbeListener(ctx context.Context, port int, token string) error {
	if port < 1 || port > 65535 || len(token) < 16 || len(token) > 128 || strings.ContainsAny(token, "\r\n") {
		return errors.New("invalid doctor probe listener identity")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		line, readErr := bufio.NewReader(io.LimitReader(connection, 129)).ReadString('\n')
		if readErr == nil && strings.TrimSuffix(line, "\n") == token {
			_, _ = connection.Write([]byte(token + "\n"))
		}
		_ = connection.Close()
	}
}

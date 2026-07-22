package cli

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestRunDoctorProbeListenerEchoesOnlyMatchingToken(t *testing.T) {
	reserved, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	const token = "probe-token-unique"
	go func() { done <- RunDoctorProbeListener(ctx, port, token) }()

	deadline := time.Now().Add(time.Second)
	for {
		connection, dialErr := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 25*time.Millisecond)
		if dialErr == nil {
			_, _ = connection.Write([]byte(token + "\n"))
			response, _ := bufio.NewReader(connection).ReadString('\n')
			_ = connection.Close()
			if response != token+"\n" {
				t.Fatalf("response = %q", response)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener did not start: %v", dialErr)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("listener did not stop after cancellation")
	}
}

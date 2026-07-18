package cli

import (
	"context"
	"errors"
	"testing"
)

func TestMachineIdentityFallbackIsStableAndFailsClosed(t *testing.T) {
	t.Parallel()
	missing := func(context.Context) (string, error) { return "", errors.New("no machine-id") }
	hostname := func() (string, error) { return "ror.devpod", nil }
	first, err := resolveMachineID(context.Background(), missing, hostname)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveMachineID(context.Background(), missing, hostname)
	if err != nil || first == "" || first != second {
		t.Fatalf("fallback first=%q second=%q err=%v", first, second, err)
	}
	if _, err := resolveMachineID(context.Background(), missing, func() (string, error) { return "", errors.New("no hostname") }); err == nil {
		t.Fatal("missing identity sources unexpectedly succeeded")
	}
}

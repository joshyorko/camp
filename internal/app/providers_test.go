package app

import (
	"context"
	"errors"
	"testing"
)

func TestProvidersReadRedactsPasswordMarkedValues(t *testing.T) {
	t.Parallel()
	providers := NewProviders(providerReaderStub{providers: []Provider{{
		Name:    "ssh",
		Options: []ProviderOption{{Name: "HOST", Value: "host.test"}, {Name: "PASSWORD", Value: "super-secret", Password: true}},
	}}})
	got, err := providers.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Options[0].Value != "host.test" || got[0].Options[1].Value != "[REDACTED]" {
		t.Fatalf("providers = %#v", got)
	}
}

func TestProvidersMutationFailsUnsupportedBeforeEffects(t *testing.T) {
	t.Parallel()
	reader := &providerReaderStub{}
	providers := NewProviders(reader)
	if err := providers.Update(context.Background(), "ssh", map[string]string{"HOST": "host.test"}); !errors.Is(err, ErrProviderMutationUnsupported) {
		t.Fatalf("Update() error = %v", err)
	}
	if reader.calls != 0 {
		t.Fatalf("provider reader calls = %d", reader.calls)
	}
}

type providerReaderStub struct {
	providers []Provider
	calls     int
}

func (s providerReaderStub) ListProviders(context.Context) ([]Provider, error) {
	return s.providers, nil
}

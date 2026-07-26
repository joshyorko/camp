package app

import (
	"context"
	"errors"
	"testing"
)

func TestProvidersReadRedactsPasswordMarkedValues(t *testing.T) {
	t.Parallel()
	providers := NewProviders(&providerReaderStub{providers: []Provider{{
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

func TestProviderReaderStubRecordsListCalls(t *testing.T) {
	t.Parallel()
	reader := &providerReaderStub{}
	providers := NewProviders(reader)
	if _, err := providers.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reader.calls != 1 {
		t.Fatalf("provider reader calls = %d, want 1", reader.calls)
	}
}

func TestProvidersListRejectsMissingReader(t *testing.T) {
	t.Parallel()
	for name, providers := range map[string]*Providers{
		"nil receiver": nil,
		"nil reader":   NewProviders(nil),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := providers.List(context.Background()); err == nil {
				t.Fatal("List() error = nil")
			}
		})
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

func TestProviderConfigurationDelegatesTypedAddAndUseRequests(t *testing.T) {
	t.Parallel()
	configurer := &providerConfigurerStub{}
	operations := NewProviderConfiguration(configurer)
	request := ProviderMutationRequest{Name: "docker", Context: "work", Options: []string{"HELPER=false"}}

	added, err := operations.Add(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	used, err := operations.Use(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(configurer.added) != 1 || configurer.added[0].Name != "docker" || len(configurer.used) != 1 || configurer.used[0].Context != "work" {
		t.Fatalf("provider requests added=%#v used=%#v", configurer.added, configurer.used)
	}
	if added.Action != "added" || added.NextCommand != "camp doctor" || used.Action != "selected" || used.NextCommand != "camp doctor" {
		t.Fatalf("provider results added=%#v used=%#v", added, used)
	}
}

func TestProviderConfigurationRejectsMissingConfigurer(t *testing.T) {
	t.Parallel()
	if _, err := NewProviderConfiguration(nil).Add(context.Background(), ProviderMutationRequest{Name: "docker", Context: "default"}); err == nil {
		t.Fatal("Add() error = nil")
	}
}

type providerReaderStub struct {
	providers []Provider
	calls     int
}

type providerConfigurerStub struct {
	added []ProviderMutationRequest
	used  []ProviderMutationRequest
}

func (s *providerConfigurerStub) AddProvider(_ context.Context, request ProviderMutationRequest) error {
	s.added = append(s.added, request)
	return nil
}

func (s *providerConfigurerStub) UseProvider(_ context.Context, request ProviderMutationRequest) error {
	s.used = append(s.used, request)
	return nil
}

func (s *providerReaderStub) ListProviders(context.Context) ([]Provider, error) {
	s.calls++
	return s.providers, nil
}

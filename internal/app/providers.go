package app

import (
	"context"
	"errors"
	"sort"
)

var ErrProviderMutationUnsupported = errors.New("provider mutation is unsupported until DevPod provides atomic non-secret persistence")

type ProviderOption struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
	Password    bool   `json:"-"`
}

type Provider struct {
	Name    string           `json:"name"`
	Options []ProviderOption `json:"options,omitempty"`
}

type ProviderReader interface {
	ListProviders(context.Context) ([]Provider, error)
}

type Providers struct{ reader ProviderReader }

func NewProviders(reader ProviderReader) *Providers { return &Providers{reader: reader} }

func (p *Providers) List(ctx context.Context) ([]Provider, error) {
	providers, err := p.reader.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	for providerIndex := range providers {
		for optionIndex := range providers[providerIndex].Options {
			if providers[providerIndex].Options[optionIndex].Password {
				providers[providerIndex].Options[optionIndex].Value = "[REDACTED]"
			}
		}
		sort.Slice(providers[providerIndex].Options, func(left, right int) bool {
			return providers[providerIndex].Options[left].Name < providers[providerIndex].Options[right].Name
		})
	}
	sort.Slice(providers, func(left, right int) bool { return providers[left].Name < providers[right].Name })
	return providers, nil
}

func (*Providers) Update(context.Context, string, map[string]string) error {
	return ErrProviderMutationUnsupported
}

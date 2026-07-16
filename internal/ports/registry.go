package ports

import "context"

type RegistryReference struct {
	Repository     string `json:"repository"`
	Tag            string `json:"tag"`
	ManifestDigest string `json:"manifestDigest"`
}

type RegistryCatalog interface {
	List(context.Context, string) ([]RegistryReference, error)
	Resolve(context.Context, string, string, string) (string, error)
}

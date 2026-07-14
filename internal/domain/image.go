package domain

import "time"

type ImageSource string

const (
	ImageSourceDaemon   ImageSource = "daemon"
	ImageSourceRegistry ImageSource = "registry"
)

type Platform struct {
	OS           string `json:"os" yaml:"os"`
	Architecture string `json:"architecture" yaml:"architecture"`
	Variant      string `json:"variant,omitempty" yaml:"variant,omitempty"`
}

type Image struct {
	EngineImageID          string      `json:"engineImageId,omitempty" yaml:"engineImageId,omitempty"`
	OriginalTags           []string    `json:"originalTags" yaml:"originalTags"`
	OriginalRepoDigests    []string    `json:"originalRepoDigests,omitempty" yaml:"originalRepoDigests,omitempty"`
	CapturedReference      string      `json:"capturedReference" yaml:"capturedReference"`
	CapturedManifestDigest string      `json:"capturedManifestDigest" yaml:"capturedManifestDigest"`
	Platform               Platform    `json:"platform" yaml:"platform"`
	Source                 ImageSource `json:"source" yaml:"source"`
	CreatedAt              time.Time   `json:"createdAt" yaml:"createdAt"`
}

type ImageInventory struct {
	SchemaVersion int       `json:"schemaVersion" yaml:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt" yaml:"generatedAt"`
	Images        []Image   `json:"images" yaml:"images"`
}

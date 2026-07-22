package hauler

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"gopkg.in/yaml.v3"
)

type manifestMetadata struct {
	Name string `yaml:"name"`
}

type filesManifest struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   manifestMetadata `yaml:"metadata"`
	Spec       filesSpec        `yaml:"spec"`
}

type filesSpec struct {
	Files []manifestFile `yaml:"files"`
}

type manifestFile struct {
	Path string `yaml:"path"`
	Name string `yaml:"name"`
}

type imagesManifest struct {
	APIVersion string           `yaml:"apiVersion"`
	Kind       string           `yaml:"kind"`
	Metadata   manifestMetadata `yaml:"metadata"`
	Spec       imagesSpec       `yaml:"spec"`
}

type imagesSpec struct {
	Images []manifestImage `yaml:"images"`
}

type manifestImage struct {
	Name     string `yaml:"name"`
	Platform string `yaml:"platform,omitempty"`
	Rewrite  string `yaml:"rewrite,omitempty"`
	Local    bool   `yaml:"local,omitempty"`
}

var manifestDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func RenderManifest(capsule string, inventory domain.ImageInventory) ([]byte, error) {
	if capsule == "" || strings.ContainsAny(capsule, "/\\\x00") {
		return nil, errors.New("invalid capsule manifest identity")
	}
	images := make([]manifestImage, 0, len(inventory.Images))
	seen := make(map[string]struct{})
	for _, image := range inventory.Images {
		if image.CapturedReference == "" {
			return nil, errors.New("image inventory entry lacks reference")
		}
		name := preferredRegistryReference(image)
		rewrite := ""
		switch image.Source {
		case domain.ImageSourceRegistry:
			mutableName := name
			var err error
			name, err = immutableImageReference(name, image.CapturedManifestDigest)
			if err != nil {
				return nil, err
			}
			parts := strings.SplitN(mutableName, "/", 2)
			if len(parts) == 2 && strings.LastIndexByte(parts[1], ':') > strings.LastIndexByte(parts[1], '/') {
				rewrite = parts[1]
			}
		case domain.ImageSourceDaemon:
		default:
			return nil, fmt.Errorf("unknown image source %q", image.Source)
		}
		platform := ""
		if image.Platform.OS != "" || image.Platform.Architecture != "" || image.Platform.Variant != "" {
			if image.Platform.OS == "" || image.Platform.Architecture == "" {
				return nil, errors.New("image inventory entry has incomplete platform")
			}
			platform = image.Platform.OS + "/" + image.Platform.Architecture
			if image.Platform.Variant != "" {
				platform += "/" + image.Platform.Variant
			}
		} else if image.Source == domain.ImageSourceDaemon {
			return nil, errors.New("daemon image inventory entry lacks platform")
		}
		key := name + "\x00" + platform
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("duplicate manifest image %q", name)
		}
		seen[key] = struct{}{}
		images = append(images, manifestImage{Name: name, Platform: platform, Rewrite: rewrite, Local: image.Source == domain.ImageSourceDaemon})
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].Name == images[j].Name {
			return images[i].Platform < images[j].Platform
		}
		return images[i].Name < images[j].Name
	})
	files := filesManifest{
		APIVersion: "content.hauler.cattle.io/v1", Kind: "Files", Metadata: manifestMetadata{Name: "camp-" + capsule},
		Spec: filesSpec{Files: []manifestFile{{Path: ".camp/build/" + capsule + ".tar.zst", Name: capsule + ".tar.zst"}}},
	}
	imageDocument := imagesManifest{
		APIVersion: "content.hauler.cattle.io/v1", Kind: "Images", Metadata: manifestMetadata{Name: "camp-" + capsule + "-images"},
		Spec: imagesSpec{Images: images},
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(files); err != nil {
		return nil, err
	}
	if err := encoder.Encode(imageDocument); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func preferredRegistryReference(image domain.Image) string {
	preferred := image.CapturedReference
	parts := strings.SplitN(image.CapturedReference, "/", 2)
	if len(parts) != 2 {
		return preferred
	}
	for _, original := range image.OriginalTags {
		originalParts := strings.SplitN(original, "/", 2)
		if len(originalParts) == 2 && originalParts[0] == parts[0] && original < preferred {
			preferred = original
		}
	}
	return preferred
}

func immutableImageReference(reference, digest string) (string, error) {
	if !manifestDigestPattern.MatchString(digest) {
		return "", errors.New("registry image lacks a verified digest")
	}
	if strings.ContainsAny(reference, " \t\r\n\x00") || strings.Contains(reference, "://") {
		return "", errors.New("invalid registry image reference")
	}
	if at := strings.LastIndexByte(reference, '@'); at >= 0 {
		if reference[at+1:] != digest || at == 0 {
			return "", errors.New("registry image reference digest conflicts with verified digest")
		}
		return reference, nil
	}
	lastSlash := strings.LastIndexByte(reference, '/')
	lastColon := strings.LastIndexByte(reference, ':')
	if lastColon > lastSlash {
		reference = reference[:lastColon]
	}
	if reference == "" || !strings.Contains(reference, "/") {
		return "", errors.New("invalid registry image reference")
	}
	return reference + "@" + digest, nil
}

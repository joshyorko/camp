package images

import (
	"context"
	"errors"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

var ErrCapturedDigestMismatch = errors.New("captured image digest mismatch")

type CaptureRequest struct {
	Scope             EngineScope
	Capsule           string
	RegistryAuthority string
	RegistryEndpoint  string
	ExcludeTags       []string
	Previous          domain.ImageInventory
}

type Capturer struct {
	clock ports.Clock
}

func NewCapturer(_ ports.WorkspaceExecutor, _ ports.RegistryCatalog, clock ports.Clock) *Capturer {
	return &Capturer{clock: clock}
}

func (c *Capturer) Capture(_ context.Context, request CaptureRequest) (domain.ImageInventory, error) {
	if c == nil || c.clock == nil {
		return domain.ImageInventory{}, errors.New("image capture dependencies or request are incomplete")
	}
	if request.Previous.SchemaVersion != 0 && request.Previous.SchemaVersion != domain.SchemaVersion {
		return domain.ImageInventory{}, errors.New("previous image inventory schema is unsupported")
	}
	return domain.ImageInventory{
		SchemaVersion: domain.SchemaVersion,
		GeneratedAt:   c.clock.Now().UTC(),
		Images:        []domain.Image{},
	}, nil
}

func splitCapturedTag(reference string) (string, string, error) {
	separator := strings.IndexByte(reference, '/')
	if separator <= 0 || separator == len(reference)-1 {
		return "", "", errors.New("captured image has no registry repository")
	}
	if err := validateRegistryAuthority(reference[:separator]); err != nil {
		return "", "", err
	}
	path := reference[separator+1:]
	if err := validateReferencePath(path); err != nil {
		return "", "", err
	}
	tagSeparator := strings.LastIndexByte(path, ':')
	if tagSeparator <= 0 || tagSeparator == len(path)-1 || strings.Contains(path, "@") {
		return "", "", errors.New("captured image is not a tagged reference")
	}
	return path[:tagSeparator], path[tagSeparator+1:], nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

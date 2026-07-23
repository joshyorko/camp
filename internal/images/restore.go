package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/joshyorko/camp/internal/domain"
	"github.com/joshyorko/camp/internal/ports"
)

type RestoreRequest struct {
	Scope             EngineScope
	RegistryAuthority string
	RegistryEndpoint  string
	Inventory         domain.ImageInventory
}

type RestoreResult struct {
	Restored int `json:"restored"`
	Tags     int `json:"tags"`
}

var ErrOriginalTagConflict = errors.New("original image tag already points at different content")

type Restorer struct {
	executor ports.WorkspaceExecutor
	catalog  ports.RegistryCatalog
}

func NewRestorer(executor ports.WorkspaceExecutor, catalog ports.RegistryCatalog) *Restorer {
	return &Restorer{executor: executor, catalog: catalog}
}

func (r *Restorer) Restore(ctx context.Context, request RestoreRequest) (RestoreResult, error) {
	if r == nil || r.executor == nil || r.catalog == nil || request.RegistryEndpoint == "" {
		return RestoreResult{}, errors.New("image restore dependencies or request are incomplete")
	}
	if request.Inventory.SchemaVersion != domain.SchemaVersion {
		return RestoreResult{}, errors.New("image inventory schema is unsupported")
	}
	if err := validateRegistryAuthority(request.RegistryAuthority); err != nil {
		return RestoreResult{}, err
	}
	engine, err := NewDetector(r.executor).Detect(ctx, request.Scope)
	if err != nil {
		return RestoreResult{}, err
	}
	result := RestoreResult{}
	for _, image := range request.Inventory.Images {
		if image.CapturedManifestDigest == "" {
			return result, errors.New("captured image lacks verified digest")
		}
		rewritten, err := RewriteRegistryAuthority(preferredServedReference(image), request.RegistryAuthority)
		if err != nil {
			return result, err
		}
		repository, _, err := splitCapturedTag(rewritten)
		if err != nil {
			return result, err
		}
		resolved, err := r.catalog.Resolve(ctx, request.RegistryEndpoint, repository, image.CapturedManifestDigest)
		if err != nil {
			return result, err
		}
		if resolved != image.CapturedManifestDigest {
			return result, fmt.Errorf("served registry resolved %s, inventory requires %s: %w", resolved, image.CapturedManifestDigest, ErrCapturedDigestMismatch)
		}
		digestReference := request.RegistryAuthority + "/" + repository + "@" + resolved
		pull := []string{"image", "pull"}
		platform := imagePlatform(image.Platform)
		if platform != "" {
			pull = append(pull, "--platform", platform)
		}
		pull = append(pull, digestReference)
		command, err := engine.run(ctx, pull...)
		if err != nil || command.ExitCode != 0 {
			return result, commandError("pull captured image", command.ExitCode, err)
		}
		inspection, err := inspectEngineImages(ctx, engine, []string{digestReference})
		if err != nil {
			return result, err
		}
		if len(inspection) != 1 || inspection[0].ID == "" || !containsString(inspection[0].RepoDigests, digestReference) {
			return result, fmt.Errorf("pulled image does not expose verified digest %q: %w", digestReference, ErrCapturedDigestMismatch)
		}
		if platform != "" && imagePlatform(inspection[0].Platform) != platform {
			return result, fmt.Errorf("pulled image platform %q does not match %q: %w", imagePlatform(inspection[0].Platform), platform, ErrCapturedDigestMismatch)
		}
		tags := sortedUnique(image.OriginalTags)
		for _, original := range tags {
			if !safeImageReference(original) {
				return result, fmt.Errorf("unsafe original image tag %q", original)
			}
			existingResult, existingErr := engine.run(ctx, "image", "inspect", original)
			if existingErr == nil && existingResult.ExitCode == 0 {
				var existing []restoreInspection
				if err := json.Unmarshal(existingResult.Stdout, &existing); err != nil || len(existing) != 1 || existing[0].ID == "" {
					return result, errors.New("decode existing original image tag inspection")
				}
				if existing[0].ID != inspection[0].ID {
					return result, fmt.Errorf("tag %q points at %s instead of %s: %w", original, existing[0].ID, inspection[0].ID, ErrOriginalTagConflict)
				}
				continue
			}
			if existingResult.ExitCode != 1 {
				return result, commandError("inspect original image tag", existingResult.ExitCode, existingErr)
			}
			tagged, err := engine.run(ctx, "image", "tag", inspection[0].ID, original)
			if err != nil || tagged.ExitCode != 0 {
				return result, commandError("restore original image tag", tagged.ExitCode, err)
			}
			verified, err := inspectEngineImages(ctx, engine, []string{original})
			if err != nil {
				return result, err
			}
			if len(verified) != 1 || verified[0].ID != inspection[0].ID {
				return result, errors.New("restored tag points at a different image")
			}
		}
		result.Restored++
		result.Tags += len(tags)
	}
	return result, nil
}

func preferredServedReference(image domain.Image) string {
	preferred := ""
	parts := strings.SplitN(image.CapturedReference, "/", 2)
	if len(parts) != 2 {
		return image.CapturedReference
	}
	for _, original := range image.OriginalTags {
		originalParts := strings.SplitN(original, "/", 2)
		if len(originalParts) == 2 && originalParts[0] == parts[0] && (preferred == "" || original < preferred) {
			preferred = original
		}
	}
	if preferred == "" {
		return image.CapturedReference
	}
	return preferred
}

type restoreInspection struct {
	ID           string   `json:"Id"`
	RepoDigests  []string `json:"RepoDigests"`
	OS           string   `json:"Os"`
	Architecture string   `json:"Architecture"`
	Variant      string   `json:"Variant"`
	Platform     domain.Platform
}

func (i *restoreInspection) UnmarshalJSON(body []byte) error {
	var value struct {
		ID           string   `json:"Id"`
		RepoDigests  []string `json:"RepoDigests"`
		OS           string   `json:"Os"`
		Architecture string   `json:"Architecture"`
		Variant      string   `json:"Variant"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	i.ID = value.ID
	i.RepoDigests = value.RepoDigests
	i.OS = value.OS
	i.Architecture = value.Architecture
	i.Variant = value.Variant
	i.Platform = domain.Platform{OS: value.OS, Architecture: value.Architecture, Variant: value.Variant}
	return nil
}

func inspectEngineImages(ctx context.Context, engine Engine, references []string) ([]restoreInspection, error) {
	args := append([]string{"image", "inspect"}, references...)
	result, err := engine.run(ctx, args...)
	if err != nil || result.ExitCode != 0 {
		return nil, commandError("inspect restored image", result.ExitCode, err)
	}
	var inspected []restoreInspection
	if err := json.Unmarshal(result.Stdout, &inspected); err != nil {
		return nil, fmt.Errorf("decode restored image inspection: %w", err)
	}
	return inspected, nil
}

func safeImageReference(reference string) bool {
	return reference != "" && !strings.HasPrefix(reference, "-") && !strings.ContainsAny(reference, " \t\r\n\x00")
}

func imagePlatform(platform domain.Platform) string {
	if platform.OS == "" && platform.Architecture == "" && platform.Variant == "" {
		return ""
	}
	if platform.OS == "" || platform.Architecture == "" {
		return ""
	}
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}
	return value
}

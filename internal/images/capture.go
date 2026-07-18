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
	executor ports.WorkspaceExecutor
	catalog  ports.RegistryCatalog
	clock    ports.Clock
}

func NewCapturer(executor ports.WorkspaceExecutor, catalog ports.RegistryCatalog, clock ports.Clock) *Capturer {
	return &Capturer{executor: executor, catalog: catalog, clock: clock}
}

func (c *Capturer) Capture(ctx context.Context, request CaptureRequest) (domain.ImageInventory, error) {
	if c == nil || c.executor == nil || c.catalog == nil || c.clock == nil || request.Capsule == "" || request.RegistryEndpoint == "" {
		return domain.ImageInventory{}, errors.New("image capture dependencies or request are incomplete")
	}
	if request.Previous.SchemaVersion != 0 && request.Previous.SchemaVersion != domain.SchemaVersion {
		return domain.ImageInventory{}, errors.New("previous image inventory schema is unsupported")
	}
	engine, err := NewDetector(c.executor).Detect(ctx, request.Scope)
	if err != nil {
		return domain.ImageInventory{}, err
	}
	engineImages, err := NewInventory().Enumerate(ctx, engine, InventoryOptions{ExcludeTags: request.ExcludeTags})
	if err != nil {
		return domain.ImageInventory{}, err
	}
	assigned, err := AssignReferences(request.RegistryAuthority, request.Capsule, engineImages)
	if err != nil {
		return domain.ImageInventory{}, err
	}
	previous, err := indexPrevious(request.Previous.Images)
	if err != nil {
		return domain.ImageInventory{}, err
	}
	for index := range assigned {
		image := &assigned[index]
		repository, tag, err := splitCapturedTag(image.CapturedReference)
		if err != nil {
			return domain.ImageInventory{}, err
		}
		known, wasCaptured := previous[captureIdentity(*image)]
		resolved, resolveErr := c.catalog.Resolve(ctx, request.RegistryEndpoint, repository, tag)
		switch {
		case resolveErr == nil && wasCaptured && known.CapturedManifestDigest == resolved:
			image.CapturedManifestDigest = resolved
			continue
		case resolveErr == nil && !wasCaptured:
			if err := verifyExistingCapture(ctx, engine, *image, repository, resolved); err != nil {
				return domain.ImageInventory{}, err
			}
			image.CapturedManifestDigest = resolved
			continue
		case resolveErr == nil:
			return domain.ImageInventory{}, fmt.Errorf("captured reference %q already resolves to %s: %w", image.CapturedReference, resolved, ErrCapturedDigestMismatch)
		case !errors.Is(resolveErr, ports.ErrNotFound):
			return domain.ImageInventory{}, resolveErr
		}
		if err := c.pushAndVerify(ctx, engine, image, request.RegistryEndpoint, repository, tag); err != nil {
			return domain.ImageInventory{}, err
		}
	}
	return domain.ImageInventory{SchemaVersion: domain.SchemaVersion, GeneratedAt: c.clock.Now().UTC(), Images: assigned}, nil
}

func verifyExistingCapture(ctx context.Context, engine Engine, image domain.Image, repository, resolved string) error {
	inspection, err := engine.run(ctx, "image", "inspect", image.EngineImageID)
	if err != nil || inspection.ExitCode != 0 {
		return commandError("inspect existing captured image", inspection.ExitCode, err)
	}
	var inspected []struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(inspection.Stdout, &inspected); err != nil || len(inspected) != 1 || inspected[0].ID != image.EngineImageID {
		return fmt.Errorf("existing captured reference lacks the exact local image identity: %w", ErrCapturedDigestMismatch)
	}
	wantRepoDigest := strings.SplitN(image.CapturedReference, "/", 2)[0] + "/" + repository + "@" + resolved
	if !containsString(inspected[0].RepoDigests, wantRepoDigest) {
		return fmt.Errorf("existing captured reference lacks verified repo digest %q: %w", wantRepoDigest, ErrCapturedDigestMismatch)
	}
	return nil
}

func (c *Capturer) pushAndVerify(ctx context.Context, engine Engine, image *domain.Image, endpoint, repository, tag string) (resultErr error) {
	result, err := engine.run(ctx, "image", "tag", image.EngineImageID, image.CapturedReference)
	if err != nil || result.ExitCode != 0 {
		return commandError("tag captured image", result.ExitCode, err)
	}
	tagged := true
	defer func() {
		if !tagged {
			return
		}
		cleanup, err := engine.run(context.WithoutCancel(ctx), "image", "rm", image.CapturedReference)
		if err != nil || cleanup.ExitCode != 0 {
			resultErr = errors.Join(resultErr, commandError("remove temporary captured image tag", cleanup.ExitCode, err))
		}
	}()
	result, err = engine.run(ctx, "image", "push", image.CapturedReference)
	if err != nil || result.ExitCode != 0 {
		return commandError("push captured image", result.ExitCode, err)
	}
	inspection, err := engine.run(ctx, "image", "inspect", image.CapturedReference)
	if err != nil || inspection.ExitCode != 0 {
		return commandError("inspect pushed image", inspection.ExitCode, err)
	}
	var inspected []struct {
		ID          string   `json:"Id"`
		RepoDigests []string `json:"RepoDigests"`
	}
	if err := json.Unmarshal(inspection.Stdout, &inspected); err != nil || len(inspected) != 1 || inspected[0].ID != image.EngineImageID {
		return errors.New("pushed image inspection identity mismatch")
	}
	resolved, err := c.catalog.Resolve(ctx, endpoint, repository, tag)
	if err != nil {
		return err
	}
	wantRepoDigest := strings.SplitN(image.CapturedReference, "/", 2)[0] + "/" + repository + "@" + resolved
	if !containsString(inspected[0].RepoDigests, wantRepoDigest) {
		return fmt.Errorf("workspace engine lacks verified repo digest %q: %w", wantRepoDigest, ErrCapturedDigestMismatch)
	}
	image.CapturedManifestDigest = resolved
	return nil
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

func indexPrevious(images []domain.Image) (map[string]domain.Image, error) {
	result := make(map[string]domain.Image, len(images))
	for _, image := range images {
		key := captureIdentity(image)
		if key == "\x00" || image.CapturedManifestDigest == "" {
			return nil, errors.New("previous image inventory contains an incomplete capture")
		}
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("previous image inventory contains a duplicate capture")
		}
		result[key] = image
	}
	return result, nil
}

func captureIdentity(image domain.Image) string {
	return image.EngineImageID + "\x00" + image.CapturedReference
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

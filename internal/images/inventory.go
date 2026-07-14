package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joshyorko/camp/internal/domain"
)

const ExcludeLabel = "dev.camp.exclude"
const maxInspectBatch = 128

type EngineImage struct {
	ID          string
	Tags        []string
	RepoDigests []string
	Platform    domain.Platform
	CreatedAt   time.Time
}

type InventoryOptions struct {
	ExcludeTags []string
}

type Inventory struct{}

func NewInventory() *Inventory { return &Inventory{} }

type inspectedImage struct {
	ID           string            `json:"Id"`
	RepoTags     []string          `json:"RepoTags"`
	RepoDigests  []string          `json:"RepoDigests"`
	Created      string            `json:"Created"`
	OS           string            `json:"Os"`
	Architecture string            `json:"Architecture"`
	Variant      string            `json:"Variant"`
	Labels       map[string]string `json:"Labels"`
	Config       inspectedConfig   `json:"Config"`
}

type inspectedConfig struct {
	Labels map[string]string `json:"Labels"`
}

func (i *Inventory) Enumerate(ctx context.Context, engine Engine, options InventoryOptions) ([]EngineImage, error) {
	if i == nil {
		return nil, errors.New("image inventory is nil")
	}
	listed, err := engine.run(ctx, "image", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil || listed.ExitCode != 0 {
		return nil, commandError("list workspace images", listed.ExitCode, err)
	}
	ids := uniqueLines(string(listed.Stdout))
	if len(ids) == 0 {
		return []EngineImage{}, nil
	}
	inspected := make([]inspectedImage, 0, len(ids))
	for start := 0; start < len(ids); start += maxInspectBatch {
		end := start + maxInspectBatch
		if end > len(ids) {
			end = len(ids)
		}
		args := append([]string{"image", "inspect"}, ids[start:end]...)
		result, err := engine.run(ctx, args...)
		if err != nil || result.ExitCode != 0 {
			return nil, commandError("inspect workspace images", result.ExitCode, err)
		}
		var batch []inspectedImage
		if err := json.Unmarshal(result.Stdout, &batch); err != nil {
			return nil, fmt.Errorf("decode workspace image inspection: %w", err)
		}
		inspected = append(inspected, batch...)
	}
	excluded := make(map[string]struct{}, len(options.ExcludeTags))
	for _, tag := range options.ExcludeTags {
		excluded[tag] = struct{}{}
	}
	requested := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		requested[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(inspected))
	images := make([]EngineImage, 0, len(inspected))
	for _, image := range inspected {
		if _, ok := requested[image.ID]; !ok || image.ID == "" {
			return nil, errors.New("image inspection returned an unexpected identity")
		}
		if _, duplicate := seen[image.ID]; duplicate {
			return nil, errors.New("image inspection returned a duplicate identity")
		}
		seen[image.ID] = struct{}{}
		if optedOut(image.Labels) || optedOut(image.Config.Labels) {
			continue
		}
		tags := make([]string, 0, len(image.RepoTags))
		for _, tag := range image.RepoTags {
			if tag == "" || tag == "<none>:<none>" {
				continue
			}
			if _, skip := excluded[tag]; skip {
				continue
			}
			tags = append(tags, tag)
		}
		tags = sortedUnique(tags)
		if len(tags) == 0 {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, image.Created)
		if err != nil {
			return nil, fmt.Errorf("parse image %q creation time: %w", image.ID, err)
		}
		if image.OS == "" || image.Architecture == "" {
			return nil, fmt.Errorf("image %q lacks platform metadata", image.ID)
		}
		images = append(images, EngineImage{
			ID: image.ID, Tags: tags, RepoDigests: sortedUnique(image.RepoDigests),
			Platform: domain.Platform{OS: image.OS, Architecture: image.Architecture, Variant: image.Variant}, CreatedAt: created.UTC(),
		})
	}
	if len(seen) != len(requested) {
		return nil, errors.New("image inspection omitted a listed identity")
	}
	sort.Slice(images, func(left, right int) bool { return images[left].ID < images[right].ID })
	return images, nil
}

func uniqueLines(value string) []string {
	items := make([]string, 0)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			items = append(items, line)
		}
	}
	return sortedUnique(items)
}

func sortedUnique(items []string) []string {
	seen := make(map[string]struct{}, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func optedOut(labels map[string]string) bool {
	value := strings.ToLower(strings.TrimSpace(labels[ExcludeLabel]))
	return value == "true" || value == "1" || value == "yes"
}

func commandError(operation string, exitCode int, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s exited %d", operation, exitCode)
}

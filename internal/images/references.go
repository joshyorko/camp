package images

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/joshyorko/camp/internal/domain"
)

const maxCapturedReferenceLength = 240

func AssignReferences(registryAuthority, capsule string, engineImages []EngineImage) ([]domain.Image, error) {
	if err := validateRegistryAuthority(registryAuthority); err != nil {
		return nil, err
	}
	if capsule == "" {
		return nil, errors.New("capsule image namespace is empty")
	}
	images := make([]domain.Image, 0, len(engineImages))
	seen := make(map[string]struct{}, len(engineImages))
	for _, engineImage := range engineImages {
		tags := withoutCampReferences(registryAuthority, engineImage.Tags)
		digests := withoutCampReferences(registryAuthority, engineImage.RepoDigests)
		if len(tags) == 0 {
			continue
		}
		if engineImage.ID == "" || len(tags) == 0 || engineImage.Platform.OS == "" || engineImage.Platform.Architecture == "" || engineImage.CreatedAt.IsZero() {
			return nil, errors.New("engine image lacks identity, tag, platform, or creation time")
		}
		canonical, err := json.Marshal(struct {
			ID       string          `json:"id"`
			Tags     []string        `json:"tags"`
			Platform domain.Platform `json:"platform"`
		}{engineImage.ID, tags, engineImage.Platform})
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(canonical)
		capsuleDigest := sha256.Sum256([]byte(capsule))
		repository := fmt.Sprintf("camp/%s-%s/%s-%s",
			slug(capsule, 24), hex.EncodeToString(capsuleDigest[:6]),
			slug(tags[0], 40), hex.EncodeToString(digest[:]),
		)
		reference := registryAuthority + "/" + repository + ":captured"
		if len(reference) > maxCapturedReferenceLength {
			return nil, errors.New("captured image reference exceeds safe length")
		}
		if _, collision := seen[reference]; collision {
			return nil, errors.New("captured image reference collision")
		}
		seen[reference] = struct{}{}
		images = append(images, domain.Image{
			EngineImageID: engineImage.ID, OriginalTags: tags, OriginalRepoDigests: digests,
			CapturedReference: reference, Platform: engineImage.Platform, Source: domain.ImageSourceRegistry, CreatedAt: engineImage.CreatedAt.UTC(),
		})
	}
	sort.Slice(images, func(left, right int) bool { return images[left].CapturedReference < images[right].CapturedReference })
	return images, nil
}

func RewriteRegistryAuthority(reference, authority string) (string, error) {
	if err := validateRegistryAuthority(authority); err != nil {
		return "", err
	}
	if strings.Contains(reference, "://") || strings.ContainsAny(reference, " \t\r\n\x00") {
		return "", errors.New("invalid captured image reference")
	}
	separator := strings.IndexByte(reference, '/')
	if separator <= 0 || separator == len(reference)-1 {
		return "", errors.New("captured image reference has no registry path")
	}
	if err := validateRegistryAuthority(reference[:separator]); err != nil {
		return "", err
	}
	if err := validateReferencePath(reference[separator+1:]); err != nil {
		return "", err
	}
	return authority + reference[separator:], nil
}

func validateRegistryAuthority(authority string) error {
	if authority == "" || len(authority) > 100 || strings.Contains(authority, "://") || strings.ContainsAny(authority, "/@?#%\\ \t\r\n\x00") {
		return errors.New("invalid registry authority")
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || host == "" {
		return errors.New("registry authority requires a host and port")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return errors.New("registry authority has an invalid port")
	}
	return nil
}

func validateReferencePath(value string) error {
	if value == "" || strings.ContainsAny(value, "?#%\\ \t\r\n\x00") {
		return errors.New("invalid captured image path")
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("invalid captured image path component")
		}
	}
	return nil
}

func withoutCampReferences(authority string, references []string) []string {
	prefix := authority + "/camp/"
	filtered := make([]string, 0, len(references))
	for _, reference := range references {
		if !strings.HasPrefix(reference, prefix) {
			filtered = append(filtered, reference)
		}
	}
	return sortedUnique(filtered)
}

func slug(value string, limit int) string {
	var output strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			output.WriteRune(character)
			separator = false
		} else if output.Len() > 0 && !separator {
			output.WriteByte('-')
			separator = true
		}
		if output.Len() >= limit {
			break
		}
	}
	result := strings.Trim(output.String(), "-")
	if result == "" {
		return "image"
	}
	return result
}

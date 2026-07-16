package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/joshyorko/camp/internal/ports"
)

var ErrRegistryDigestMismatch = errors.New("registry manifest digest mismatch")

const (
	maxCatalogBody  = 1 << 20
	maxManifestBody = 8 << 20
	maxPages        = 10000
)

var (
	repositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)
	tagPattern        = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type Catalog struct {
	client   *http.Client
	pageSize int
}

func NewCatalog(client *http.Client, pageSize int) *Catalog {
	return &Catalog{client: client, pageSize: pageSize}
}

func (c *Catalog) List(ctx context.Context, endpoint string) ([]ports.RegistryReference, error) {
	base, err := c.validate(endpoint)
	if err != nil {
		return nil, err
	}
	first := *base
	first.Path = "/v2/_catalog"
	first.RawQuery = url.Values{"n": []string{strconv.Itoa(c.pageSize)}}.Encode()
	var repositories []string
	if err := c.pages(ctx, base, &first, func(body []byte) error {
		var page struct {
			Repositories []string `json:"repositories"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decode registry catalog: %w", err)
		}
		repositories = append(repositories, page.Repositories...)
		return nil
	}); err != nil {
		return nil, err
	}
	repositories = uniqueSorted(repositories)
	result := make([]ports.RegistryReference, 0)
	for _, repository := range repositories {
		if !repositoryPattern.MatchString(repository) {
			return nil, fmt.Errorf("registry returned invalid repository %q", repository)
		}
		tags, err := c.tags(ctx, base, repository)
		if err != nil {
			return nil, err
		}
		for _, tag := range tags {
			digest, err := c.Resolve(ctx, endpoint, repository, tag)
			if err != nil {
				return nil, err
			}
			result = append(result, ports.RegistryReference{Repository: repository, Tag: tag, ManifestDigest: digest})
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Repository == result[right].Repository {
			return result[left].Tag < result[right].Tag
		}
		return result[left].Repository < result[right].Repository
	})
	return result, nil
}

func (c *Catalog) Resolve(ctx context.Context, endpoint, repository, reference string) (string, error) {
	base, err := c.validate(endpoint)
	if err != nil {
		return "", err
	}
	if !repositoryPattern.MatchString(repository) || (!tagPattern.MatchString(reference) && !digestPattern.MatchString(reference)) {
		return "", errors.New("invalid registry manifest reference")
	}
	target := *base
	target.Path = "/v2/" + repository + "/manifests/" + reference
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	response, err := c.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("read registry manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("registry manifest %s/%s: %w", repository, reference, ports.ErrNotFound)
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read registry manifest: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxManifestBody+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxManifestBody {
		return "", errors.New("registry manifest exceeds size limit")
	}
	header := strings.ToLower(strings.TrimSpace(response.Header.Get("Docker-Content-Digest")))
	if !digestPattern.MatchString(header) {
		return "", fmt.Errorf("registry manifest has invalid digest: %w", ErrRegistryDigestMismatch)
	}
	digest := sha256.Sum256(body)
	computed := "sha256:" + hex.EncodeToString(digest[:])
	if computed != header || (digestPattern.MatchString(reference) && reference != header) {
		return "", fmt.Errorf("resolved %s, header %s, bytes %s: %w", reference, header, computed, ErrRegistryDigestMismatch)
	}
	return header, nil
}

func (c *Catalog) tags(ctx context.Context, base *url.URL, repository string) ([]string, error) {
	first := *base
	first.Path = "/v2/" + repository + "/tags/list"
	first.RawQuery = url.Values{"n": []string{strconv.Itoa(c.pageSize)}}.Encode()
	var tags []string
	err := c.pages(ctx, base, &first, func(body []byte) error {
		var page struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(body, &page); err != nil {
			return fmt.Errorf("decode registry tags: %w", err)
		}
		if page.Name != repository {
			return errors.New("registry tag page identity mismatch")
		}
		tags = append(tags, page.Tags...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	tags = uniqueSorted(tags)
	for _, tag := range tags {
		if !tagPattern.MatchString(tag) {
			return nil, fmt.Errorf("registry returned invalid tag %q", tag)
		}
	}
	return tags, nil
}

func (c *Catalog) pages(ctx context.Context, base, first *url.URL, consume func([]byte) error) error {
	seen := make(map[string]struct{})
	current := first
	for page := 0; current != nil; page++ {
		if page >= maxPages {
			return errors.New("registry pagination exceeds page limit")
		}
		key := current.String()
		if _, duplicate := seen[key]; duplicate {
			return errors.New("registry pagination loop")
		}
		seen[key] = struct{}{}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, key, nil)
		if err != nil {
			return err
		}
		response, err := c.client.Do(request)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxCatalogBody+1))
		closeErr := response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if response.StatusCode != http.StatusOK {
			return fmt.Errorf("registry pagination returned HTTP %d", response.StatusCode)
		}
		if len(body) > maxCatalogBody {
			return errors.New("registry pagination body exceeds size limit")
		}
		if err := consume(body); err != nil {
			return err
		}
		current, err = nextLink(base, response.Header.Values("Link"))
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Catalog) validate(endpoint string) (*url.URL, error) {
	if c == nil || c.client == nil || c.pageSize < 1 || c.pageSize > 1000 {
		return nil, errors.New("registry catalog dependencies are incomplete")
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, errors.New("invalid registry catalog endpoint")
	}
	parsed.Path = ""
	return parsed, nil
}

func nextLink(base *url.URL, values []string) (*url.URL, error) {
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			item = strings.TrimSpace(item)
			parts := strings.Split(item, ";")
			if len(parts) < 2 || !strings.Contains(strings.Join(parts[1:], ";"), `rel="next"`) {
				continue
			}
			raw := strings.TrimSpace(parts[0])
			if len(raw) < 3 || raw[0] != '<' || raw[len(raw)-1] != '>' {
				return nil, errors.New("invalid registry pagination link")
			}
			parsed, err := url.Parse(raw[1 : len(raw)-1])
			if err != nil {
				return nil, err
			}
			resolved := base.ResolveReference(parsed)
			if resolved.Scheme != base.Scheme || resolved.Host != base.Host || !strings.HasPrefix(resolved.Path, "/v2/") {
				return nil, errors.New("registry pagination escaped endpoint")
			}
			return resolved, nil
		}
	}
	return nil, nil
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

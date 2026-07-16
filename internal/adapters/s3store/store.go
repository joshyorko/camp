package s3store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/joshyorko/camp/internal/ports"
)

const maxErrorBody = 4 << 10

type Signer interface {
	Sign(*http.Request) error
}

type SignFunc func(*http.Request) error

func (f SignFunc) Sign(request *http.Request) error { return f(request) }

// Config deliberately contains no credentials. Authentication material belongs
// to the injected Signer and must not enter durable Camp configuration.
type Config struct {
	Endpoint   string
	Bucket     string
	Prefix     string
	PathStyle  bool
	HTTPClient *http.Client
	Signer     Signer
}

type Store struct {
	endpoint  *url.URL
	bucket    string
	prefix    string
	pathStyle bool
	client    *http.Client
	signer    Signer
}

var _ ports.ObjectStore = (*Store)(nil)

func New(config Config) (*Store, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Scheme != "http" && endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("invalid S3 endpoint: %w", ports.ErrInvalidKey)
	}
	if config.Bucket == "" || strings.ContainsAny(config.Bucket, "/\\") {
		return nil, fmt.Errorf("invalid S3 bucket: %w", ports.ErrInvalidKey)
	}
	prefix := strings.Trim(config.Prefix, "/")
	if prefix != "" {
		if err := validateKey(prefix); err != nil {
			return nil, fmt.Errorf("invalid S3 prefix: %w", err)
		}
		prefix += "/"
	}
	client := config.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Store{
		endpoint:  endpoint,
		bucket:    config.Bucket,
		prefix:    prefix,
		pathStyle: config.PathStyle,
		client:    client,
		signer:    config.Signer,
	}, nil
}

func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, ports.ObjectMeta, error) {
	request, err := s.newObjectRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, ports.ObjectMeta{}, err
	}
	response, err := s.do(request)
	if err != nil {
		return nil, ports.ObjectMeta{}, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, ports.ObjectMeta{}, responseError(response, "get", key, false)
	}
	meta, err := objectMeta(key, response.Header)
	if err != nil {
		response.Body.Close()
		return nil, ports.ObjectMeta{}, err
	}
	return response.Body, meta, nil
}

func (s *Store) Head(ctx context.Context, key string) (ports.ObjectMeta, error) {
	request, err := s.newObjectRequest(ctx, http.MethodHead, key, nil)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	response, err := s.do(request)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ports.ObjectMeta{}, responseError(response, "head", key, false)
	}
	return objectMeta(key, response.Header)
}

func (s *Store) PutImmutable(context.Context, string, ports.RestartableSource, string, int64) (ports.ObjectMeta, error) {
	return ports.ObjectMeta{}, errors.New("S3 multipart immutable upload is not implemented")
}

func (s *Store) PutConditional(ctx context.Context, key string, body []byte, condition ports.WriteCondition) (ports.ObjectMeta, error) {
	if condition.MustBeAbsent == (condition.MatchRevision != "") {
		return ports.ObjectMeta{}, fmt.Errorf("conditional write for %q: %w", key, ports.ErrInvalidCondition)
	}
	request, err := s.newObjectRequest(ctx, http.MethodPut, key, bytes.NewReader(body))
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	request.ContentLength = int64(len(body))
	if condition.MustBeAbsent {
		request.Header.Set("If-None-Match", "*")
	} else {
		request.Header.Set("If-Match", string(condition.MatchRevision))
	}
	response, err := s.do(request)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ports.ObjectMeta{}, responseError(response, "put", key, true)
	}
	revision := ports.Revision(response.Header.Get("ETag"))
	if revision == "" {
		return ports.ObjectMeta{}, fmt.Errorf("put object %q returned no opaque revision: %w", key, ports.ErrIntegrity)
	}
	digest := sha256.Sum256(body)
	return ports.ObjectMeta{Key: key, Size: int64(len(body)), Revision: revision, SHA256: hex.EncodeToString(digest[:])}, nil
}

func (s *Store) DeleteConditional(ctx context.Context, key string, expected ports.Revision) error {
	if expected == "" {
		return fmt.Errorf("conditional delete for %q: %w", key, ports.ErrInvalidCondition)
	}
	request, err := s.newObjectRequest(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	request.Header.Set("If-Match", string(expected))
	response, err := s.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response, "delete", key, true)
	}
	return nil
}

func (s *Store) List(ctx context.Context, prefix, pageToken string) ([]ports.ObjectMeta, string, error) {
	if prefix != "" {
		if err := validateKey(strings.TrimSuffix(prefix, "/")); err != nil {
			return nil, "", err
		}
	}
	requestURL := s.bucketURL()
	query := requestURL.Query()
	query.Set("list-type", "2")
	query.Set("prefix", s.prefix+prefix)
	if pageToken != "" {
		query.Set("continuation-token", pageToken)
	}
	requestURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, "", err
	}
	response, err := s.do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", responseError(response, "list", prefix, false)
	}
	var result struct {
		Contents []struct {
			Key          string `xml:"Key"`
			ETag         string `xml:"ETag"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		} `xml:"Contents"`
		Next string `xml:"NextContinuationToken"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&result); err != nil {
		return nil, "", fmt.Errorf("decode S3 list response: %w", err)
	}
	items := make([]ports.ObjectMeta, 0, len(result.Contents))
	for _, object := range result.Contents {
		if !strings.HasPrefix(object.Key, s.prefix) {
			return nil, "", fmt.Errorf("listed object escaped configured prefix: %w", ports.ErrIntegrity)
		}
		key := strings.TrimPrefix(object.Key, s.prefix)
		if err := validateKey(key); err != nil {
			return nil, "", fmt.Errorf("listed invalid object key: %w", ports.ErrIntegrity)
		}
		modified, err := parseTime(object.LastModified)
		if err != nil {
			return nil, "", fmt.Errorf("listed object %q has invalid modification time: %w", key, ports.ErrIntegrity)
		}
		if object.ETag == "" || object.Size < 0 {
			return nil, "", fmt.Errorf("listed object %q has incomplete metadata: %w", key, ports.ErrIntegrity)
		}
		items = append(items, ports.ObjectMeta{Key: key, Size: object.Size, Revision: ports.Revision(object.ETag), Modified: modified})
	}
	return items, result.Next, nil
}

func (s *Store) do(request *http.Request) (*http.Response, error) {
	if s.signer != nil {
		if err := s.signer.Sign(request); err != nil {
			return nil, fmt.Errorf("sign S3 %s request: %w", request.Method, err)
		}
	}
	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("perform S3 %s request: %w", request.Method, err)
	}
	return response, nil
}

func (s *Store) newObjectRequest(ctx context.Context, method, key string, body io.Reader) (*http.Request, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	requestURL := s.objectURL(s.prefix + key)
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create S3 %s request: %w", method, err)
	}
	return request, nil
}

func (s *Store) bucketURL() *url.URL {
	result := *s.endpoint
	if s.pathStyle {
		result.Path = path.Join(result.Path, s.bucket)
		result.RawPath = ""
	} else {
		result.Host = s.bucket + "." + result.Host
	}
	return &result
}

func (s *Store) objectURL(key string) *url.URL {
	result := s.bucketURL()
	baseRawPath := result.EscapedPath()
	segments := strings.Split(key, "/")
	for index := range segments {
		segments[index] = url.PathEscape(segments[index])
	}
	escaped := strings.Join(segments, "/")
	result.Path = strings.TrimSuffix(result.Path, "/") + "/" + key
	result.RawPath = strings.TrimSuffix(baseRawPath, "/") + "/" + escaped
	return result
}

func objectMeta(key string, header http.Header) (ports.ObjectMeta, error) {
	revision := ports.Revision(header.Get("ETag"))
	if revision == "" {
		return ports.ObjectMeta{}, fmt.Errorf("object %q has no opaque revision: %w", key, ports.ErrIntegrity)
	}
	size, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64)
	if err != nil || size < 0 {
		return ports.ObjectMeta{}, fmt.Errorf("object %q has invalid size: %w", key, ports.ErrIntegrity)
	}
	modified, err := parseTime(header.Get("Last-Modified"))
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("object %q has invalid modification time: %w", key, ports.ErrIntegrity)
	}
	return ports.ObjectMeta{
		Key: key, Size: size, Revision: revision,
		SHA256: header.Get("X-Amz-Meta-Sha256"), Modified: modified,
	}, nil
}

func parseTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	return http.ParseTime(value)
}

func validateKey(key string) error {
	if key == "" || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") || strings.ContainsRune(key, '\x00') || strings.ContainsRune(key, '\\') {
		return fmt.Errorf("invalid S3 object key %q: %w", key, ports.ErrInvalidKey)
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid S3 object key %q: %w", key, ports.ErrInvalidKey)
		}
	}
	return nil
}

func responseError(response *http.Response, operation, key string, mutation bool) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	detail := strings.TrimSpace(string(body))
	var sentinel error
	switch response.StatusCode {
	case http.StatusNotFound:
		sentinel = ports.ErrNotFound
	case http.StatusConflict, http.StatusPreconditionFailed:
		sentinel = ports.ErrConflict
	default:
		if mutation && response.StatusCode >= 500 {
			sentinel = ports.ErrAmbiguous
		} else {
			sentinel = fmt.Errorf("S3 request failed")
		}
	}
	if detail == "" {
		return fmt.Errorf("S3 %s %q returned %s: %w", operation, key, response.Status, sentinel)
	}
	return fmt.Errorf("S3 %s %q returned %s (%s): %w", operation, key, response.Status, detail, sentinel)
}

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

func (s *Store) PutImmutable(ctx context.Context, key string, source ports.RestartableSource, expectedSHA256 string, expectedSize int64) (meta ports.ObjectMeta, resultErr error) {
	if err := validateKey(key); err != nil {
		return ports.ObjectMeta{}, err
	}
	if source == nil || expectedSize < 0 || !validSHA256(expectedSHA256) {
		return ports.ObjectMeta{}, fmt.Errorf("immutable object %q expectations: %w", key, ports.ErrIntegrity)
	}
	current, err := s.Head(ctx, key)
	switch {
	case err == nil:
		if current.Size == expectedSize && current.SHA256 == expectedSHA256 {
			if err := s.verifyRemote(ctx, key, current.Revision, expectedSHA256, expectedSize); err != nil {
				return ports.ObjectMeta{}, err
			}
			return current, nil
		}
		return ports.ObjectMeta{}, fmt.Errorf("immutable object %q contains different bytes: %w", key, ports.ErrConflict)
	case !errors.Is(err, ports.ErrNotFound):
		return ports.ObjectMeta{}, err
	}
	if err := verifySource(ctx, key, source, expectedSHA256, expectedSize); err != nil {
		return ports.ObjectMeta{}, err
	}

	uploadID, err := s.createMultipart(ctx, key, expectedSHA256)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	completed := false
	defer func() {
		if !completed {
			if abortErr := s.abortMultipart(key, uploadID); abortErr != nil {
				resultErr = errors.Join(resultErr, abortErr)
			}
		}
	}()
	parts, err := s.uploadParts(ctx, key, uploadID, source, expectedSize)
	if err != nil {
		return ports.ObjectMeta{}, err
	}
	if err := s.completeMultipart(ctx, key, uploadID, parts); err != nil {
		if errors.Is(err, ports.ErrAmbiguous) {
			observed, observeErr := s.Head(context.WithoutCancel(ctx), key)
			if observeErr == nil && observed.Size == expectedSize && observed.SHA256 == expectedSHA256 {
				if verifyErr := s.verifyRemote(context.WithoutCancel(ctx), key, observed.Revision, expectedSHA256, expectedSize); verifyErr == nil {
					completed = true
					return observed, nil
				}
			}
			// Completion may have committed remotely. Never abort an upload whose
			// outcome cannot be distinguished from success.
			completed = true
		}
		return ports.ObjectMeta{}, err
	}
	completed = true
	meta, err = s.Head(ctx, key)
	if err != nil {
		return ports.ObjectMeta{}, fmt.Errorf("read back immutable object %q: %w", key, err)
	}
	if meta.Size != expectedSize || meta.SHA256 != expectedSHA256 {
		return ports.ObjectMeta{}, fmt.Errorf("immutable object %q readback has size %d and sha256 %s: %w", key, meta.Size, meta.SHA256, ports.ErrIntegrity)
	}
	return meta, nil
}

func (s *Store) verifyRemote(ctx context.Context, key string, revision ports.Revision, expectedSHA256 string, expectedSize int64) error {
	reader, meta, err := s.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read immutable object %q for verification: %w", key, err)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: reader})
	closeErr := reader.Close()
	if copyErr != nil {
		return fmt.Errorf("verify immutable object %q: %w", key, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close immutable object %q: %w", key, closeErr)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if meta.Revision != revision || size != expectedSize || digest != expectedSHA256 {
		return fmt.Errorf("immutable object %q readback has revision %q, size %d, and sha256 %s: %w", key, meta.Revision, size, digest, ports.ErrIntegrity)
	}
	return nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func verifySource(ctx context.Context, key string, source ports.RestartableSource, expectedSHA256 string, expectedSize int64) error {
	reader, err := source.Open()
	if err != nil {
		return fmt.Errorf("open immutable source for %q: %w", key, err)
	}
	if reader == nil {
		return fmt.Errorf("open immutable source for %q: nil reader: %w", key, ports.ErrIntegrity)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: reader})
	closeErr := reader.Close()
	if copyErr != nil {
		return fmt.Errorf("verify immutable source for %q: %w", key, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close immutable source for %q: %w", key, closeErr)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if size != expectedSize || digest != expectedSHA256 {
		return fmt.Errorf("immutable source for %q has size %d and sha256 %s: %w", key, size, digest, ports.ErrIntegrity)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func (s *Store) createMultipart(ctx context.Context, key, digest string) (string, error) {
	request, err := s.multipartRequest(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("X-Amz-Meta-Sha256", digest)
	response, err := s.do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", responseError(response, "create multipart upload", key, true)
	}
	var result struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || result.UploadID == "" {
		return "", fmt.Errorf("decode multipart upload for %q: %w", key, ports.ErrIntegrity)
	}
	return result.UploadID, nil
}

type completedPart struct {
	ETag       string `xml:"ETag"`
	PartNumber int    `xml:"PartNumber"`
}

const multipartPartSize int64 = 8 << 20

func (s *Store) uploadParts(ctx context.Context, key, uploadID string, source ports.RestartableSource, size int64) ([]completedPart, error) {
	reader, err := source.Open()
	if err != nil {
		return nil, fmt.Errorf("reopen immutable source for %q: %w", key, err)
	}
	if reader == nil {
		return nil, fmt.Errorf("reopen immutable source for %q: nil reader: %w", key, ports.ErrIntegrity)
	}
	defer reader.Close()
	parts := make([]completedPart, 0, int(size/multipartPartSize)+1)
	remaining := size
	for number := 1; remaining > 0 || number == 1; number++ {
		partSize := min(remaining, multipartPartSize)
		request, err := s.multipartRequest(ctx, http.MethodPut, key, url.Values{"partNumber": {strconv.Itoa(number)}, "uploadId": {uploadID}}, io.LimitReader(reader, partSize))
		if err != nil {
			return nil, err
		}
		request.ContentLength = partSize
		response, err := s.do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			err := responseError(response, "upload multipart part", key, true)
			response.Body.Close()
			return nil, err
		}
		etag := response.Header.Get("ETag")
		response.Body.Close()
		if etag == "" {
			return nil, fmt.Errorf("multipart part %d for %q returned no opaque revision: %w", number, key, ports.ErrIntegrity)
		}
		parts = append(parts, completedPart{ETag: etag, PartNumber: number})
		remaining -= partSize
	}
	return parts, nil
}

func (s *Store) completeMultipart(ctx context.Context, key, uploadID string, parts []completedPart) error {
	body, err := xml.Marshal(struct {
		XMLName xml.Name        `xml:"CompleteMultipartUpload"`
		Parts   []completedPart `xml:"Part"`
	}{Parts: parts})
	if err != nil {
		return err
	}
	request, err := s.multipartRequest(ctx, http.MethodPost, key, url.Values{"uploadId": {uploadID}}, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("If-None-Match", "*")
	response, err := s.do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response, "complete multipart upload", key, true)
	}
	var result struct {
		XMLName xml.Name
		ETag    string `xml:"ETag"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil || result.XMLName.Local != "CompleteMultipartUploadResult" || result.ETag == "" {
		return fmt.Errorf("decode completed multipart upload for %q: %w", key, ports.ErrIntegrity)
	}
	return nil
}

func (s *Store) abortMultipart(key, uploadID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := s.multipartRequest(ctx, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil)
	if err != nil {
		return err
	}
	response, err := s.do(request)
	if err != nil {
		return fmt.Errorf("abort multipart upload for %q: %w", key, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response, "abort multipart upload", key, true)
	}
	return nil
}

func (s *Store) multipartRequest(ctx context.Context, method, key string, query url.Values, body io.Reader) (*http.Request, error) {
	request, err := s.newObjectRequest(ctx, method, key, body)
	if err != nil {
		return nil, err
	}
	request.URL.RawQuery = query.Encode()
	return request, nil
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

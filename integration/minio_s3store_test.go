package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshyorko/camp/internal/adapters/s3store"
	"github.com/joshyorko/camp/internal/ports"
)

const minioImage = "minio/minio@sha256:a1ea29fa28355559ef137d71fc570e508a214ec84ff8083e39bc5428980b015e"

type minioFixture struct {
	endpoint string
	client   *http.Client
	signer   *minioSigner
}

func TestMinIOImmutableLifecycle(t *testing.T) {
	fixture := startMinIO(t)
	fixture.createBucket(t, "camp-test")
	store := fixture.store(t, fixture.client)

	t.Run("create, verified idempotence, and conflict", func(t *testing.T) {
		body := []byte("immutable MinIO generation")
		digest := sha256Hex(body)
		created, err := store.PutImmutable(context.Background(), "generations/create.tar.zst", bytesSource(body), digest, int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		verified, err := store.PutImmutable(context.Background(), "generations/create.tar.zst", bytesSource(body), digest, int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		if verified.Revision != created.Revision || verified.SHA256 != digest || verified.Size != int64(len(body)) {
			t.Fatalf("verified metadata = %#v, created = %#v", verified, created)
		}
		different := []byte("different immutable bytes")
		if _, err := store.PutImmutable(context.Background(), "generations/create.tar.zst", bytesSource(different), sha256Hex(different), int64(len(different))); !errors.Is(err, ports.ErrConflict) {
			t.Fatalf("conflicting PutImmutable error = %v, want conflict", err)
		}
	})

	t.Run("cancellation aborts remote multipart state", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		body := bytes.Repeat([]byte("a"), (8<<20)+1)
		source := &cancelOnUploadSource{body: body, cancel: cancel}
		if _, err := store.PutImmutable(ctx, "generations/canceled.tar.zst", source, sha256Hex(body), int64(len(body))); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled PutImmutable error = %v, want context canceled", err)
		}
		if uploads := fixture.listUploads(t, "camp-test", "generations/canceled.tar.zst"); len(uploads) != 0 {
			t.Fatalf("active multipart uploads after cancellation = %v", uploads)
		}
	})

	t.Run("lost completion response reconciles committed object", func(t *testing.T) {
		transport := &loseCompletionResponse{base: http.DefaultTransport}
		ambiguousStore := fixture.store(t, &http.Client{Transport: transport})
		body := []byte("committed before response loss")
		meta, err := ambiguousStore.PutImmutable(context.Background(), "generations/ambiguous.tar.zst", bytesSource(body), sha256Hex(body), int64(len(body)))
		if err != nil {
			t.Fatal(err)
		}
		if !transport.lost || meta.SHA256 != sha256Hex(body) {
			t.Fatalf("lost response = %v, metadata = %#v", transport.lost, meta)
		}
	})

	t.Run("conditional completion rejects an existing key", func(t *testing.T) {
		const key = "generations/conditional.tar.zst"
		uploadID := fixture.initiateUpload(t, "camp-test", key)
		etag := fixture.uploadPart(t, "camp-test", key, uploadID, []byte("contender"))
		fixture.putObject(t, "camp-test", key, []byte("winner"))
		status := fixture.completeUpload(t, "camp-test", key, uploadID, etag, true)
		if status != http.StatusPreconditionFailed {
			t.Fatalf("conditional completion status = %d, want %d", status, http.StatusPreconditionFailed)
		}
	})
}

func TestCreateMinIOBucketRetriesUntilServerAPIsInitialize(t *testing.T) {
	statuses := []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK}
	attempt := 0
	err := createMinIOBucketWhenReady(context.Background(), func() (*http.Response, error) {
		status := statuses[attempt]
		attempt++
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader("XMinioServerNotInitialized"))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempt != len(statuses) {
		t.Fatalf("bucket API attempts = %d, want %d", attempt, len(statuses))
	}
}

type bytesSource []byte

func (s bytesSource) Open() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(s)), nil }

type cancelOnUploadSource struct {
	mu     sync.Mutex
	body   []byte
	cancel context.CancelFunc
	opens  int
}

func (s *cancelOnUploadSource) Open() (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	if s.opens == 2 {
		return io.NopCloser(&cancelingReader{reader: bytes.NewReader(s.body), cancel: s.cancel}), nil
	}
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

type cancelingReader struct {
	reader io.Reader
	cancel context.CancelFunc
	done   bool
}

func (r *cancelingReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if !r.done {
		r.done = true
		r.cancel()
	}
	return n, err
}

type loseCompletionResponse struct {
	base http.RoundTripper
	lost bool
}

func (t *loseCompletionResponse) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || t.lost || request.Method != http.MethodPost || request.URL.Query().Get("uploadId") == "" || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	t.lost = true
	_ = response.Body.Close()
	return &http.Response{
		StatusCode: http.StatusInternalServerError,
		Status:     "500 Internal Server Error",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("simulated lost completion response")),
		Request:    request,
	}, nil
}

func startMinIO(t *testing.T) *minioFixture {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("real MinIO fixture requires docker: %v", err)
	}
	name := "camp-minio-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	accessKey := randomCredential(t)
	secretKey := randomCredential(t)
	output, err := exec.Command("docker", "run", "--detach", "--rm", "--name", name,
		"--publish", "127.0.0.1::9000", "--env", "MINIO_ROOT_USER="+accessKey,
		"--env", "MINIO_ROOT_PASSWORD="+secretKey, minioImage, "server", "/data", "--address", ":9000").CombinedOutput()
	if err != nil {
		t.Fatalf("start MinIO container: %v: %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", name).Run() })
	portOutput, err := exec.Command("docker", "port", name, "9000/tcp").CombinedOutput()
	if err != nil {
		t.Fatalf("resolve MinIO port: %v: %s", err, portOutput)
	}
	endpoint := "http://" + strings.TrimSpace(string(portOutput))
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(20 * time.Second)
	for {
		response, requestErr := client.Get(endpoint + "/minio/health/ready")
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("MinIO did not become ready: %v", requestErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &minioFixture{endpoint: endpoint, client: client, signer: &minioSigner{accessKey: accessKey, secretKey: secretKey}}
}

func randomCredential(t *testing.T) string {
	t.Helper()
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}

func (f *minioFixture) store(t *testing.T, client *http.Client) *s3store.Store {
	t.Helper()
	store, err := s3store.New(s3store.Config{Endpoint: f.endpoint, Bucket: "camp-test", PathStyle: true, HTTPClient: client, Signer: f.signer})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func (f *minioFixture) createBucket(t *testing.T, bucket string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := createMinIOBucketWhenReady(ctx, func() (*http.Response, error) {
		return f.do(t, http.MethodPut, "/"+bucket, nil, nil, nil), nil
	}); err != nil {
		t.Fatal(err)
	}
}

func createMinIOBucketWhenReady(ctx context.Context, request func() (*http.Response, error)) error {
	for {
		response, err := request()
		if err != nil {
			return fmt.Errorf("create MinIO bucket: %w", err)
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<10))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return fmt.Errorf("read MinIO bucket response: %w", errors.Join(readErr, closeErr))
		}
		if response.StatusCode == http.StatusOK {
			return nil
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			return fmt.Errorf("create bucket status = %s: %s", response.Status, body)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("create bucket status = %s before readiness deadline: %s: %w", response.Status, body, ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (f *minioFixture) initiateUpload(t *testing.T, bucket, key string) string {
	t.Helper()
	response := f.do(t, http.MethodPost, "/"+bucket+"/"+key, url.Values{"uploads": {""}}, nil, nil)
	defer response.Body.Close()
	var result struct {
		UploadID string `xml:"UploadId"`
	}
	if response.StatusCode != http.StatusOK || xml.NewDecoder(response.Body).Decode(&result) != nil || result.UploadID == "" {
		t.Fatalf("initiate multipart status = %s, upload ID = %q", response.Status, result.UploadID)
	}
	return result.UploadID
}

func (f *minioFixture) uploadPart(t *testing.T, bucket, key, uploadID string, body []byte) string {
	t.Helper()
	response := f.do(t, http.MethodPut, "/"+bucket+"/"+key, url.Values{"partNumber": {"1"}, "uploadId": {uploadID}}, nil, body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.Header.Get("ETag") == "" {
		t.Fatalf("upload part status = %s, ETag = %q", response.Status, response.Header.Get("ETag"))
	}
	return response.Header.Get("ETag")
}

func (f *minioFixture) putObject(t *testing.T, bucket, key string, body []byte) {
	t.Helper()
	response := f.do(t, http.MethodPut, "/"+bucket+"/"+key, nil, http.Header{"If-None-Match": {"*"}}, body)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("put object status = %s", response.Status)
	}
}

func (f *minioFixture) completeUpload(t *testing.T, bucket, key, uploadID, etag string, conditional bool) int {
	t.Helper()
	body, err := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []struct {
			ETag       string `xml:"ETag"`
			PartNumber int    `xml:"PartNumber"`
		} `xml:"Part"`
	}{Parts: []struct {
		ETag       string `xml:"ETag"`
		PartNumber int    `xml:"PartNumber"`
	}{{ETag: etag, PartNumber: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	header := make(http.Header)
	if conditional {
		header.Set("If-None-Match", "*")
	}
	response := f.do(t, http.MethodPost, "/"+bucket+"/"+key, url.Values{"uploadId": {uploadID}}, header, body)
	defer response.Body.Close()
	return response.StatusCode
}

func (f *minioFixture) listUploads(t *testing.T, bucket, prefix string) []string {
	t.Helper()
	response := f.do(t, http.MethodGet, "/"+bucket, url.Values{"uploads": {""}, "prefix": {prefix}}, nil, nil)
	defer response.Body.Close()
	var result struct {
		Uploads []struct {
			Key string `xml:"Key"`
		} `xml:"Upload"`
	}
	if response.StatusCode != http.StatusOK || xml.NewDecoder(response.Body).Decode(&result) != nil {
		t.Fatalf("list multipart uploads status = %s", response.Status)
	}
	keys := make([]string, 0, len(result.Uploads))
	for _, upload := range result.Uploads {
		keys = append(keys, upload.Key)
	}
	return keys
}

func (f *minioFixture) do(t *testing.T, method, requestPath string, query url.Values, header http.Header, body []byte) *http.Response {
	t.Helper()
	requestURL := f.endpoint + requestPath
	if len(query) != 0 {
		requestURL += "?" + query.Encode()
	}
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, requestURL, requestBody)
	if err != nil {
		t.Fatal(err)
	}
	request.Header = make(http.Header)
	for name, values := range header {
		request.Header[name] = append([]string(nil), values...)
	}
	if err := f.signer.Sign(request); err != nil {
		t.Fatal(err)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

type minioSigner struct {
	accessKey string
	secretKey string
}

func (s *minioSigner) Sign(request *http.Request) error {
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return err
		}
		_ = request.Body.Close()
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
	}
	payloadHash := sha256Hex(body)
	now := time.Now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	host := request.Host
	if host == "" {
		host = request.URL.Host
	}
	canonicalHeaders := "host:" + host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := strings.Join([]string{request.Method, request.URL.EscapedPath(), request.URL.Query().Encode(), canonicalHeaders, signedHeaders, payloadHash}, "\n")
	scope := date + "/us-east-1/s3/aws4_request"
	canonicalHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])
	dateKey := hmacSHA256([]byte("AWS4"+s.secretKey), date)
	regionKey := hmacSHA256(dateKey, "us-east-1")
	serviceKey := hmacSHA256(regionKey, "s3")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.accessKey+"/"+scope+", SignedHeaders="+signedHeaders+", Signature="+signature)
	return nil
}

func hmacSHA256(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

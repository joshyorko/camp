package s3store_test

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
	"strings"
	"sync"
	"testing"

	"github.com/joshyorko/camp/internal/ports"
)

type countingSource struct {
	mu    sync.Mutex
	body  []byte
	opens int
}

func TestPutImmutableRejectsSourceIntegrityBeforeMultipart(t *testing.T) {
	body := []byte("wrong bytes")
	source := &countingSource{body: body}
	requests := 0
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	}))

	_, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", source, strings.Repeat("0", 64), int64(len(body)))
	if !errors.Is(err, ports.ErrIntegrity) {
		t.Fatalf("error = %v, want integrity", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want only initial HEAD", requests)
	}
	if source.openCount() != 1 {
		t.Fatalf("source opens = %d, want 1", source.openCount())
	}
}

func TestPutImmutableCancellationAbortsMultipart(t *testing.T) {
	body := []byte("immutable generation archive")
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	ctx, cancel := context.WithCancel(context.Background())
	aborted := false
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			cancel()
			w.Header().Set("ETag", `"part-1"`)
		case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") == "upload-1":
			aborted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))

	_, err := store.PutImmutable(ctx, "capsule/generation.tar.zst", &countingSource{body: body}, digest, int64(len(body)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
	if !aborted {
		t.Fatal("multipart upload was not aborted")
	}
}

func TestPutImmutableVerifiesExistingObject(t *testing.T) {
	body := []byte("immutable generation archive")
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	getCalls := 0
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.Header().Set("ETag", `"existing"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("X-Amz-Meta-Sha256", digest)
		case http.MethodGet:
			getCalls++
			w.Header().Set("ETag", `"existing"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))

	meta, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", &countingSource{body: body}, digest, int64(len(body)))
	if err != nil || meta.Revision != `"existing"` {
		t.Fatalf("metadata = %#v, error = %v", meta, err)
	}
	if getCalls != 1 {
		t.Fatalf("verified GET calls = %d, want 1", getCalls)
	}

	different := append([]byte(nil), body...)
	different[0] ^= 1
	differentDigest := sha256.Sum256(different)
	_, err = store.PutImmutable(context.Background(), "capsule/generation.tar.zst", &countingSource{body: different}, hex.EncodeToString(differentDigest[:]), int64(len(different)))
	if !errors.Is(err, ports.ErrConflict) {
		t.Fatalf("different object error = %v, want conflict", err)
	}
}

func TestPutImmutableAbortsMultipartAfterUploadFailure(t *testing.T) {
	body := []byte("archive")
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	aborted := false
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			http.Error(w, "upload failed", http.StatusInternalServerError)
		case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") == "upload-1":
			aborted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))

	_, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", &countingSource{body: body}, digest, int64(len(body)))
	if err == nil {
		t.Fatal("PutImmutable succeeded, want upload error")
	}
	if !aborted {
		t.Fatal("failed multipart upload was not aborted")
	}
}

func TestPutImmutableRejectsReadbackMismatch(t *testing.T) {
	body := []byte("archive")
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	headCalls := 0
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			headCalls++
			if headCalls == 1 {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", `"revision"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("X-Amz-Meta-Sha256", strings.Repeat("f", 64))
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			w.Header().Set("ETag", `"part-1"`)
		case r.Method == http.MethodPost:
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"revision"</ETag></CompleteMultipartUploadResult>`)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))

	_, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", &countingSource{body: body}, digest, int64(len(body)))
	if !errors.Is(err, ports.ErrIntegrity) {
		t.Fatalf("PutImmutable error = %v, want integrity error", err)
	}
}

func TestPutImmutableUploadsMultipleOrderedParts(t *testing.T) {
	body := bytes.Repeat([]byte("x"), (8<<20)+1)
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	var partSizes []int
	var completedParts []int
	headCalls := 0
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			headCalls++
			if headCalls == 1 {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", `"revision"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("X-Amz-Meta-Sha256", digest)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			part, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			partSizes = append(partSizes, len(part))
			w.Header().Set("ETag", fmt.Sprintf(`"part-%s"`, r.URL.Query().Get("partNumber")))
		case r.Method == http.MethodPost:
			var complete struct {
				Parts []struct {
					PartNumber int `xml:"PartNumber"`
				} `xml:"Part"`
			}
			if err := xml.NewDecoder(r.Body).Decode(&complete); err != nil {
				t.Fatal(err)
			}
			for _, part := range complete.Parts {
				completedParts = append(completedParts, part.PartNumber)
			}
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"revision"</ETag></CompleteMultipartUploadResult>`)
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))

	if _, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", &countingSource{body: body}, digest, int64(len(body))); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(partSizes) != fmt.Sprint([]int{8 << 20, 1}) {
		t.Fatalf("part sizes = %v, want [%d 1]", partSizes, 8<<20)
	}
	if fmt.Sprint(completedParts) != fmt.Sprint([]int{1, 2}) {
		t.Fatalf("completed parts = %v, want [1 2]", completedParts)
	}
}

func TestPutImmutableReconcilesAmbiguousCompletionByReadback(t *testing.T) {
	body := []byte("archive")
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	completed := false
	aborted := false
	store := newStore(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && !completed:
			http.NotFound(w, r)
		case r.Method == http.MethodHead:
			w.Header().Set("ETag", `"revision"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("X-Amz-Meta-Sha256", digest)
		case r.Method == http.MethodGet && completed:
			w.Header().Set("ETag", `"revision"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Header().Set("X-Amz-Meta-Sha256", digest)
			_, _ = w.Write(body)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut:
			w.Header().Set("ETag", `"part-1"`)
		case r.Method == http.MethodPost:
			completed = true
			http.Error(w, "lost response", http.StatusInternalServerError)
		case r.Method == http.MethodDelete:
			aborted = true
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))

	meta, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", &countingSource{body: body}, digest, int64(len(body)))
	if err != nil || meta.Revision != `"revision"` {
		t.Fatalf("metadata = %#v, error = %v", meta, err)
	}
	if aborted {
		t.Fatal("reconciled completed upload was aborted")
	}
}

func (s *countingSource) Open() (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opens++
	return io.NopCloser(bytes.NewReader(s.body)), nil
}

func (s *countingSource) openCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.opens
}

func TestPutImmutableUsesMultipartAndVerifiesReadback(t *testing.T) {
	body := []byte("immutable generation archive")
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	source := &countingSource{body: body}

	var stored []byte
	var requests []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/bucket/capsule/generation.tar.zst":
			if stored == nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("ETag", `"multipart-revision"`)
			w.Header().Set("Content-Length", fmt.Sprint(len(stored)))
			w.Header().Set("X-Amz-Meta-Sha256", digest)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			if got := r.Header.Get("X-Amz-Meta-Sha256"); got != digest {
				t.Errorf("create checksum metadata = %q, want %q", got, digest)
			}
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>upload-1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && r.URL.Query().Get("uploadId") == "upload-1":
			part, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			stored = append(stored, part...)
			w.Header().Set("ETag", `"part-1"`)
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "upload-1":
			if got := r.Header.Get("If-None-Match"); got != "*" {
				t.Errorf("complete If-None-Match = %q, want *", got)
			}
			var complete struct {
				Parts []struct {
					ETag       string `xml:"ETag"`
					PartNumber int    `xml:"PartNumber"`
				} `xml:"Part"`
			}
			if err := xml.NewDecoder(r.Body).Decode(&complete); err != nil {
				t.Error(err)
			} else if len(complete.Parts) != 1 || complete.Parts[0].PartNumber != 1 || complete.Parts[0].ETag != `"part-1"` {
				t.Errorf("complete body = %#v", complete.Parts)
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"multipart-revision"</ETag></CompleteMultipartUploadResult>`)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	})

	store := newStore(t, handler)
	meta, err := store.PutImmutable(context.Background(), "capsule/generation.tar.zst", source, digest, int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, body) {
		t.Fatalf("stored body = %q, want %q", stored, body)
	}
	if source.openCount() != 2 {
		t.Fatalf("source opens = %d, want 2", source.openCount())
	}
	if meta.Key != "capsule/generation.tar.zst" || meta.Size != int64(len(body)) || meta.SHA256 != digest || meta.Revision != `"multipart-revision"` {
		t.Fatalf("metadata = %#v", meta)
	}
	joined := strings.Join(requests, "\n")
	for _, expected := range []string{"HEAD /bucket/capsule/generation.tar.zst", "POST /bucket/capsule/generation.tar.zst?uploads=", "PUT /bucket/capsule/generation.tar.zst?partNumber=1&uploadId=upload-1", "POST /bucket/capsule/generation.tar.zst?uploadId=upload-1"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("request log missing %q:\n%s", expected, joined)
		}
	}
}

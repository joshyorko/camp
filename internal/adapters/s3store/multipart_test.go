package s3store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

)

type countingSource struct {
	mu    sync.Mutex
	body  []byte
	opens int
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
